// Package validatejob validates monitor files in job mode: it runs each
// validation as a Kubernetes Job from the validator image and reads the
// result from the Job's pod (docs/validator.md). It implements
// validateclient.Validator, with the same errors as the sidecar client.
//
// A validation creates a Job, then a ConfigMap that holds the gzipped
// request and has the Job as its owner. The pod reads the request from the
// ConfigMap and writes the result to its termination message. Sidekick
// polls the Job, reads the message, and deletes the Job and the ConfigMap,
// also when the validation fails or times out. If Sidekick dies first, the
// Job's own deadline and its time to live remove everything.
package validatejob

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Limits of one validation.
const (
	// maxConcurrent is how many validations run at the same time in one
	// Sidekick process, like the validator's own limit.
	maxConcurrent = 2
	// slotWait is how long a validation waits for a free slot before it
	// gets ErrBusy, like the validator's own wait.
	slotWait = 60 * time.Second
	// validationTimeout is what the validator gives one validation.
	validationTimeout = 60 * time.Second
	// startAllowance is the time on top of validationTimeout for the
	// cluster to schedule the pod and pull the image.
	startAllowance = 30 * time.Second
	// maxRequestBytes is the most the gzipped request may take. A ConfigMap
	// holds at most 1 MiB, with its metadata counted in.
	maxRequestBytes = 1_000_000
)

// Timings of the Job and of the wait for it.
const (
	pollInterval = 500 * time.Millisecond
	// cleanupLimit bounds the calls that delete the Job and the ConfigMap,
	// which run even after the caller gave up.
	cleanupLimit = 10 * time.Second
	// jobTTL is how long after it finishes the cluster keeps a Job that
	// Sidekick could not delete.
	jobTTL = 5 * time.Minute
)

// Options configure a Client.
type Options struct {
	// Image is the validator image the Job runs. It is required.
	Image string
	// ImagePullPolicy is the pull policy of the image; empty means the
	// cluster's default.
	ImagePullPolicy corev1.PullPolicy
	// ImagePullSecrets are the names of the Secrets that give access to
	// the image.
	ImagePullSecrets []string
	// ServiceAccountName is the service account of the Job's pod. It should
	// not be Sidekick's own, and the pod mounts no token whichever it is.
	ServiceAccountName string
	// Resources are the resource requests and limits of the validator.
	Resources corev1.ResourceRequirements
	// WorkDirSize is the size limit of the directory that holds the files
	// of the validation; zero means no limit.
	WorkDirSize resource.Quantity
	// PodLabels are put on the Job, its pod and its ConfigMap. The
	// validator's NetworkPolicy selects the pod by them.
	PodLabels map[string]string
	// AllowDownloads lets OpenTofu download providers and modules.
	AllowDownloads bool
	// Namespace is where the Jobs run. Empty means the namespace of the
	// pod Sidekick runs in.
	Namespace string
	// Logger gets the warnings of the clean-up. Nil discards them.
	Logger *slog.Logger
}

// Client runs validations as Jobs. It is safe for concurrent use. It makes
// no call to the cluster until the first validation.
type Client struct {
	opts Options
	log  *slog.Logger

	slots chan struct{}

	// These are fields, not constants, so tests can change them.
	connect      func() (kubernetes.Interface, string, error)
	newName      func() string
	pollEvery    time.Duration
	waitLimit    time.Duration
	slotWaitTime time.Duration

	mu     sync.Mutex
	client kubernetes.Interface
	ns     string
}

var _ validateclient.Validator = (*Client)(nil)

// New returns a Client for the Jobs described by opts.
func New(opts Options) *Client {
	c := &Client{
		opts:         opts,
		log:          opts.Logger,
		slots:        make(chan struct{}, maxConcurrent),
		newName:      randomName,
		pollEvery:    pollInterval,
		waitLimit:    validationTimeout + startAllowance,
		slotWaitTime: slotWait,
	}

	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}

	c.connect = func() (kubernetes.Interface, string, error) { return inCluster(opts.Namespace) }

	return c
}

// Validate runs the request in a Job and returns the validator's result. A
// result that lists errors is not an error here: the files failed, and OK
// is false. The error wraps ErrBadRequest, ErrBusy, ErrTimeout,
// ErrUnavailable or ErrTooLarge of package validateclient.
func (c *Client) Validate(ctx context.Context, req validateclient.Request) (validateclient.Result, error) {
	payload, err := encodeRequest(req)
	if err != nil {
		return validateclient.Result{}, fmt.Errorf("prepare the request: %w", err)
	}

	release, err := c.acquire(ctx)
	if err != nil {
		return validateclient.Result{}, fmt.Errorf("wait for a free slot: %w", err)
	}

	defer release()

	kc, ns, err := c.cluster()
	if err != nil {
		return validateclient.Result{}, fmt.Errorf("reach the cluster: %w", err)
	}

	name := c.newName()

	job, err := kc.BatchV1().Jobs(ns).Create(ctx, c.job(name), metav1.CreateOptions{})
	if err != nil {
		return validateclient.Result{}, apiError(ctx, "create the Job", err)
	}

	defer c.cleanup(kc, ns, name)

	_, err = kc.CoreV1().ConfigMaps(ns).Create(ctx, requestMap(name, c.labels(name), job, payload), metav1.CreateOptions{})
	if err != nil {
		return validateclient.Result{}, apiError(ctx, "create the ConfigMap", err)
	}

	finished, err := c.wait(ctx, kc, ns, name)
	if err != nil {
		return validateclient.Result{}, fmt.Errorf("wait for the Job: %w", err)
	}

	pods, err := c.pods(ctx, kc, ns, name)
	if err != nil {
		return validateclient.Result{}, fmt.Errorf("read the pod of the Job: %w", err)
	}

	res, err := outcome(finished, pods)
	if err != nil {
		return validateclient.Result{}, fmt.Errorf("run the validation: %w", err)
	}

	return res, nil
}

// encodeRequest returns the request as gzipped JSON, or an error that wraps
// ErrTooLarge when the validator or the ConfigMap would not take it.
func encodeRequest(req validateclient.Request) ([]byte, error) {
	if len(req.Files) > validateclient.MaxFiles {
		return nil, fmt.Errorf("%d files, the validator takes at most %d: %w", len(req.Files), validateclient.MaxFiles, validateclient.ErrTooLarge)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode the request: %w", err)
	}

	if len(body) > validateclient.MaxRequestBytes {
		return nil, fmt.Errorf("%d bytes, the validator takes at most %d: %w", len(body), validateclient.MaxRequestBytes, validateclient.ErrTooLarge)
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)

	if _, err := zw.Write(body); err != nil {
		return nil, fmt.Errorf("compress the request: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compress the request: %w", err)
	}

	if buf.Len() > maxRequestBytes {
		return nil, fmt.Errorf("%d bytes after compression, job mode takes at most %d: %w", buf.Len(), maxRequestBytes, validateclient.ErrTooLarge)
	}

	return buf.Bytes(), nil
}

// acquire takes one of the slots, waiting up to the slot wait, and returns
// the function that gives it back.
func (c *Client) acquire(ctx context.Context) (func(), error) {
	select {
	case c.slots <- struct{}{}:
		return c.release, nil
	default:
	}

	wait := time.NewTimer(c.slotWaitTime)
	defer wait.Stop()

	select {
	case c.slots <- struct{}{}:
		return c.release, nil
	case <-wait.C:
		return nil, fmt.Errorf("%w: %d validations were running for %s", validateclient.ErrBusy, maxConcurrent, c.slotWaitTime)
	case <-ctx.Done():
		return nil, fmt.Errorf("call the validator: %w", ctx.Err())
	}
}

// release gives back a slot taken by acquire.
func (c *Client) release() {
	<-c.slots
}

// cluster returns the Kubernetes client and the namespace, connecting on the
// first call, and again after a failed one.
func (c *Client) cluster() (kubernetes.Interface, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil {
		return c.client, c.ns, nil
	}

	kc, ns, err := c.connect()
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", validateclient.ErrUnavailable, err)
	}

	c.client, c.ns = kc, ns

	return kc, ns, nil
}

// cleanup deletes the Job, which takes its pods and its ConfigMap along, and
// then the ConfigMap itself in case the cluster is slow to collect it. It
// does not depend on the caller's context, which may be the reason the
// validation stopped.
func (c *Client) cleanup(kc kubernetes.Interface, ns, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupLimit)
	defer cancel()

	background := metav1.DeletePropagationBackground

	if err := kc.BatchV1().Jobs(ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil && !apierrors.IsNotFound(err) {
		c.log.Warn("could not delete a validation Job; the cluster removes it after its deadline", "job", name, "error", err)
	}

	if err := kc.CoreV1().ConfigMaps(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		c.log.Warn("could not delete a validation ConfigMap; the cluster removes it with its Job", "configmap", name, "error", err)
	}
}

// apiError describes a failed call to the cluster. A caller that gave up
// gets its own context error, as with the sidecar client.
func apiError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("call the validator: %w", ctx.Err())
	}

	return fmt.Errorf("%w: %s: %w", validateclient.ErrUnavailable, what, err)
}

// randomName returns the name of one validation's Job and ConfigMap.
func randomName() string {
	var b [5]byte

	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("random bytes: %v", err))
	}

	return namePrefix + hex.EncodeToString(b[:])
}

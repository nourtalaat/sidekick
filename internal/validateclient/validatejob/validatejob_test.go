package validatejob

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// bothDeleted is what every validation that created a Job and a ConfigMap
// cleans up.
var bothDeleted = []string{"job/" + namePrefix + "1", "configmap/" + namePrefix + "1"}

func TestValidateResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    validateclient.Result
	}{
		{
			name:    "files that pass",
			message: `{"ok":true,"errors":[]}`,
			want:    validateclient.Result{OK: true, Errors: []validateclient.Problem{}},
		},
		{
			name:    "a result without an errors list",
			message: `{"ok":true}`,
			want:    validateclient.Result{OK: true, Errors: []validateclient.Problem{}},
		},
		{
			name:    "files that fail",
			message: `{"ok":false,"errors":[{"path":"a.yaml","line":3,"message":"bad"}],"notChecked":[{"path":"b.yaml","message":"loki"}]}`,
			want: validateclient.Result{
				Errors:     []validateclient.Problem{{Path: "a.yaml", Line: 3, Message: "bad"}},
				NotChecked: []validateclient.Problem{{Path: "b.yaml", Message: "loki"}},
			},
		},
		{
			name:    "a result cut to fit the termination message",
			message: `{"ok":false,"errors":[{"path":"a.yaml","message":"bad"}],"truncated":true}`,
			want:    validateclient.Result{Errors: []validateclient.Problem{{Path: "a.yaml", Message: "bad"}}, Truncated: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeAPI(t, []step{running(), complete(tt.message)}, faults{})
			c := api.client(Options{}, nil)

			got, err := c.Validate(t.Context(), requestOf())
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, bothDeleted, api.deleted())
		})
	}
}

func TestValidateCreatesTheObjects(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{complete(`{"ok":true,"errors":[]}`)}, faults{})
	c := api.client(Options{
		ImagePullPolicy:    corev1.PullIfNotPresent,
		ImagePullSecrets:   []string{"registry-login"},
		ServiceAccountName: "sidekick-validator",
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
		},
		WorkDirSize:    resource.MustParse("2Gi"),
		PodLabels:      map[string]string{"app.kubernetes.io/component": "validator"},
		AllowDownloads: true,
	}, nil)

	req := validateclient.Request{
		Format: validateclient.FormatTerraform,
		Dir:    "datadog",
		Files:  []validateclient.File{{Path: "datadog/main.tf", Content: strings.Repeat("resource {}\n", 100)}},
	}

	_, err := c.Validate(t.Context(), req)
	require.NoError(t, err)

	jobs := api.jobs()
	require.Len(t, jobs, 1)

	job := jobs[0]
	require.Equal(t, namePrefix+"1", job.Name)
	require.Equal(t, map[string]string{"app.kubernetes.io/component": "validator", validationLabel: job.Name}, job.Labels)
	require.Equal(t, job.Labels, job.Spec.Template.Labels)
	require.Equal(t, int32(0), *job.Spec.BackoffLimit)
	require.Equal(t, int64(90), *job.Spec.ActiveDeadlineSeconds)
	require.Equal(t, int32(300), *job.Spec.TTLSecondsAfterFinished)

	pod := job.Spec.Template.Spec
	require.Equal(t, corev1.RestartPolicyNever, pod.RestartPolicy)
	require.Equal(t, "sidekick-validator", pod.ServiceAccountName)
	require.False(t, *pod.AutomountServiceAccountToken)
	require.Equal(t, []corev1.LocalObjectReference{{Name: "registry-login"}}, pod.ImagePullSecrets)
	require.True(t, *pod.SecurityContext.RunAsNonRoot)

	require.Len(t, pod.Containers, 1)

	container := pod.Containers[0]
	require.Equal(t, "registry.example.com/sidekick-validator:1.2.3", container.Image)
	require.Equal(t, corev1.PullIfNotPresent, container.ImagePullPolicy)
	require.Equal(t, []string{"job", "--request", "/request/request.json.gz", "--work-dir", "/tmp", "--allow-downloads"}, container.Args)
	require.True(t, *container.SecurityContext.ReadOnlyRootFilesystem)
	require.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
	memory := container.Resources.Limits[corev1.ResourceMemory]
	require.Equal(t, "1Gi", memory.String())
	require.Equal(t, "2Gi", pod.Volumes[0].EmptyDir.SizeLimit.String())
	require.Equal(t, job.Name, pod.Volumes[1].ConfigMap.Name)

	maps := api.configMaps()
	require.Len(t, maps, 1)

	cm := maps[0]
	require.Equal(t, job.Name, cm.Name)
	require.True(t, *cm.Immutable)
	require.Equal(t, job.Labels, cm.Labels)
	require.Len(t, cm.OwnerReferences, 1)
	require.Equal(t, "Job", cm.OwnerReferences[0].Kind)
	require.Equal(t, job.Name, cm.OwnerReferences[0].Name)
	require.Equal(t, testUID, string(cm.OwnerReferences[0].UID))
	require.Nil(t, cm.OwnerReferences[0].BlockOwnerDeletion)

	zr, err := gzip.NewReader(bytes.NewReader(cm.BinaryData[requestKey]))
	require.NoError(t, err)

	body, err := io.ReadAll(zr)
	require.NoError(t, err)

	var sent validateclient.Request

	require.NoError(t, json.Unmarshal(body, &sent))
	require.Equal(t, req, sent)
}

func TestValidateErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		steps   []step
		wantErr error
		wantMsg string
	}{
		{
			name:    "the validator refused the request",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(1, "Error", `{"error":"bad request: no files","kind":"bad_request"}`))},
			wantErr: validateclient.ErrBadRequest,
			wantMsg: "bad request: no files",
		},
		{
			name:    "the validation timed out inside the pod",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(1, "Error", `{"error":"validation timed out after 1m0s","kind":"timeout"}`))},
			wantErr: validateclient.ErrTimeout,
		},
		{
			name:    "the validator was busy",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(1, "Error", `{"error":"the validator is busy","kind":"busy"}`))},
			wantErr: validateclient.ErrBusy,
		},
		{
			name:    "the validator could not run for another reason",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(1, "Error", `{"error":"start: no promtool","kind":"internal"}`))},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "start: no promtool",
		},
		{
			name:    "a failure from a validator that sends no kind",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(1, "Error", `{"error":"old validator"}`))},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "old validator",
		},
		{
			name:    "a failure message that is not JSON",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(2, "Error", "panic: boom"))},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "panic: boom",
		},
		{
			name:    "the Job ran into its deadline",
			steps:   []step{running(), failed("DeadlineExceeded", "Job was active longer than specified deadline")},
			wantErr: validateclient.ErrTimeout,
		},
		{
			name:    "the validator ran out of memory",
			steps:   []step{failed("BackoffLimitExceeded", "", podEnded(137, "OOMKilled", ""))},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "OOMKilled",
		},
		{
			name: "the pod was evicted",
			steps: []step{failed("BackoffLimitExceeded", "", corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed, Reason: "Evicted", Message: "The node was low on resource: memory.",
			}})},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "Evicted",
		},
		{
			name:    "a Job that failed with no pod",
			steps:   []step{failed("BackoffLimitExceeded", "Job has reached the specified backoff limit")},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "backoff limit",
		},
		{
			name:    "a finished Job with no result",
			steps:   []step{complete("")},
			wantErr: validateclient.ErrUnavailable,
		},
		{
			name:    "a result that is not JSON",
			steps:   []step{complete("not json")},
			wantErr: validateclient.ErrUnavailable,
		},
		{
			name:    "an image that cannot be pulled",
			steps:   []step{running(), podWaiting("ImagePullBackOff", "Back-off pulling image")},
			wantErr: validateclient.ErrUnavailable,
			wantMsg: "ImagePullBackOff",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeAPI(t, tt.steps, faults{})
			c := api.client(Options{}, nil)

			_, err := c.Validate(t.Context(), requestOf())
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Equal(t, bothDeleted, api.deleted())
		})
	}
}

func TestValidateTimesOut(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{running()}, faults{})
	c := api.client(Options{}, func(c *Client) { c.waitLimit = 40 * time.Millisecond })

	_, err := c.Validate(t.Context(), requestOf())
	require.ErrorIs(t, err, validateclient.ErrTimeout)
	require.Equal(t, bothDeleted, api.deleted())
}

func TestValidateCleansUpWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{running()}, faults{})
	c := api.client(Options{}, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	api.onJobRead = cancel

	_, err := c.Validate(ctx, requestOf())
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, validateclient.ErrUnavailable)
	require.Equal(t, bothDeleted, api.deleted())
}

func TestValidateClusterFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		faults      faults
		wantDeleted []string
		wantMsg     string
	}{
		{name: "RBAC refuses the Job", faults: faults{createJob: http.StatusForbidden}, wantMsg: "create the Job"},
		{name: "the ConfigMap cannot be made", faults: faults{createMap: http.StatusInternalServerError}, wantDeleted: []string{"job/" + namePrefix + "1", "configmap/" + namePrefix + "1"}, wantMsg: "create the ConfigMap"},
		{name: "the Job cannot be read", faults: faults{getJob: http.StatusInternalServerError}, wantDeleted: bothDeleted, wantMsg: "read the Job"},
		{name: "the pods cannot be listed", faults: faults{listPods: http.StatusForbidden}, wantDeleted: bothDeleted, wantMsg: "list the pods"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeAPI(t, []step{running()}, tt.faults)
			c := api.client(Options{}, nil)

			_, err := c.Validate(t.Context(), requestOf())
			require.ErrorIs(t, err, validateclient.ErrUnavailable)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Equal(t, tt.wantDeleted, api.deleted())
		})
	}
}

func TestValidateKeepsTheResultWhenCleanUpFails(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{complete(`{"ok":true,"errors":[]}`)}, faults{deleteJob: http.StatusInternalServerError, deleteMap: http.StatusForbidden})
	c := api.client(Options{}, nil)

	res, err := c.Validate(t.Context(), requestOf())
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Equal(t, bothDeleted, api.deleted(), "both deletes are tried even when the first fails")
}

func TestValidateRefusesLargeRequests(t *testing.T) {
	t.Parallel()

	manyFiles := make([]validateclient.File, validateclient.MaxFiles+1)
	for i := range manyFiles {
		manyFiles[i] = validateclient.File{Path: "a.yaml", Content: "x"}
	}

	tests := []struct {
		name string
		req  validateclient.Request
	}{
		{name: "too many files", req: validateclient.Request{Format: validateclient.FormatPrometheusRules, Files: manyFiles}},
		{
			name: "more than the validator takes",
			req: validateclient.Request{Format: validateclient.FormatPrometheusRules, Files: []validateclient.File{
				{Path: "a.yaml", Content: strings.Repeat("a", validateclient.MaxRequestBytes)},
			}},
		},
		{
			name: "more than a ConfigMap holds after compression",
			req: validateclient.Request{Format: validateclient.FormatPrometheusRules, Files: []validateclient.File{
				{Path: "a.yaml", Content: randomText(t, maxRequestBytes*5/4)},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeAPI(t, nil, faults{})
			c := api.client(Options{}, nil)

			_, err := c.Validate(t.Context(), tt.req)
			require.ErrorIs(t, err, validateclient.ErrTooLarge)
			api.requireNoCluster()
		})
	}
}

func TestValidateCompressesLargeRequests(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{complete(`{"ok":true,"errors":[]}`)}, faults{})
	c := api.client(Options{}, nil)

	// Several MiB of text, more than a ConfigMap holds, that gzip makes small.
	req := validateclient.Request{Format: validateclient.FormatPrometheusRules, Files: []validateclient.File{
		{Path: "a.yaml", Content: strings.Repeat("groups: []\n", 400_000)},
	}}

	_, err := c.Validate(t.Context(), req)
	require.NoError(t, err)
	require.Less(t, len(api.configMaps()[0].BinaryData[requestKey]), maxRequestBytes)
}

func TestValidateAtMostTwoAtATime(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{running()}, faults{})
	c := api.client(Options{}, func(c *Client) { c.slotWaitTime = 30 * time.Millisecond })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 2)

	for range maxConcurrent {
		go func() {
			_, err := c.Validate(ctx, requestOf())
			done <- err
		}()
	}

	require.Eventually(t, func() bool { return len(api.jobs()) == maxConcurrent }, 5*time.Second, time.Millisecond)

	_, err := c.Validate(t.Context(), requestOf())
	require.ErrorIs(t, err, validateclient.ErrBusy)
	require.Len(t, api.jobs(), maxConcurrent, "the third validation never made a Job")

	cancel()

	for range maxConcurrent {
		require.ErrorIs(t, <-done, context.Canceled)
	}

	require.Len(t, api.deleted(), 2*maxConcurrent, "both Jobs and both ConfigMaps are cleaned up")
}

func TestValidateAFreedSlotIsUsed(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t, []step{complete(`{"ok":true,"errors":[]}`)}, faults{})
	c := api.client(Options{}, nil)

	for range 3 {
		_, err := c.Validate(t.Context(), requestOf())
		require.NoError(t, err)
	}

	require.Len(t, api.jobs(), 3)
}

func TestClusterIsReachedOnFirstUse(t *testing.T) {
	t.Parallel()

	errOutside := errors.New("not in a cluster")
	calls := 0

	c := New(Options{Image: "validator:1"})
	c.connect = func() (kubernetes.Interface, string, error) {
		calls++

		return nil, "", errOutside
	}

	require.Zero(t, calls, "New makes no call")

	for range 2 {
		_, err := c.Validate(t.Context(), requestOf())
		require.ErrorIs(t, err, validateclient.ErrUnavailable)
		require.ErrorIs(t, err, errOutside)
	}

	require.Equal(t, 2, calls, "a failed connection is tried again")
}

// randomText returns base64 text of n random bytes, which gzip cannot
// shrink below about three quarters of its length.
func randomText(t *testing.T, n int) string {
	t.Helper()

	raw := make([]byte, n)
	_, err := rand.Read(raw)
	require.NoError(t, err)

	return base64.StdEncoding.EncodeToString(raw)
}

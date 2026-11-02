package validatejob

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// maxErrorChars is the most of a failure text that an error keeps.
const maxErrorChars = 500

// The kinds of failure that sidekick-validate job reports, and the shape
// of its failure message. They are copies, so this package does not link the
// validator's tools; a test checks them against the validator's.
const (
	kindBadRequest = "bad_request"
	kindTimeout    = "timeout"
	kindBusy       = "busy"
)

// jobFailure is what the job command writes when it could not run.
type jobFailure struct {
	Error string `json:"error"`
	Kind  string `json:"kind"`
}

// deadlineExceeded is the reason of a Job that ran into its deadline.
const deadlineExceeded = "DeadlineExceeded"

// stuckReasons are the reasons a container waits for that no waiting
// will fix, such as an image that cannot be pulled.
var stuckReasons = map[string]bool{
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"ErrImageNeverPull":          true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

// wait polls the Job until it finishes and returns it. A pod that is stuck
// ends the wait at once with ErrUnavailable; a Job that is still not done
// when the wait limit ends it gets ErrTimeout.
func (c *Client) wait(ctx context.Context, kc kubernetes.Interface, ns, name string) (*batchv1.Job, error) {
	limit := time.NewTimer(c.waitLimit)
	defer limit.Stop()

	tick := time.NewTicker(c.pollEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("call the validator: %w", ctx.Err())
		case <-limit.C:
			return nil, fmt.Errorf("%w: the Job did not finish in %s", validateclient.ErrTimeout, c.waitLimit)
		case <-tick.C:
		}

		job, err := kc.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, apiError(ctx, "read the Job", err)
		}

		if jobComplete(job) || failedCondition(job) != nil {
			return job, nil
		}

		pods, err := c.pods(ctx, kc, ns, name)
		if err != nil {
			return nil, fmt.Errorf("look at the pod: %w", err)
		}

		if err := stuck(pods); err != nil {
			return nil, fmt.Errorf("check the pod: %w", err)
		}
	}
}

// pods lists the pods of one validation.
func (*Client) pods(ctx context.Context, kc kubernetes.Interface, ns, name string) ([]corev1.Pod, error) {
	list, err := kc.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector(name)})
	if err != nil {
		return nil, apiError(ctx, "list the pods", err)
	}

	return list.Items, nil
}

// stuck returns an error that wraps ErrUnavailable when a pod waits for
// something that will not come.
func stuck(pods []corev1.Pod) error {
	for _, pod := range pods {
		for _, status := range pod.Status.ContainerStatuses {
			if w := status.State.Waiting; w != nil && stuckReasons[w.Reason] {
				return fmt.Errorf("%w: the validator pod cannot start: %s: %s", validateclient.ErrUnavailable, w.Reason, shorten(w.Message))
			}
		}
	}

	return nil
}

// outcome turns a finished Job and its pods into the answer to the
// request.
func outcome(job *batchv1.Job, pods []corev1.Pod) (validateclient.Result, error) {
	state := terminated(pods)

	if jobComplete(job) {
		return decodeResult(state)
	}

	return validateclient.Result{}, failure(job, state, pods)
}

// decodeResult reads the result a validator pod wrote to its termination
// message.
func decodeResult(state *corev1.ContainerStateTerminated) (validateclient.Result, error) {
	if state == nil || state.Message == "" {
		return validateclient.Result{}, fmt.Errorf("%w: the Job finished without a result", validateclient.ErrUnavailable)
	}

	var res validateclient.Result
	if err := json.Unmarshal([]byte(state.Message), &res); err != nil {
		return validateclient.Result{}, fmt.Errorf("%w: decode the result: %w", validateclient.ErrUnavailable, err)
	}

	if res.Errors == nil {
		res.Errors = []validateclient.Problem{}
	}

	return res, nil
}

// failure returns the error for a Job that failed. The validator says why
// in its termination message; when it could not, the Job's own reason and
// the pod tell.
func failure(job *batchv1.Job, state *corev1.ContainerStateTerminated, pods []corev1.Pod) error {
	if state != nil && state.Message != "" {
		var f jobFailure
		if err := json.Unmarshal([]byte(state.Message), &f); err != nil || f.Error == "" {
			return fmt.Errorf("%w: the validator exited with code %d: %s", validateclient.ErrUnavailable, state.ExitCode, shorten(state.Message))
		}

		return fmt.Errorf("%w: %s", kindError(f.Kind), shorten(f.Error))
	}

	cond := failedCondition(job)

	switch {
	case cond != nil && cond.Reason == deadlineExceeded:
		return fmt.Errorf("%w: the Job ran into its deadline", validateclient.ErrTimeout)
	case state != nil:
		return fmt.Errorf("%w: the validator exited with code %d (%s) without a result", validateclient.ErrUnavailable, state.ExitCode, state.Reason)
	case len(pods) > 0 && pods[0].Status.Reason != "":
		return fmt.Errorf("%w: the validator pod failed: %s: %s", validateclient.ErrUnavailable, pods[0].Status.Reason, shorten(pods[0].Status.Message))
	case cond != nil:
		return fmt.Errorf("%w: the Job failed: %s: %s", validateclient.ErrUnavailable, cond.Reason, shorten(cond.Message))
	default:
		return fmt.Errorf("%w: the Job failed", validateclient.ErrUnavailable)
	}
}

// kindError returns the error kind for a failure kind of the validator.
func kindError(kind string) error {
	switch kind {
	case kindBadRequest:
		return validateclient.ErrBadRequest
	case kindTimeout:
		return validateclient.ErrTimeout
	case kindBusy:
		return validateclient.ErrBusy
	default:
		return validateclient.ErrUnavailable
	}
}

// terminated returns how the validator container ended in the first pod
// that has one, or nil.
func terminated(pods []corev1.Pod) *corev1.ContainerStateTerminated {
	for _, pod := range pods {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == containerName && status.State.Terminated != nil {
				return status.State.Terminated
			}
		}
	}

	return nil
}

// jobComplete reports whether the Job succeeded.
func jobComplete(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// failedCondition returns the condition that says the Job failed, or nil.
func failedCondition(job *batchv1.Job) *batchv1.JobCondition {
	for i, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return &job.Status.Conditions[i]
		}
	}

	return nil
}

// shorten cuts text to the length an error keeps.
func shorten(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > maxErrorChars {
		return string(runes[:maxErrorChars-1]) + "…"
	}

	return string(runes)
}

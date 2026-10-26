package kubernetes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestListPodProblems(t *testing.T) {
	t.Parallel()

	healthy := pod("prod", "checkout-ok", corev1.PodRunning, map[string]string{"app": "checkout"},
		corev1.ContainerStatus{Name: "app", Ready: true, State: running()})
	crashing := pod("prod", "checkout-crash", corev1.PodRunning, map[string]string{"app": "checkout"},
		corev1.ContainerStatus{
			Name: "app", RestartCount: 12,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: oomKilled, ExitCode: 137, FinishedAt: at(2 * time.Minute),
			}},
		})
	crashing.OwnerReferences = []metav1.OwnerReference{{Kind: kindReplicaSet, Name: "checkout-7d9", Controller: ptr(true)}}
	unschedulable := pod("prod", "worker-pending", corev1.PodPending, map[string]string{"app": "worker"})
	unschedulable.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes are available: 3 Insufficient memory.",
	}}
	evicted := pod("staging", "web-evicted", corev1.PodFailed, map[string]string{"app": "web"})
	evicted.Status.Reason, evicted.Status.Message = "Evicted", "The node was low on resource: memory."
	notReady := pod("staging", "web-not-ready", corev1.PodRunning, map[string]string{"app": "web"},
		corev1.ContainerStatus{Name: "web", State: running()})
	starting := pod("staging", "web-starting", corev1.PodPending, map[string]string{"app": "web"},
		corev1.ContainerStatus{Name: "web", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}})
	done := pod("staging", "job-done", corev1.PodSucceeded, nil)
	badInit := pod("staging", "web-init", corev1.PodPending, map[string]string{"app": "web"})
	badInit.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "migrate", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}

	objects := []runtime.Object{healthy, crashing, unschedulable, evicted, notReady, starting, done, badInit}

	tests := []struct {
		name       string
		cfg        Config
		reactors   []reactor
		input      string
		wantPods   []string
		wantText   []string
		wantNoData bool
		wantTool   []string
		wantErr    error
	}{
		{
			name:     "every namespace, most restarts first",
			input:    `{}`,
			wantPods: []string{"checkout-crash", "worker-pending", "web-evicted", "web-init", "web-not-ready", "web-starting"},
			wantText: []string{
				"6 pods with problems in every namespace (8 pods checked), most restarts first:",
				"prod/checkout-crash phase Running on node node-1, owned by ReplicaSet/checkout-7d9, started 2026-10-26T11:00:00Z",
				"  app: waiting, CrashLoopBackOff: back-off 5m0s restarting failed container",
				"  app: last run ended with exit code 137 (OOMKilled) at 2026-10-26T11:58:00Z",
				"  app: 12 restarts",
				"  Pending, not scheduled: Unschedulable: 0/3 nodes are available: 3 Insufficient memory.",
				"  Failed: Evicted: The node was low on resource: memory.",
				"  migrate (init): waiting, ImagePullBackOff",
				"  web: running but not ready",
			},
		},
		{name: "a label selector", input: `{"labelSelector":"app=checkout"}`, wantPods: []string{"checkout-crash"}},
		{name: "a set-based selector", input: `{"labelSelector":"app in (worker,checkout)"}`, wantPods: []string{"checkout-crash", "worker-pending"}},
		{name: "the configured namespaces only", cfg: Config{Namespaces: []string{"prod"}}, input: `{}`, wantPods: []string{"checkout-crash", "worker-pending"}},
		{
			name:     "a limit",
			input:    `{"namespace":"prod","limit":1}`,
			wantPods: []string{"checkout-crash"},
			wantText: []string{"1 of 2 pods with problems in namespace prod (3 pods checked)"},
		},
		{
			name:       "no problems",
			input:      `{"namespace":"prod","labelSelector":"app=checkout,track=stable"}`,
			wantNoData: true,
			wantText:   []string{"No pod has a problem in namespace prod (0 pods checked)."},
		},
		{name: "a bad selector", input: `{"labelSelector":"app in ("}`, wantTool: []string{"labelSelector:"}},
		{
			name:     "a missing permission",
			reactors: []reactor{failWith("list", "pods", forbidden("", "pods"))},
			input:    `{}`,
			wantTool: []string{"may not list pods in any namespace", `the "list" verb on "pods" in the core API group`},
		},
		{
			name:     "an API server that cannot be reached",
			reactors: []reactor{failWith("list", "pods", errConnection)},
			input:    `{}`,
			wantErr:  errConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, objects, tt.reactors...)

			res, err := call(t, s, toolListPodProblems, tt.input)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)

				return
			case tt.wantTool != nil:
				requireToolError(t, err, tt.wantTool...)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantNoData, res.NoData)
			require.NotEmpty(t, res.Evidence)

			for _, want := range tt.wantText {
				require.Contains(t, res.Text, want)
			}

			if tt.wantNoData {
				return
			}

			var out podProblemsOutput
			require.NoError(t, json.Unmarshal(res.Structured, &out))

			names := make([]string, 0, len(out.Pods))
			for _, p := range out.Pods {
				names = append(names, p.Name)
			}

			require.Equal(t, tt.wantPods, names)
		})
	}
}

// pod returns a pod in the phase with the container statuses, started an
// hour before testNow on node-1.
func pod(ns, name string, phase corev1.PodPhase, labels map[string]string, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: phase, StartTime: ptr(at(time.Hour)), ContainerStatuses: statuses},
	}
}

// running is the state of a running container.
func running() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: at(time.Hour)}}
}

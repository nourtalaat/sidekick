package kubernetes

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestGetWorkloadPods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		objects       []runtime.Object
		reactors      []reactor
		input         string
		wantPods      []string
		wantMatched   int
		wantText      []string
		wantPodsError string
		wantJSON      string
	}{
		{
			name:        "a deployment lists its own pods, not ready first, with their revisions",
			objects:     deploymentWithPods(),
			input:       `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantPods:    []string{"checkout-7d9-b", "checkout-6c1-x", "checkout-7d9-a"},
			wantMatched: 3,
			wantText: []string{
				"Pods (3, not ready first):",
				"  checkout-7d9-b Running, 0/1 ready, 3 restarts, node node-1, started 2026-10-26T11:00:00Z, revision 7 (ReplicaSet checkout-7d9)",
				"  checkout-6c1-x Running, 1/1 ready, 0 restarts, node node-1, started 2026-10-26T11:00:00Z, revision 6 (ReplicaSet checkout-6c1)",
			},
		},
		{
			name:        "a statefulset lists its own pods and not those of a workload with an overlapping name and label",
			objects:     statefulSetWithPods(),
			input:       `{"namespace":"prod","kind":"StatefulSet","name":"db"}`,
			wantPods:    []string{"db-0", "db-1"},
			wantMatched: 2,
			wantText:    []string{"  db-0 Running, 1/1 ready, 0 restarts, node node-1, started 2026-10-26T11:00:00Z, revision db-5d8"},
		},
		{
			name:        "a daemonset lists its own pods and not those of a workload with the same name and label",
			objects:     daemonSetWithPods(),
			input:       `{"namespace":"prod","kind":"DaemonSet","name":"agent"}`,
			wantPods:    []string{"agent-b", "agent-a"},
			wantMatched: 2,
			wantText:    []string{"  agent-b Running, 0/1 ready, 1 restart, node node-1, started 2026-10-26T11:00:00Z, revision agent-77"},
		},
		{
			name:        "more pods than the cap",
			objects:     manyPods(defaultPods + 3),
			input:       `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantPods:    append([]string{fmt.Sprintf("web-%02d", defaultPods+2)}, podNames("web-%02d", 0, defaultPods-2)...),
			wantMatched: defaultPods + 3,
			wantText:    []string{fmt.Sprintf("Pods (first %d of %d, not ready first):", defaultPods, defaultPods+3)},
		},
		{
			name:     "a workload with no pods",
			objects:  []runtime.Object{deployment("checkout", "dep-uid")},
			input:    `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantText: []string{"Pods: none"},
			wantJSON: `"pods":[]`,
		},
		{
			name:          "pods that cannot be listed",
			objects:       deploymentWithPods(),
			reactors:      []reactor{failWith("list", "pods", forbidden("", "pods"))},
			input:         `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantText:      []string{"Pods: unknown, Sidekick's account may not list pods in namespace prod"},
			wantPodsError: "Sidekick's account may not list pods in namespace prod",
			wantJSON:      `"pods":[]`,
		},
		{
			name:          "replicasets that cannot be listed",
			objects:       deploymentWithPods(),
			reactors:      []reactor{failWith("list", "replicasets", forbidden("apps", "replicasets"))},
			input:         `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantText:      []string{"Pods: unknown, Sidekick's account may not list replicasets in namespace prod"},
			wantPodsError: "Sidekick's account may not list replicasets in namespace prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, Config{}, tt.objects, tt.reactors...)

			res, err := call(t, s, toolGetWorkload, tt.input)
			require.NoError(t, err)

			var view workloadView
			require.NoError(t, json.Unmarshal(res.Structured, &view))

			names := make([]string, 0, len(view.Pods))
			for _, p := range view.Pods {
				names = append(names, p.Name)
			}

			require.Equal(t, append([]string{}, tt.wantPods...), names)
			require.Equal(t, tt.wantMatched, view.PodsMatched)

			if tt.wantPodsError == "" {
				require.Empty(t, view.PodsError)
			} else {
				require.Contains(t, view.PodsError, tt.wantPodsError)
			}

			for _, want := range tt.wantText {
				require.Contains(t, res.Text, want)
			}

			if tt.wantJSON != "" {
				require.Contains(t, string(res.Structured), tt.wantJSON)
			}
		})
	}
}

func TestGetWorkloadPodFields(t *testing.T) {
	t.Parallel()

	s := newTestSource(t, Config{}, deploymentWithPods())

	res, err := call(t, s, toolGetWorkload, `{"namespace":"prod","kind":"Deployment","name":"checkout"}`)
	require.NoError(t, err)

	var view workloadView
	require.NoError(t, json.Unmarshal(res.Structured, &view))
	require.NotEmpty(t, view.Pods)

	want := podView{
		Name: "checkout-7d9-b", Phase: "Running", Ready: "0/1", Restarts: 3, Node: "node-1",
		Started: testNow.Add(-time.Hour), Revision: "7", ReplicaSet: "checkout-7d9",
	}

	got := view.Pods[0]
	require.True(t, want.Started.Equal(got.Started))

	got.Started, want.Started = time.Time{}, time.Time{}
	require.Equal(t, want, got)
}

// deploymentWithPods returns the Deployment checkout in prod with two
// ReplicaSets and their pods, and things that must not show up: another
// Deployment with the same label and its pod, a pod with no owner, a pod
// of a ReplicaSet with the right name and another UID, and a pod with the
// same label in another namespace.
func deploymentWithPods() []runtime.Object {
	current := replicaSet("checkout-7d9", "dep-uid", 7, time.Hour, "", "app=checkout:2")
	current.UID = "rs-7d9"
	previous := replicaSet("checkout-6c1", "dep-uid", 6, 2*time.Hour, "", "app=checkout:1")
	previous.UID = "rs-6c1"

	canary := replicaSet("checkout-canary-1", "canary-uid", 1, time.Hour, "", "app=checkout:3")
	canary.UID = "rs-canary"
	canary.OwnerReferences[0].Name = "checkout-canary"

	labels := map[string]string{"app": "checkout"}

	ok := ownedPod("prod", "checkout-7d9-a", kindReplicaSet, "checkout-7d9", "rs-7d9", labels, true)
	failing := ownedPod("prod", "checkout-7d9-b", kindReplicaSet, "checkout-7d9", "rs-7d9", labels, false)
	failing.Status.ContainerStatuses[0].RestartCount = 2
	failing.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "migrate", RestartCount: 1}}

	return []runtime.Object{
		deployment("checkout", "dep-uid"), deployment("checkout-canary", "canary-uid"),
		current, previous, canary,
		ok, failing,
		ownedPod("prod", "checkout-6c1-x", kindReplicaSet, "checkout-6c1", "rs-6c1", labels, true),
		ownedPod("prod", "checkout-canary-1-y", kindReplicaSet, "checkout-canary-1", "rs-canary", labels, true),
		ownedPod("prod", "checkout-7d9-stale", kindReplicaSet, "checkout-7d9", "another-uid", labels, true),
		ownedPod("prod", "checkout-orphan", "", "", "", labels, true),
		ownedPod("staging", "checkout-7d9-c", kindReplicaSet, "checkout-7d9", "rs-7d9", labels, true),
	}
}

// statefulSetWithPods returns the StatefulSet db in prod and its pods, and
// a StatefulSet db-replica and a DaemonSet db with the same label and their
// pods.
func statefulSetWithPods() []runtime.Object {
	labels := map[string]string{"app": "db", controllerRevisionLabel: "db-5d8"}

	st := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db", UID: "st-uid"},
		Spec:       appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
	}
	replica := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db-replica", UID: "replica-uid"},
		Spec:       appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
	}
	sameName := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db", UID: "ds-uid"},
		Spec:       appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
	}

	return []runtime.Object{
		st, replica, sameName,
		ownedPod("prod", "db-0", kindStatefulSet, "db", "st-uid", labels, true),
		ownedPod("prod", "db-1", kindStatefulSet, "db", "st-uid", labels, true),
		ownedPod("prod", "db-replica-0", kindStatefulSet, "db-replica", "replica-uid", labels, true),
		ownedPod("prod", "db-agent-x", kindDaemonSet, "db", "ds-uid", labels, true),
	}
}

// daemonSetWithPods returns the DaemonSet agent in prod and its pods, and
// a StatefulSet agent with the same label and its pod, and a pod of the
// DaemonSet's label in another namespace.
func daemonSetWithPods() []runtime.Object {
	labels := map[string]string{"app": "agent", controllerRevisionLabel: "agent-77"}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "agent", UID: "ds-uid"},
		Spec:       appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent"}}},
	}
	st := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "agent", UID: "st-uid"},
		Spec:       appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent"}}},
	}

	restarting := ownedPod("prod", "agent-b", kindDaemonSet, "agent", "ds-uid", labels, false)
	restarting.Status.ContainerStatuses[0].RestartCount = 1

	return []runtime.Object{
		ds, st,
		ownedPod("prod", "agent-a", kindDaemonSet, "agent", "ds-uid", labels, true),
		restarting,
		ownedPod("prod", "agent-0", kindStatefulSet, "agent", "st-uid", labels, true),
		ownedPod("staging", "agent-c", kindDaemonSet, "agent", "ds-uid", labels, true),
	}
}

// manyPods returns the Deployment checkout in prod with one ReplicaSet and
// n pods named web-00 and on, of which only the last is not ready.
func manyPods(n int) []runtime.Object {
	rs := replicaSet("checkout-7d9", "dep-uid", 7, time.Hour, "", "app=checkout:2")
	rs.UID = "rs-7d9"

	objects := []runtime.Object{deployment("checkout", "dep-uid"), rs}

	for i := range n {
		objects = append(objects, ownedPod("prod", fmt.Sprintf("web-%02d", i), kindReplicaSet, "checkout-7d9", "rs-7d9", map[string]string{"app": "checkout"}, i != n-1))
	}

	return objects
}

// podNames returns the names of the pods from first to last, by the format.
func podNames(format string, first, last int) []string {
	var names []string

	for i := first; i <= last; i++ {
		names = append(names, fmt.Sprintf(format, i))
	}

	return names
}

// deployment returns a Deployment in prod that selects the pods of its own
// name's label app=checkout.
func deployment(name string, uid types.UID) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: name, UID: uid},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}}},
	}
}

// ownedPod returns a running pod with one container, started an hour
// before testNow, that the controller of the kind, name and UID owns. An
// empty kind means no owner.
func ownedPod(ns, name, kind, owner string, uid types.UID, labels map[string]string, ready bool) *corev1.Pod {
	p := pod(ns, name, corev1.PodRunning, labels, corev1.ContainerStatus{Name: "app", Ready: ready, State: running()})
	p.Spec.Containers = []corev1.Container{{Name: "app"}}

	if kind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, Name: owner, UID: uid, Controller: ptr(true)}}
	}

	return p
}

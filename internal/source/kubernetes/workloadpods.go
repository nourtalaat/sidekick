package kubernetes

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// controllerRevisionLabel is the label that names the controller revision
// of a StatefulSet's or DaemonSet's pod.
const controllerRevisionLabel = "controller-revision-hash"

// podScope is what finds the pods of a workload: its label selector and
// its UID.
type podScope struct {
	selector *metav1.LabelSelector
	uid      types.UID
}

// podView is one pod of a workload.
type podView struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
	// Ready is the ready containers over all containers, for example 1/2.
	Ready string `json:"ready"`
	// Restarts counts the restarts of every container, init containers
	// included.
	Restarts int32     `json:"restarts"`
	Node     string    `json:"node,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	// Revision is the Deployment revision of the pod's ReplicaSet, or the
	// controller revision of a StatefulSet's or DaemonSet's pod.
	Revision string `json:"revision,omitempty"`
	// ReplicaSet is the ReplicaSet of a Deployment's pod.
	ReplicaSet string `json:"replicaSet,omitempty"`

	notReady bool
}

// ownerKey names a controller: the name and UID in an owner reference.
type ownerKey struct {
	name string
	uid  types.UID
}

// findPods returns the pods of a workload, not ready first, at most
// defaultPods of them, and how many pods it has. A pod belongs to the
// workload only when the workload's own selector finds it and its
// controller is the workload, or for a Deployment a ReplicaSet that the
// Deployment controls, so pods of another workload with the same labels
// are left out. When pods cannot be read, it returns why instead, since
// the workload itself was read.
func findPods(ctx context.Context, c kubernetes.Interface, kind, ns string, scope podScope) ([]podView, int, string) {
	none := []podView{}

	if scope.selector == nil {
		return none, 0, ""
	}

	selector, err := metav1.LabelSelectorAsSelector(scope.selector)
	if err != nil {
		return none, 0, fmt.Sprintf("the selector of the workload cannot be read: %s", err)
	}

	if selector.Empty() {
		return none, 0, ""
	}

	var replicaSets map[ownerKey]int64

	if kind == kindDeployment {
		replicaSets, err = replicaSetsOf(ctx, c, ns, selector.String(), scope.uid)
		if err != nil {
			return none, 0, messageOf(err)
		}
	}

	pods := none

	err = eachPage(ctx, metav1.ListOptions{LabelSelector: selector.String()}, c.CoreV1().Pods(ns).List, func(page *corev1.PodList) {
		for i := range page.Items {
			if p, ok := podOf(&page.Items[i], kind, scope.uid, replicaSets); ok {
				pods = append(pods, p)
			}
		}
	})
	if err != nil {
		return none, 0, messageOf(apiError("list", resPods, ns, err))
	}

	sortPods(pods)

	return pods[:min(len(pods), defaultPods)], len(pods), ""
}

// replicaSetsOf returns the ReplicaSets that the Deployment with the UID
// controls, with their revision numbers.
func replicaSetsOf(ctx context.Context, c kubernetes.Interface, ns, selector string, deployment types.UID) (map[ownerKey]int64, error) {
	owned := map[ownerKey]int64{}

	err := eachPage(ctx, metav1.ListOptions{LabelSelector: selector}, c.AppsV1().ReplicaSets(ns).List, func(page *appsv1.ReplicaSetList) {
		for i := range page.Items {
			rs := &page.Items[i]
			if owner := metav1.GetControllerOf(rs); owner != nil && owner.UID == deployment {
				owned[ownerKey{name: rs.Name, uid: rs.UID}] = revisionNumber(rs.Annotations)
			}
		}
	})
	if err != nil {
		return nil, apiError("list", resReplicaSets, ns, err)
	}

	return owned, nil
}

// podOf summarizes the pod when the workload of the kind and UID controls
// it, or for a Deployment when one of its ReplicaSets does.
func podOf(pod *corev1.Pod, kind string, uid types.UID, replicaSets map[ownerKey]int64) (podView, bool) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return podView{}, false
	}

	p := podView{
		Name:  pod.Name,
		Phase: string(pod.Status.Phase),
		Node:  pod.Spec.NodeName,
	}

	if kind == kindDeployment {
		revision, ok := replicaSets[ownerKey{name: owner.Name, uid: owner.UID}]
		if owner.Kind != kindReplicaSet || !ok {
			return podView{}, false
		}

		p.ReplicaSet = owner.Name

		if revision > 0 {
			p.Revision = fmt.Sprint(revision)
		}
	} else {
		if owner.Kind != kind || owner.UID != uid {
			return podView{}, false
		}

		p.Revision = pod.Labels[controllerRevisionLabel]
	}

	if pod.Status.StartTime != nil {
		p.Started = pod.Status.StartTime.Time
	}

	ready := 0

	for _, cs := range pod.Status.ContainerStatuses {
		p.Restarts += cs.RestartCount

		if cs.Ready {
			ready++
		}
	}

	for _, cs := range pod.Status.InitContainerStatuses {
		p.Restarts += cs.RestartCount
	}

	total := len(pod.Spec.Containers)
	p.Ready = fmt.Sprintf("%d/%d", ready, total)
	p.notReady = ready < total

	return p, true
}

// sortPods puts pods that are not ready first, then orders by name.
func sortPods(pods []podView) {
	slices.SortFunc(pods, func(a, b podView) int {
		switch {
		case a.notReady && !b.notReady:
			return -1
		case !a.notReady && b.notReady:
			return 1
		default:
			return cmp.Compare(a.Name, b.Name)
		}
	})
}

// writePods writes the heading and one line per pod of the workload.
func writePods(b *strings.Builder, v workloadView) {
	switch {
	case v.PodsError != "":
		fmt.Fprintf(b, "Pods: unknown, %s\n", v.PodsError)

		return
	case v.PodsMatched == 0:
		b.WriteString("Pods: none\n")

		return
	case v.PodsMatched > len(v.Pods):
		fmt.Fprintf(b, "Pods (first %d of %d, not ready first):\n", len(v.Pods), v.PodsMatched)
	default:
		fmt.Fprintf(b, "Pods (%d, not ready first):\n", v.PodsMatched)
	}

	for _, p := range v.Pods {
		fmt.Fprintf(b, "  %s %s, %s ready, %s", p.Name, p.Phase, p.Ready, plural(int(p.Restarts), "restart"))

		if p.Node != "" {
			fmt.Fprintf(b, ", node %s", p.Node)
		}

		fmt.Fprintf(b, ", started %s", timestamp(p.Started))

		switch {
		case p.Revision != "" && p.ReplicaSet != "":
			fmt.Fprintf(b, ", revision %s (ReplicaSet %s)", p.Revision, p.ReplicaSet)
		case p.Revision != "":
			fmt.Fprintf(b, ", revision %s", p.Revision)
		case p.ReplicaSet != "":
			fmt.Fprintf(b, ", ReplicaSet %s", p.ReplicaSet)
		}

		b.WriteString("\n")
	}
}

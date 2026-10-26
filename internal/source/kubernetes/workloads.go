package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Kinds the tools name.
const (
	kindDeployment  = "Deployment"
	kindStatefulSet = "StatefulSet"
	kindDaemonSet   = "DaemonSet"
	kindReplicaSet  = "ReplicaSet"
	kindPod         = "Pod"
	kindNode        = "Node"
)

// revisionAnnotation holds a Deployment's or ReplicaSet's revision number.
const revisionAnnotation = "deployment.kubernetes.io/revision"

// workloadInput is the input of get_workload.
type workloadInput struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// workloadView is the structured result of get_workload.
type workloadView struct {
	Kind               string          `json:"kind"`
	Namespace          string          `json:"namespace"`
	Name               string          `json:"name"`
	Created            time.Time       `json:"created,omitzero"`
	Generation         int64           `json:"generation"`
	ObservedGeneration int64           `json:"observedGeneration"`
	Revision           string          `json:"revision,omitempty"`
	Paused             bool            `json:"paused,omitempty"`
	Strategy           string          `json:"strategy,omitempty"`
	Replicas           replicaCounts   `json:"replicas"`
	Containers         []containerView `json:"containers"`
	Conditions         []conditionView `json:"conditions,omitempty"`
	Autoscaler         *autoscalerView `json:"autoscaler,omitempty"`
	// AutoscalerError says why autoscalers could not be read, if so.
	AutoscalerError string `json:"autoscalerError,omitempty"`
}

// replicaCounts are the replica numbers of a workload. For a DaemonSet,
// desired is the number of nodes that should run its pod.
type replicaCounts struct {
	Desired     int32 `json:"desired"`
	Ready       int32 `json:"ready"`
	Updated     int32 `json:"updated"`
	Available   int32 `json:"available"`
	Unavailable int32 `json:"unavailable"`
}

// containerView is one container of a pod template.
type containerView struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Init  bool   `json:"init,omitempty"`
}

// conditionView is one status condition.
type conditionView struct {
	Type           string    `json:"type"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	Message        string    `json:"message,omitempty"`
	LastTransition time.Time `json:"lastTransition,omitzero"`
}

// autoscalerView is the horizontal pod autoscaler that scales a workload.
type autoscalerView struct {
	Name       string          `json:"name"`
	Min        int32           `json:"min"`
	Max        int32           `json:"max"`
	Current    int32           `json:"current"`
	Desired    int32           `json:"desired"`
	Conditions []conditionView `json:"conditions,omitempty"`
}

// getWorkload returns the status of one Deployment, StatefulSet or
// DaemonSet, and the autoscaler that scales it.
func (s *Source) getWorkload(ctx context.Context, c kubernetes.Interface, in workloadInput) (source.Result, error) {
	if err := s.allowed(in.Namespace); err != nil {
		return source.Result{}, fmt.Errorf("get workload: %w", err)
	}

	view, err := fetchWorkload(ctx, c, in)
	if err != nil {
		return source.Result{}, fmt.Errorf("get workload: %w", err)
	}

	view.Autoscaler, view.AutoscalerError = findAutoscaler(ctx, c, view.Kind, in.Namespace, in.Name)

	o := object{Kind: view.Kind, Resource: resourceOf(view.Kind), Namespace: in.Namespace, Name: in.Name}
	command := kubectl("describe", strings.ToLower(view.Kind)+"/"+in.Name, "-n", in.Namespace)

	return result(workloadText(view), view, s.links.evidence(source.EvidenceResource, []object{o}, command), false)
}

// fetchWorkload reads the workload of the input's kind.
func fetchWorkload(ctx context.Context, c kubernetes.Interface, in workloadInput) (workloadView, error) {
	apps := c.AppsV1()

	switch in.Kind {
	case kindDeployment:
		d, err := apps.Deployments(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return workloadView{}, apiError("get", resDeployments, in.Namespace, err)
		}

		return deploymentView(d), nil
	case kindStatefulSet:
		st, err := apps.StatefulSets(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return workloadView{}, apiError("get", resStatefulSets, in.Namespace, err)
		}

		return statefulSetView(st), nil
	case kindDaemonSet:
		ds, err := apps.DaemonSets(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return workloadView{}, apiError("get", resDaemonSets, in.Namespace, err)
		}

		return daemonSetView(ds), nil
	default:
		return workloadView{}, &source.ToolError{Message: fmt.Sprintf("kind: %q is not one of %s, %s and %s", in.Kind, kindDeployment, kindStatefulSet, kindDaemonSet)}
	}
}

// deploymentView summarizes a Deployment.
func deploymentView(d *appsv1.Deployment) workloadView {
	v := workloadView{
		Kind:               kindDeployment,
		Namespace:          d.Namespace,
		Name:               d.Name,
		Created:            d.CreationTimestamp.Time,
		Generation:         d.Generation,
		ObservedGeneration: d.Status.ObservedGeneration,
		Revision:           d.Annotations[revisionAnnotation],
		Paused:             d.Spec.Paused,
		Strategy:           string(d.Spec.Strategy.Type),
		Replicas: replicaCounts{
			Desired:     desired(d.Spec.Replicas),
			Ready:       d.Status.ReadyReplicas,
			Updated:     d.Status.UpdatedReplicas,
			Available:   d.Status.AvailableReplicas,
			Unavailable: d.Status.UnavailableReplicas,
		},
		Containers: containers(&d.Spec.Template.Spec),
	}

	for _, c := range d.Status.Conditions {
		v.Conditions = append(v.Conditions, conditionView{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransition: c.LastTransitionTime.Time,
		})
	}

	return v
}

// statefulSetView summarizes a StatefulSet.
func statefulSetView(st *appsv1.StatefulSet) workloadView {
	want := desired(st.Spec.Replicas)

	v := workloadView{
		Kind:               kindStatefulSet,
		Namespace:          st.Namespace,
		Name:               st.Name,
		Created:            st.CreationTimestamp.Time,
		Generation:         st.Generation,
		ObservedGeneration: st.Status.ObservedGeneration,
		Revision:           st.Status.UpdateRevision,
		Strategy:           string(st.Spec.UpdateStrategy.Type),
		Replicas: replicaCounts{
			Desired:     want,
			Ready:       st.Status.ReadyReplicas,
			Updated:     st.Status.UpdatedReplicas,
			Available:   st.Status.AvailableReplicas,
			Unavailable: max(want-st.Status.AvailableReplicas, 0),
		},
		Containers: containers(&st.Spec.Template.Spec),
	}

	for _, c := range st.Status.Conditions {
		v.Conditions = append(v.Conditions, conditionView{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransition: c.LastTransitionTime.Time,
		})
	}

	return v
}

// daemonSetView summarizes a DaemonSet.
func daemonSetView(ds *appsv1.DaemonSet) workloadView {
	v := workloadView{
		Kind:               kindDaemonSet,
		Namespace:          ds.Namespace,
		Name:               ds.Name,
		Created:            ds.CreationTimestamp.Time,
		Generation:         ds.Generation,
		ObservedGeneration: ds.Status.ObservedGeneration,
		Strategy:           string(ds.Spec.UpdateStrategy.Type),
		Replicas: replicaCounts{
			Desired:     ds.Status.DesiredNumberScheduled,
			Ready:       ds.Status.NumberReady,
			Updated:     ds.Status.UpdatedNumberScheduled,
			Available:   ds.Status.NumberAvailable,
			Unavailable: ds.Status.NumberUnavailable,
		},
		Containers: containers(&ds.Spec.Template.Spec),
	}

	for _, c := range ds.Status.Conditions {
		v.Conditions = append(v.Conditions, conditionView{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransition: c.LastTransitionTime.Time,
		})
	}

	return v
}

// findAutoscaler returns the autoscaler in the namespace that scales the
// workload, or nil. When autoscalers cannot be read, it returns why
// instead, since the workload itself was read.
func findAutoscaler(ctx context.Context, c kubernetes.Interface, kind, ns, name string) (*autoscalerView, string) {
	var found *autoscalerView

	err := eachPage(ctx, metav1.ListOptions{}, c.AutoscalingV2().HorizontalPodAutoscalers(ns).List, func(page *autoscalingv2.HorizontalPodAutoscalerList) {
		for i := range page.Items {
			h := &page.Items[i]
			if found == nil && h.Spec.ScaleTargetRef.Kind == kind && h.Spec.ScaleTargetRef.Name == name {
				found = autoscalerOf(h)
			}
		}
	})
	if err != nil {
		return nil, messageOf(apiError("list", resAutoscalers, ns, err))
	}

	return found, ""
}

// autoscalerOf summarizes an autoscaler.
func autoscalerOf(h *autoscalingv2.HorizontalPodAutoscaler) *autoscalerView {
	v := &autoscalerView{
		Name:    h.Name,
		Min:     desired(h.Spec.MinReplicas),
		Max:     h.Spec.MaxReplicas,
		Current: h.Status.CurrentReplicas,
		Desired: h.Status.DesiredReplicas,
	}

	for _, c := range h.Status.Conditions {
		v.Conditions = append(v.Conditions, conditionView{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransition: c.LastTransitionTime.Time,
		})
	}

	return v
}

// workloadText renders the workload for the model.
func workloadText(v workloadView) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s %s/%s, created %s\n", v.Kind, v.Namespace, v.Name, timestamp(v.Created))

	r := v.Replicas
	fmt.Fprintf(&b, "Replicas: %d desired, %d ready, %d updated, %d available, %d unavailable\n", r.Desired, r.Ready, r.Updated, r.Available, r.Unavailable)
	fmt.Fprintf(&b, "Generation: %d, observed %d\n", v.Generation, v.ObservedGeneration)

	if v.Revision != "" {
		fmt.Fprintf(&b, "Revision: %s\n", v.Revision)
	}

	if v.Strategy != "" {
		fmt.Fprintf(&b, "Update strategy: %s\n", v.Strategy)
	}

	if v.Paused {
		b.WriteString("Rollout: paused\n")
	}

	writeContainers(&b, v.Containers)
	writeConditions(&b, "Conditions", v.Conditions)

	switch a := v.Autoscaler; {
	case a != nil:
		fmt.Fprintf(&b, "Autoscaler %s: %d to %d replicas, %d now, %d desired\n", a.Name, a.Min, a.Max, a.Current, a.Desired)
		writeConditions(&b, "Autoscaler conditions", a.Conditions)
	case v.AutoscalerError != "":
		fmt.Fprintf(&b, "Autoscaler: unknown, %s\n", v.AutoscalerError)
	}

	return b.String()
}

// writeContainers writes one line per container and its image.
func writeContainers(b *strings.Builder, list []containerView) {
	b.WriteString("Containers:\n")

	for _, c := range list {
		if c.Init {
			fmt.Fprintf(b, "  %s (init): %s\n", c.Name, c.Image)
		} else {
			fmt.Fprintf(b, "  %s: %s\n", c.Name, c.Image)
		}
	}
}

// writeConditions writes one line per condition under a heading.
func writeConditions(b *strings.Builder, heading string, list []conditionView) {
	if len(list) == 0 {
		return
	}

	fmt.Fprintf(b, "%s:\n", heading)

	for _, c := range list {
		fmt.Fprintf(b, "  %s=%s", c.Type, c.Status)

		if c.Reason != "" {
			fmt.Fprintf(b, " %s", c.Reason)
		}

		if c.Message != "" {
			fmt.Fprintf(b, ": %s", c.Message)
		}

		fmt.Fprintf(b, " (since %s)\n", timestamp(c.LastTransition))
	}
}

// containers lists the init and app containers of a pod spec.
func containers(spec *corev1.PodSpec) []containerView {
	list := make([]containerView, 0, len(spec.InitContainers)+len(spec.Containers))

	for _, c := range spec.InitContainers {
		list = append(list, containerView{Name: c.Name, Image: c.Image, Init: true})
	}

	for _, c := range spec.Containers {
		list = append(list, containerView{Name: c.Name, Image: c.Image})
	}

	return list
}

// desired returns the replica count a spec asks for; nil means one.
func desired(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}

	return *replicas
}

// resourceOf returns the lowercase plural resource name of a kind, for
// console links, for example deployments for Deployment.
func resourceOf(kind string) string {
	r := strings.ToLower(kind)

	switch {
	case r == "":
		return ""
	case r == "endpoints":
		return r
	case strings.HasSuffix(r, "s"), strings.HasSuffix(r, "x"):
		return r + "es"
	case strings.HasSuffix(r, "y"):
		return strings.TrimSuffix(r, "y") + "ies"
	default:
		return r + "s"
	}
}

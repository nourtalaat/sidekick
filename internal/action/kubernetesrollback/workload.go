package kubernetesrollback

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// revisionAnnotation is the revision number the Deployment controller
	// writes on the ReplicaSets of a Deployment (pkg/controller/deployment/util,
	// RevisionAnnotation).
	revisionAnnotation = "deployment.kubernetes.io/revision"
	// changeCauseAnnotation is where kubectl and deploy tools record why a
	// revision was made.
	changeCauseAnnotation = "kubernetes.io/change-cause"
)

// workload is the part of a Deployment, StatefulSet or DaemonSet the action
// uses.
type workload struct {
	kind, namespace, name string
	uid                   types.UID
	selector              *metav1.LabelSelector
	// template is the pod template it has now.
	template    corev1.PodTemplateSpec
	labels      map[string]string
	annotations map[string]string
	managed     []metav1.ManagedFieldsEntry
	// generation and observed are metadata.generation and
	// status.observedGeneration. They differ while its controller has not
	// processed the last change.
	generation, observed int64
	// paused is spec.paused of a Deployment.
	paused bool
	// version is metadata.resourceVersion.
	version string
}

// revision is one earlier or current revision of a workload: a ReplicaSet of
// a Deployment, or a ControllerRevision of a StatefulSet or DaemonSet.
type revision struct {
	name    string
	number  int64
	created time.Time
	// template is the pod template of the revision. For a ReplicaSet the
	// pod-template-hash label is left out, so it compares with the template
	// of the Deployment.
	template corev1.PodTemplateSpec
	// changeCause is the text of the change-cause annotation of a ReplicaSet.
	changeCause string
	// data is the stored data of a ControllerRevision, a strategic merge
	// patch that replaces spec.template.
	data json.RawMessage
}

// String names the workload for an error text.
func (w workload) String() string {
	return fmt.Sprintf("%s %s/%s", w.kind, w.namespace, w.name)
}

// id is the identifier of a revision in a snapshot: its name and its number.
// The name is what finds it again, because Kubernetes gives a revision a new
// number when a rollback goes back to it.
func (r revision) id() string {
	return r.name + ":" + strconv.FormatInt(r.number, 10)
}

// fromDeployment takes what the action uses from a Deployment.
func fromDeployment(d *appsv1.Deployment) workload {
	template := *d.Spec.Template.DeepCopy()
	delete(template.Labels, appsv1.DefaultDeploymentUniqueLabelKey)

	return workload{
		kind: kindDeployment, namespace: d.Namespace, name: d.Name, uid: d.UID, selector: d.Spec.Selector,
		template: template, labels: d.Labels, annotations: d.Annotations, managed: d.ManagedFields,
		generation: d.Generation, observed: d.Status.ObservedGeneration, paused: d.Spec.Paused, version: d.ResourceVersion,
	}
}

// fromStatefulSet takes what the action uses from a StatefulSet.
func fromStatefulSet(s *appsv1.StatefulSet) workload {
	return workload{
		kind: kindStatefulSet, namespace: s.Namespace, name: s.Name, uid: s.UID, selector: s.Spec.Selector,
		template: s.Spec.Template, labels: s.Labels, annotations: s.Annotations, managed: s.ManagedFields,
		generation: s.Generation, observed: s.Status.ObservedGeneration, version: s.ResourceVersion,
	}
}

// fromDaemonSet takes what the action uses from a DaemonSet.
func fromDaemonSet(d *appsv1.DaemonSet) workload {
	return workload{
		kind: kindDaemonSet, namespace: d.Namespace, name: d.Name, uid: d.UID, selector: d.Spec.Selector,
		template: d.Spec.Template, labels: d.Labels, annotations: d.Annotations, managed: d.ManagedFields,
		generation: d.Generation, observed: d.Status.ObservedGeneration, version: d.ResourceVersion,
	}
}

// fromReplicaSet takes what the action uses from a ReplicaSet. A revision
// number that is missing or not a number reads as 0.
func fromReplicaSet(rs *appsv1.ReplicaSet) revision {
	template := *rs.Spec.Template.DeepCopy()
	delete(template.Labels, appsv1.DefaultDeploymentUniqueLabelKey)

	number, _ := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64)

	return revision{
		name: rs.Name, number: number, created: rs.CreationTimestamp.Time, template: template,
		changeCause: rs.Annotations[changeCauseAnnotation],
	}
}

// fromControllerRevision takes what the action uses from a ControllerRevision.
// It fails when the data does not hold a pod template.
func fromControllerRevision(cr *appsv1.ControllerRevision) (revision, error) {
	var stored struct {
		Spec struct {
			Template *corev1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}

	if err := json.Unmarshal(cr.Data.Raw, &stored); err != nil {
		return revision{}, fmt.Errorf("read the stored data: %w", err)
	}

	if stored.Spec.Template == nil {
		return revision{}, errors.New("the stored data has no pod template")
	}

	return revision{
		name: cr.Name, number: cr.Revision, created: cr.CreationTimestamp.Time, template: *stored.Spec.Template, data: cr.Data.Raw,
	}, nil
}

// current returns the revision whose pod template the workload has now. When
// several have it, a Deployment takes the oldest ReplicaSet, as its
// controller does (FindNewReplicaSet), and the others the highest number.
func current(w workload, revs []revision) (revision, bool) {
	var (
		best  revision
		found bool
	)

	for _, r := range revs {
		if !sameTemplate(r.template, w.template) {
			continue
		}

		if !found || preferred(w.kind, r, best) {
			best, found = r, true
		}
	}

	return best, found
}

// preferred reports whether r is a better match than best for the current
// template.
func preferred(kind string, r, best revision) bool {
	if kind == kindDeployment {
		return cmp.Or(r.created.Compare(best.created), cmp.Compare(r.name, best.name)) < 0
	}

	return r.number > best.number
}

// earlier lists the revisions before the running one, newest first. It leaves
// out those that have the pod template of the running revision or of a newer
// one in the list, so each entry is a distinct revision, and those with no
// revision number.
func earlier(running revision, revs []revision) []revision {
	candidates := slices.DeleteFunc(slices.Clone(revs), func(r revision) bool {
		return r.number <= 0 || r.number >= running.number
	})

	slices.SortFunc(candidates, func(a, b revision) int {
		return cmp.Or(cmp.Compare(b.number, a.number), b.created.Compare(a.created), cmp.Compare(a.name, b.name))
	})

	var out []revision

	for _, r := range candidates {
		if sameTemplate(r.template, running.template) || slices.ContainsFunc(out, func(o revision) bool { return sameTemplate(o.template, r.template) }) {
			continue
		}

		out = append(out, r)
	}

	return out
}

// find returns the revision with the given name.
func find(revs []revision, name string) (revision, bool) {
	i := slices.IndexFunc(revs, func(r revision) bool { return r.name == name })
	if i < 0 {
		return revision{}, false
	}

	return revs[i], true
}

// sameTemplate reports whether two pod templates are the same, the way the
// Deployment controller compares them.
func sameTemplate(a, b corev1.PodTemplateSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

package kubernetes

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// changeCauseAnnotation is where kubectl and deploy tools record why a
// revision was made.
const changeCauseAnnotation = "kubernetes.io/change-cause"

// rolloutInput is the input of get_rollout_history.
type rolloutInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Limit     int    `json:"limit"`
}

// rolloutOutput is the structured result of get_rollout_history.
type rolloutOutput struct {
	Namespace       string         `json:"namespace"`
	Name            string         `json:"name"`
	CurrentRevision int64          `json:"currentRevision"`
	Revisions       []revisionView `json:"revisions"`
	// Matched is how many revisions there are, before the limit.
	Matched int `json:"matched"`
}

// revisionView is one ReplicaSet revision of a Deployment.
type revisionView struct {
	Revision    int64           `json:"revision"`
	ReplicaSet  string          `json:"replicaSet"`
	Created     time.Time       `json:"created,omitzero"`
	Replicas    int32           `json:"replicas"`
	Ready       int32           `json:"ready"`
	Current     bool            `json:"current,omitempty"`
	ChangeCause string          `json:"changeCause,omitempty"`
	Containers  []containerView `json:"containers"`
	// Changes says what differs from the revision before, when there is one.
	Changes []string `json:"changes,omitempty"`
}

// getRolloutHistory lists the revisions of a Deployment, newest first.
func (s *Source) getRolloutHistory(ctx context.Context, c kubernetes.Interface, in rolloutInput) (source.Result, error) {
	if err := s.allowed(in.Namespace); err != nil {
		return source.Result{}, fmt.Errorf("get rollout history: %w", err)
	}

	d, err := c.AppsV1().Deployments(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil {
		return source.Result{}, apiError("get", resDeployments, in.Namespace, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("the selector of Deployment %s/%s cannot be read: %s", in.Namespace, in.Name, err)}
	}

	var revisions []revisionView

	err = eachPage(ctx, metav1.ListOptions{LabelSelector: selector.String()}, c.AppsV1().ReplicaSets(in.Namespace).List, func(page *appsv1.ReplicaSetList) {
		for i := range page.Items {
			rs := &page.Items[i]
			if owner := metav1.GetControllerOf(rs); owner != nil && owner.UID == d.UID {
				revisions = append(revisions, revisionOf(rs))
			}
		}
	})
	if err != nil {
		return source.Result{}, apiError("list", resReplicaSets, in.Namespace, err)
	}

	slices.SortFunc(revisions, func(a, b revisionView) int {
		return cmp.Or(cmp.Compare(b.Revision, a.Revision), b.Created.Compare(a.Created))
	})

	out := rolloutOutput{Namespace: in.Namespace, Name: in.Name, CurrentRevision: revisionNumber(d.Annotations), Matched: len(revisions)}

	for i := range revisions {
		revisions[i].Current = revisions[i].Revision == out.CurrentRevision
		if i+1 < len(revisions) {
			revisions[i].Changes = changes(revisions[i+1], revisions[i])
		}
	}

	out.Revisions = revisions[:min(len(revisions), limit(in.Limit, defaultRevisions, maxRevisions))]

	o := object{Kind: kindDeployment, Resource: resourceOf(kindDeployment), Namespace: in.Namespace, Name: in.Name}
	evidence := s.links.evidence(source.EvidenceDeploy, []object{o}, kubectl("rollout", "history", "deployment/"+in.Name, "-n", in.Namespace))

	if len(out.Revisions) == 0 {
		return source.Result{NoData: true, Evidence: evidence}, nil
	}

	return result(rolloutText(out), out, evidence, false)
}

// revisionOf summarizes one ReplicaSet.
func revisionOf(rs *appsv1.ReplicaSet) revisionView {
	return revisionView{
		Revision:    revisionNumber(rs.Annotations),
		ReplicaSet:  rs.Name,
		Created:     rs.CreationTimestamp.Time,
		Replicas:    rs.Status.Replicas,
		Ready:       rs.Status.ReadyReplicas,
		ChangeCause: rs.Annotations[changeCauseAnnotation],
		Containers:  containers(&rs.Spec.Template.Spec),
	}
}

// changes says what changed from the older revision to the newer one: the
// image of each container, and containers added or removed. Every revision
// has a new pod template, so when no image changed, the change was in
// something else, such as the environment or resources.
func changes(older, newer revisionView) []string {
	before := map[string]string{}
	for _, c := range older.Containers {
		before[c.Name] = c.Image
	}

	var out []string

	for _, c := range newer.Containers {
		image, ok := before[c.Name]

		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s: added with %s", c.Name, c.Image))
		case image != c.Image:
			out = append(out, fmt.Sprintf("%s: %s -> %s", c.Name, image, c.Image))
		}

		delete(before, c.Name)
	}

	removed := make([]string, 0, len(before))
	for name := range before {
		removed = append(removed, name)
	}

	slices.Sort(removed)

	for _, name := range removed {
		out = append(out, name+": removed")
	}

	if len(out) == 0 {
		out = append(out, "same images; another part of the pod template changed")
	}

	return out
}

// rolloutText renders the revisions, newest first.
func rolloutText(out rolloutOutput) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Rollout history of Deployment %s/%s, current revision %d", out.Namespace, out.Name, out.CurrentRevision)

	if out.Matched > len(out.Revisions) {
		fmt.Fprintf(&b, ", newest %d of %d revisions", len(out.Revisions), out.Matched)
	}

	b.WriteString(":\n")

	for _, r := range out.Revisions {
		fmt.Fprintf(&b, "Revision %d, ReplicaSet %s, created %s, %d/%d ready", r.Revision, r.ReplicaSet, timestamp(r.Created), r.Ready, r.Replicas)

		if r.Current {
			b.WriteString(", current")
		}

		b.WriteString("\n")

		if r.ChangeCause != "" {
			fmt.Fprintf(&b, "  Change cause: %s\n", r.ChangeCause)
		}

		images := make([]string, 0, len(r.Containers))
		for _, c := range r.Containers {
			images = append(images, c.Name+"="+c.Image)
		}

		fmt.Fprintf(&b, "  Images: %s\n", strings.Join(images, ", "))

		for _, change := range r.Changes {
			fmt.Fprintf(&b, "  Changed: %s\n", change)
		}
	}

	return b.String()
}

// revisionNumber reads the revision annotation; zero when it is missing.
func revisionNumber(annotations map[string]string) int64 {
	n, err := strconv.ParseInt(annotations[revisionAnnotation], 10, 64)
	if err != nil {
		return 0
	}

	return n
}

package kubernetes

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// oomKilled is the termination reason of a container the kernel killed for
// using too much memory.
const oomKilled = "OOMKilled"

// startingReasons are waiting reasons of a container that is starting
// normally, which are not problems on their own.
var startingReasons = []string{"ContainerCreating", "PodInitializing"}

// podProblemsInput is the input of list_pod_problems.
type podProblemsInput struct {
	Namespace     string `json:"namespace"`
	LabelSelector string `json:"labelSelector"`
	Limit         int    `json:"limit"`
}

// podProblemsOutput is the structured result of list_pod_problems.
type podProblemsOutput struct {
	Pods []podProblem `json:"pods"`
	// Matched is how many pods have problems, before the limit.
	Matched int `json:"matched"`
	// Checked is how many pods were looked at.
	Checked int `json:"checked"`
}

// podProblem is one pod with problems.
type podProblem struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Phase     string    `json:"phase"`
	Node      string    `json:"node,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	Started   time.Time `json:"started,omitzero"`
	Restarts  int32     `json:"restarts"`
	// Problems says what is wrong, one problem per entry.
	Problems []string `json:"problems"`
}

// listPodProblems lists the pods that have problems in the input's scope.
func (s *Source) listPodProblems(ctx context.Context, c kubernetes.Interface, in podProblemsInput) (source.Result, error) {
	namespaces, err := s.scope(in.Namespace)
	if err != nil {
		return source.Result{}, fmt.Errorf("list pod problems: %w", err)
	}

	if _, err := labels.Parse(in.LabelSelector); err != nil {
		return source.Result{}, &source.ToolError{Message: "labelSelector: " + err.Error()}
	}

	var out podProblemsOutput

	for _, ns := range namespaces {
		err := eachPage(ctx, metav1.ListOptions{LabelSelector: in.LabelSelector}, c.CoreV1().Pods(ns).List, func(page *corev1.PodList) {
			out.Checked += len(page.Items)

			for i := range page.Items {
				if p, ok := problemsOf(&page.Items[i]); ok {
					out.Pods = append(out.Pods, p)
				}
			}
		})
		if err != nil {
			return source.Result{}, apiError("list", resPods, ns, err)
		}
	}

	slices.SortFunc(out.Pods, func(a, b podProblem) int {
		return cmp.Or(cmp.Compare(b.Restarts, a.Restarts), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})

	out.Matched = len(out.Pods)
	out.Pods = out.Pods[:min(len(out.Pods), limit(in.Limit, defaultPods, maxPods))]

	objects := make([]object, 0, len(out.Pods))
	for _, p := range out.Pods {
		objects = append(objects, object{Kind: kindPod, Resource: resourceOf(kindPod), Namespace: p.Namespace, Name: p.Name})
	}

	args := append([]string{"get", "pods"}, namespaceArgs(commandNamespace(namespaces))...)
	if in.LabelSelector != "" {
		args = append(args, "-l", in.LabelSelector)
	}

	evidence := s.links.evidence(source.EvidenceResource, objects, kubectl(args...))

	if len(out.Pods) == 0 {
		text := fmt.Sprintf("No pod has a problem in %s (%s checked).\n", where(namespaces), plural(out.Checked, "pod"))

		return source.Result{Text: text, NoData: true, Evidence: evidence}, nil
	}

	return result(podProblemsText(namespaces, out), out, evidence, false)
}

// problemsOf returns the pod's problems, and whether it has any.
func problemsOf(pod *corev1.Pod) (podProblem, bool) {
	p := podProblem{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Phase:     string(pod.Status.Phase),
		Node:      pod.Spec.NodeName,
	}

	if pod.Status.StartTime != nil {
		p.Started = pod.Status.StartTime.Time
	}

	if owner := metav1.GetControllerOf(pod); owner != nil {
		p.Owner = owner.Kind + "/" + owner.Name
	}

	switch pod.Status.Phase {
	case corev1.PodPending:
		p.Problems = append(p.Problems, pendingProblems(pod)...)
	case corev1.PodFailed:
		p.Problems = append(p.Problems, joinNonEmpty(": ", "Failed", pod.Status.Reason, pod.Status.Message))
	case corev1.PodUnknown:
		p.Problems = append(p.Problems, joinNonEmpty(": ", "Unknown: the node does not report this pod", pod.Status.Message))
	case corev1.PodSucceeded:
		return podProblem{}, false
	}

	for _, cs := range pod.Status.InitContainerStatuses {
		p.Restarts += cs.RestartCount
		p.Problems = append(p.Problems, containerProblems(cs, true, pod.Status.Phase)...)
	}

	for _, cs := range pod.Status.ContainerStatuses {
		p.Restarts += cs.RestartCount
		p.Problems = append(p.Problems, containerProblems(cs, false, pod.Status.Phase)...)
	}

	return p, len(p.Problems) > 0
}

// pendingProblems says why a pending pod has not started.
func pendingProblems(pod *corev1.Pod) []string {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return []string{joinNonEmpty(": ", "Pending, not scheduled", c.Reason, c.Message)}
		}
	}

	return []string{"Pending"}
}

// containerProblems returns what is wrong with one container: restarts, a
// waiting reason other than a normal start, a last run that was OOM killed
// or failed, or a running container that is not ready.
func containerProblems(cs corev1.ContainerStatus, init bool, phase corev1.PodPhase) []string {
	name := cs.Name
	if init {
		name += " (init)"
	}

	var problems []string

	if w := cs.State.Waiting; w != nil && !slices.Contains(startingReasons, w.Reason) {
		problems = append(problems, joinNonEmpty(": ", name+": waiting, "+cmp.Or(w.Reason, "no reason given"), w.Message))
	}

	if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
		problems = append(problems, fmt.Sprintf("%s: terminated with exit code %d (%s) at %s", name, t.ExitCode, cmp.Or(t.Reason, "no reason given"), timestamp(t.FinishedAt.Time)))
	}

	if t := cs.LastTerminationState.Terminated; t != nil && (t.Reason == oomKilled || t.ExitCode != 0) {
		problems = append(problems, fmt.Sprintf("%s: last run ended with exit code %d (%s) at %s", name, t.ExitCode, cmp.Or(t.Reason, "no reason given"), timestamp(t.FinishedAt.Time)))
	}

	if cs.RestartCount > 0 {
		problems = append(problems, fmt.Sprintf("%s: %s", name, plural(int(cs.RestartCount), "restart")))
	}

	if !init && phase == corev1.PodRunning && cs.State.Running != nil && !cs.Ready {
		problems = append(problems, name+": running but not ready")
	}

	return problems
}

// podProblemsText renders the pods and their problems.
func podProblemsText(namespaces []string, out podProblemsOutput) string {
	var b strings.Builder

	if out.Matched > len(out.Pods) {
		fmt.Fprintf(&b, "%d of %s with problems in %s (%s checked), most restarts first:\n",
			len(out.Pods), plural(out.Matched, "pod"), where(namespaces), plural(out.Checked, "pod"))
	} else {
		fmt.Fprintf(&b, "%s with problems in %s (%s checked), most restarts first:\n", plural(out.Matched, "pod"), where(namespaces), plural(out.Checked, "pod"))
	}

	for _, p := range out.Pods {
		fmt.Fprintf(&b, "%s/%s phase %s", p.Namespace, p.Name, p.Phase)

		if p.Node != "" {
			fmt.Fprintf(&b, " on node %s", p.Node)
		}

		if p.Owner != "" {
			fmt.Fprintf(&b, ", owned by %s", p.Owner)
		}

		fmt.Fprintf(&b, ", started %s\n", timestamp(p.Started))

		for _, problem := range p.Problems {
			fmt.Fprintf(&b, "  %s\n", problem)
		}
	}

	return b.String()
}

// joinNonEmpty joins the parts that are not empty.
func joinNonEmpty(sep string, parts ...string) string {
	kept := slices.DeleteFunc(slices.Clone(parts), func(p string) bool { return strings.TrimSpace(p) == "" })

	return strings.Join(kept, sep)
}

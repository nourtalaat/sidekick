package kubernetesrollback

import (
	"fmt"
)

// The tools whose controllers would undo a live rollback.
const (
	toolArgoCD = "Argo CD"
	toolFlux   = "Flux"
)

// marker is a label or an annotation that says a tool manages a workload.
type marker struct {
	key  string
	tool string
}

// markers are the keys that show a workload is managed by Argo CD or Flux.
// They are checked as labels and as annotations. A workload that has only
// the labels of Helm itself is not managed in this sense.
//
// Argo CD tracks by the annotation argocd.argoproj.io/tracking-id by default
// (docs/user-guide/resource_tracking.md, tag v3.5.7), and by a label whose key
// is set in its config, with argocd.argoproj.io/instance as the recommended
// value. The default label key
// app.kubernetes.io/instance is also used by other tools, so it is not a sign.
// Flux's kustomize-controller and helm-controller put their name labels on
// what they apply.
var markers = []marker{
	{key: "argocd.argoproj.io/tracking-id", tool: toolArgoCD},
	{key: "argocd.argoproj.io/instance", tool: toolArgoCD},
	{key: "kustomize.toolkit.fluxcd.io/name", tool: toolFlux},
	{key: "helm.toolkit.fluxcd.io/name", tool: toolFlux},
}

// managedBy returns the tool that manages the workload and what shows it,
// such as "the annotation argocd.argoproj.io/tracking-id", or "" when none does.
func (w workload) managedBy() (tool, evidence string) {
	for _, m := range markers {
		if _, ok := w.annotations[m.key]; ok {
			return m.tool, "the annotation " + m.key
		}

		if _, ok := w.labels[m.key]; ok {
			return m.tool, "the label " + m.key
		}
	}

	return "", ""
}

// refusal returns why the workload must not be rolled back, or "" when
// nothing stops it. It does not check that the controller has caught up,
// which only matters when proposing.
func (w workload) refusal() string {
	if tool, evidence := w.managedBy(); tool != "" {
		return fmt.Sprintf("%s is managed by %s (it has %s), and %s would undo a live rollback at its next %s. %s",
			w, tool, evidence, tool, syncWord(tool), instead(tool))
	}

	if w.paused {
		return fmt.Sprintf("%s is paused, so a rollback would not roll out until someone resumes it. "+
			"Do not propose a rollback; say in the report that it is paused.", w)
	}

	return ""
}

// syncWord is what the tool calls a run of its controller.
func syncWord(tool string) string {
	if tool == toolArgoCD {
		return "sync"
	}

	return "reconcile"
}

// instead tells the model what to propose for a workload the tool manages.
func instead(tool string) string {
	if tool == toolArgoCD {
		return "Propose argocd.rollback if it is offered, or a pull request that reverts the change in Git."
	}

	return "Propose a pull request that reverts the change in Git."
}

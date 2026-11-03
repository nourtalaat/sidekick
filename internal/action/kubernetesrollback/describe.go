package kubernetesrollback

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	kindReplicaSet         = "ReplicaSet revision"
	kindControllerRevision = "ControllerRevision"
	// unrecordedID is the ID of a pod template that no revision has yet.
	unrecordedID = "unrecorded"
	// maxCauseRunes is the most of a change cause that the notes carry.
	maxCauseRunes = 200
	// specField and templateField are the keys of the managed fields set
	// that lead to spec.template.
	specField     = "f:spec"
	templateField = "f:template"
)

// describe returns the revision as the proposal shows it. The running
// revision is the one the workload has now, whose managed fields say who
// changed its template last. No record says that of an earlier revision, so
// DeployedBy of the others stays empty and the screens show "unknown".
func (w workload) describe(r revision, running bool) action.Revision {
	out := action.Revision{Kind: kindReplicaSet, ID: r.id(), Version: images(r.template.Spec.Containers), Notes: notes(r)}

	if w.kind != kindDeployment {
		out.Kind = kindControllerRevision
	}

	if !r.created.IsZero() {
		created := r.created.UTC()
		out.DeployedAt = &created
	}

	if running {
		out.DeployedBy = w.deployer()
	}

	return out
}

// describeUnrecorded describes the pod template of a workload that no
// revision has, for the live definition in a conflict.
func (w workload) describeUnrecorded() action.Revision {
	kind := kindReplicaSet
	if w.kind != kindDeployment {
		kind = kindControllerRevision
	}

	return action.Revision{Kind: kind, ID: unrecordedID, Version: images(w.template.Spec.Containers)}
}

// images lists the images of containers, as "name=image" for each, or just
// the image when there is one container.
func images(containers []corev1.Container) string {
	if len(containers) == 1 {
		return containers[0].Image
	}

	parts := make([]string, len(containers))
	for i, c := range containers {
		parts[i] = c.Name + "=" + c.Image
	}

	return strings.Join(parts, ", ")
}

// notes writes what else a person may want to know about a revision: its
// init containers and the cause that was recorded for it.
func notes(r revision) string {
	var parts []string

	if init := r.template.Spec.InitContainers; len(init) > 0 {
		var list []string

		for _, c := range init {
			list = append(list, c.Name+"="+c.Image)
		}

		parts = append(parts, "init containers: "+strings.Join(list, ", "))
	}

	if r.changeCause != "" {
		parts = append(parts, "change cause: "+truncate(r.changeCause, maxCauseRunes))
	}

	return strings.Join(parts, "; ")
}

// truncate cuts s to at most n runes.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}

	return string([]rune(s)[:n]) + "..."
}

// deployer names who changed the pod template last, as far as the managed
// fields of the workload show: the manager with the newest entry that owns
// a field of spec.template, and the time of that entry. It returns "" when
// no entry does. Entries of a subresource are about the status.
func (w workload) deployer() string {
	var (
		manager string
		at      time.Time
	)

	for _, entry := range w.managed {
		if entry.Subresource != "" || entry.Time == nil || entry.FieldsV1 == nil || !ownsTemplate(entry.FieldsV1.Raw) {
			continue
		}

		if entry.Time.Time.After(at) {
			manager, at = entry.Manager, entry.Time.Time
		}
	}

	if manager == "" {
		return ""
	}

	return fmt.Sprintf("%s at %s", manager, at.UTC().Format(time.RFC3339))
}

// ownsTemplate reports whether a managed fields set, in the FieldsV1 format,
// holds a field of spec.template.
func ownsTemplate(raw []byte) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return false
	}

	var spec map[string]json.RawMessage
	if json.Unmarshal(top[specField], &spec) != nil {
		return false
	}

	_, ok := spec[templateField]

	return ok
}

//go:generate mockgen -source output.go -destination mocks_output.go -package output -write_package_comment=false

// Package output publishes investigation progress and results.
package output

import (
	"context"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/report"
)

// Sink is one configured output, such as a Slack thread or a webhook callback.
type Sink interface {
	// Accepts reports whether the sink wants updates of this phase. A webhook
	// callback, for example, wants only PhaseFinal.
	Accepts(phase Phase) bool
	// Publish sends or updates the sink's message. When update.Previous is set
	// the sink edits that message in place instead of sending a new one (OUT-1).
	// The returned Ref is stored and passed back as Previous next time.
	Publish(ctx context.Context, update Update) (Ref, error)
}

// Phase is the stage of the investigation an update describes.
type Phase string

// Phases.
const (
	PhaseQueued    Phase = "queued"
	PhaseRunning   Phase = "running"
	PhaseFinal     Phase = "final"
	PhaseFollowUp  Phase = "follow_up"
	PhaseProposal  Phase = "action_proposal"
	PhaseResolved  Phase = "resolved"
	PhaseCancelled Phase = "cancelled"
	// PhaseWatcher reports a watcher state change; Update.Watcher says which one.
	PhaseWatcher Phase = "watcher"
)

// Progress is one line of live progress, such as "Checked recent deploys".
type Progress struct {
	At   time.Time
	Text string
}

// Update is everything a sink needs to render one state of an investigation.
type Update struct {
	InvestigationID string
	// URL is the investigation page in the web UI; empty when the UI is disabled.
	URL      string
	Event    event.Event
	Events   int
	Phase    Phase
	Progress []Progress
	Report   *report.Report
	// Answer is the reply to a follow-up question.
	Answer    string
	Proposals []action.Proposal
	// Watchers is the current state of every watcher on the group, for live display (WT-6).
	Watchers []WatcherState
	// Watcher is the watcher whose state changed, set only with PhaseWatcher.
	Watcher  *WatcherState
	Previous *Ref
	// Seq increases with every update, so a sink can ignore an older one that arrives late.
	Seq int64
}

// Ref locates what a sink published, for example a Slack channel and message timestamp.
type Ref struct {
	Sink     string            `json:"sink"`
	External string            `json:"external"`
	Data     map[string]string `json:"data,omitempty"`
}

// WatcherStatus is the state of one watcher (WT-4).
type WatcherStatus string

// Watcher statuses. Every status but WatcherRunning is final.
const (
	WatcherRunning   WatcherStatus = "running"
	WatcherRecovered WatcherStatus = "recovered"
	WatcherTimedOut  WatcherStatus = "timed_out"
	WatcherStopped   WatcherStatus = "stopped"
)

// WatcherState is what a sink shows about one watcher.
type WatcherState struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	CheckType       string        `json:"checkType"`
	Status          WatcherStatus `json:"status"`
	PassesInARow    int           `json:"passesInARow"`
	PassesToRecover int           `json:"passesToRecover"`
	Checks          int           `json:"checks"`
	LastCheckAt     time.Time     `json:"lastCheckAt,omitzero"`
	// LastResult is a short, redacted summary such as "HTTP 503".
	LastResult string    `json:"lastResult,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	EndedAt    time.Time `json:"endedAt,omitzero"`
	// StopReason is set for WatcherStopped: group_resolved or responder.
	StopReason string `json:"stopReason,omitempty"`
}

// EventName is the webhook callback event for a state change (WT-6), for
// example "watcher.recovered". A running watcher reports "watcher.started".
func (w WatcherState) EventName() string {
	if w.Status == WatcherRunning {
		return "watcher.started"
	}

	return "watcher." + string(w.Status)
}

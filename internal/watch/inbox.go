package watch

import "github.com/nourtalaat/sidekick/internal/output"

// InboxKind is the investigation_inbox kind of a watcher's state change,
// and of a route or strategy watcher that did not start.
const InboxKind = "watcher"

// InboxItem is the body of an inbox item of kind InboxKind.
type InboxItem struct {
	// State is the watcher's state after the change. For a watcher that
	// did not start it holds only the name and the check type.
	State output.WatcherState `json:"state"`
	// NotStarted says why a route or strategy watcher did not start.
	NotStarted string `json:"notStarted,omitempty"`
}

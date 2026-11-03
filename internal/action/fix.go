package action

// FixKind says how an action fixes a noisy monitor.
type FixKind string

// The kinds of monitor fix. The same two words are the values the
// action_proposals.fix_kind column allows.
const (
	// FixAPI means the action changes a monitor or an alert rule through
	// its tool's API.
	FixAPI FixKind = "api"
	// FixPullRequest means the action opens a pull request or merge request
	// that changes a monitor definition.
	FixPullRequest FixKind = "pull_request"
)

// MonitorFixer is implemented by an action whose purpose is to stop a false
// positive from firing again. An action that does not implement it fixes
// nothing. Mutes, rollbacks, flags, tickets, Slack messages and code
// changes never implement it.
type MonitorFixer interface {
	FixKind() FixKind
}

//go:generate mockgen -source action.go -destination mocks_action.go -package action -write_package_comment=false

// Package action defines the changes Sidekick may make, and the rules for when.
package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
)

// Action is one configured, allowlisted action instance, for example
// "monitor-pr" of type github.pull_request. Its limits are decoded from
// config when the instance is built, so Validate needs only the parameters.
type Action interface {
	// Spec describes the action to the model.
	Spec() Spec
	// Validate checks the parameters against the schema and the configured limits (ACT-2).
	// It runs when the model proposes the action and again right before Execute.
	Validate(ctx context.Context, params json.RawMessage) error
	// Execute performs the action. It must be safe to repeat with the same
	// Invocation.IdempotencyKey, because a crash can cause a retry.
	Execute(ctx context.Context, inv Invocation) (Result, error)
}

// Snapshotter is implemented by actions that change a definition in place,
// such as a monitor or an alert rule (ACT-7, ACT-9). Snapshot reads the
// current definition and computes the new one without changing anything.
//
// Sidekick calls it when the model proposes, after Validate, and stores the
// result with the proposal, so an approver reads the difference between
// Before and After. Snapshot refuses a change it must not make, such as a
// field outside limits.fields, with an error wrapping ErrLimit; the model
// reads the error and may propose again. After is JSON null for a change
// that removes the definition.
type Snapshotter interface {
	Snapshot(ctx context.Context, params json.RawMessage) (Snapshot, error)
}

// Undoer is implemented by actions that can undo what Execute did. Undo
// fails with an error wrapping ErrConflict when the target no longer matches
// what the action wrote, unless force is set by an approver who has seen the
// difference. An action that changes a definition in place restores only the
// fields it changed, taken from snap.Before, and leaves every other field as
// it is now. An Undoer without a snapshot gets the zero Snapshot. Undo must
// be safe to repeat: when the target is restored already it succeeds.
//
// An action that returns a *ConflictError gives the approver the live
// definition to compare with.
type Undoer interface {
	Undo(ctx context.Context, inv Invocation, snap Snapshot, force bool) (Result, error)
}

// Snapshot holds a target's definition before and after a change.
type Snapshot struct {
	// Target identifies the definition, for example "datadog:monitor/123".
	Target string          `json:"target"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
	// Version is the provider's version or modification time of Before, used
	// to detect a change made by someone else between proposal and execution.
	Version string `json:"version,omitempty"`
}

// ErrPermanent is wrapped by an Execute error that repeating the call cannot
// fix, such as a target that no longer exists. Sidekick then fails the
// proposal at once instead of trying again. ErrConflict and ErrLimit count
// as permanent too. Any other Execute error is tried again with the same
// idempotency key.
var ErrPermanent = errors.New("permanent failure")

// ErrConflict is returned when a definition changed since its snapshot was taken.
var ErrConflict = errors.New("definition changed since the snapshot")

// ConflictError is an ErrConflict that also carries the live definition, in
// the form of Snapshot.Before and After, for Undo to show what a forced
// restore would overwrite.
type ConflictError struct {
	Live json.RawMessage
}

// Error implements error.
func (e *ConflictError) Error() string {
	return ErrConflict.Error()
}

// Unwrap makes the error match ErrConflict.
func (e *ConflictError) Unwrap() error {
	return ErrConflict
}

// Spec is what the model sees when it may propose this action.
type Spec struct {
	Name        string
	Description string
	// ParamsSchema is a JSON Schema object.
	ParamsSchema json.RawMessage
}

// Invocation is an approved request to run an action.
type Invocation struct {
	ProposalID      string
	InvestigationID string
	Params          json.RawMessage
	IdempotencyKey  string
	// ApprovedBy is a user ID, or "policy" in read + write mode.
	ApprovedBy string
	// Snapshot is the snapshot stored with the proposal. Nil for an action
	// that is not a Snapshotter. Execute writes Snapshot.After, never a
	// definition computed again from Params, so what runs is what the
	// approver read.
	Snapshot *Snapshot
}

// Link points at what the action created or changed.
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Result is what an action did.
type Result struct {
	Summary string `json:"summary"`
	Links   []Link `json:"links"`
}

// Mode is a route's action mode (ACT-1).
type Mode string

// Action modes. ReadOnly is the default.
const (
	ReadOnly  Mode = "read-only"
	Approved  Mode = "approved"
	ReadWrite Mode = "read-write"
)

// ErrLimit is wrapped by every limit violation, so callers can tell it from other errors.
var ErrLimit = errors.New("outside configured limits")

// RepoLimits restricts actions that change files in a repository.
type RepoLimits struct {
	Repos           []string `yaml:"repos"`
	Paths           []string `yaml:"paths"`
	MaxFiles        int      `yaml:"maxFiles"`
	MaxChangedLines int      `yaml:"maxChangedLines"`
}

// FileChange is one file an action wants to write.
type FileChange struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	ChangedLines int    `json:"-"`
}

// Check returns an error wrapping ErrLimit when the change is not allowed.
func (l RepoLimits) Check(repo string, files []FileChange) error {
	if !slices.Contains(l.Repos, repo) {
		return fmt.Errorf("repo %q: %w", repo, ErrLimit)
	}

	if l.MaxFiles > 0 && len(files) > l.MaxFiles {
		return fmt.Errorf("%d files, limit %d: %w", len(files), l.MaxFiles, ErrLimit)
	}

	lines := 0
	for _, f := range files {
		ok, err := matchAny(l.Paths, f.Path)
		if err != nil {
			return fmt.Errorf("match path %q: %w", f.Path, err)
		}

		if !ok {
			return fmt.Errorf("path %q: %w", f.Path, ErrLimit)
		}

		lines += f.ChangedLines
	}

	if l.MaxChangedLines > 0 && lines > l.MaxChangedLines {
		return fmt.Errorf("%d changed lines, limit %d: %w", lines, l.MaxChangedLines, ErrLimit)
	}

	return nil
}

// ErrUntrustedWrite is returned when read + write meets an untrusted sender (ACT-6).
var ErrUntrustedWrite = errors.New("read-write mode needs a trusted sender")

// EffectiveMode returns the mode an investigation runs with. A read-write route
// that receives an untrusted event drops to Approved unless the route has the
// explicit override. Config validation also refuses read-write routes whose
// match can select untrusted intakes, so this is the second line of defense.
func EffectiveMode(route Mode, trust event.Trust, allowUntrustedWrite bool) Mode {
	if route == ReadWrite && trust != event.Trusted && !allowUntrustedWrite {
		return Approved
	}

	if route == "" {
		return ReadOnly
	}

	return route
}

// matchAny reports whether name matches one of the patterns. A pattern ending
// in "/**" matches any depth below its prefix.
func matchAny(patterns []string, name string) (bool, error) {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "/**"); ok {
			if strings.HasPrefix(name, prefix+"/") {
				return true, nil
			}

			continue
		}

		ok, err := path.Match(p, name)
		if err != nil {
			return false, fmt.Errorf("pattern %q: %w", p, err)
		}

		if ok {
			return true, nil
		}
	}

	return false, nil
}

//go:generate mockgen -source alert.go -destination mocks_alert.go -package watch -write_package_comment=false

package watch

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Require says how many of a group's alerts must recover for an alert
// check to pass.
type Require string

// Require values.
const (
	// RequireAll: every alert of the group has recovered. It is the
	// default.
	RequireAll Require = "all"
	// RequireAny: at least one has.
	RequireAny Require = "any"
)

// AlertState is whether one alert of a group has recovered.
type AlertState struct {
	// Fingerprint identifies the alert within the group.
	Fingerprint string
	// Resolved is set once the sender's recovery event for the alert has
	// arrived.
	Resolved bool
}

// AlertStates reads the alerts of an event group, for alert checks.
type AlertStates interface {
	// AlertStates returns one state per distinct alert that fired in the
	// group.
	AlertStates(ctx context.Context, groupID uuid.UUID) ([]AlertState, error)
}

// AlertCheck passes when the sender's recovery events have arrived for the
// group's alerts. It reads the stored alert states and calls nothing.
type AlertCheck struct {
	// Require is all (the default) or any.
	Require Require `yaml:"require" json:"require,omitempty"`
}

// Type returns TypeAlert.
func (AlertCheck) Type() Type {
	return TypeAlert
}

// Validate checks Require.
func (a AlertCheck) Validate() error {
	if a.Require != "" && !slices.Contains([]Require{RequireAll, RequireAny}, a.Require) {
		return fmt.Errorf("require: expected all or any, got %q", a.Require)
	}

	return nil
}

// run reads the group's alerts and counts the recovered ones.
func (a AlertCheck) run(ctx context.Context, c *Checker, target Target) Outcome {
	if c.opts.Alerts == nil {
		return errorOutcome("alert checks are not set up")
	}

	states, err := c.opts.Alerts.AlertStates(ctx, target.GroupID)
	if err != nil {
		return errorOutcome("cannot read the alerts of the group")
	}

	if len(states) == 0 {
		return Outcome{Result: Fail, Detail: "the group has no alerts"}
	}

	resolved := 0

	for _, s := range states {
		if s.Resolved {
			resolved++
		}
	}

	detail := fmt.Sprintf("%d of %d alerts recovered", resolved, len(states))

	pass := resolved == len(states)
	if a.Require == RequireAny {
		pass = resolved > 0
	}

	if pass {
		return Outcome{Result: Pass, Detail: detail}
	}

	return Outcome{Result: Fail, Detail: detail}
}

// Package roles names the Sidekick roles and how a Slack user's role is found.
package roles

import "context"

// Role is a Sidekick role. A higher role includes every lower one.
type Role int

const (
	Viewer Role = iota + 1
	Responder
	Approver
	Admin
)

// Lookup finds the role of a Slack user.
type Lookup interface {
	Role(ctx context.Context, teamID, userID string) (Role, error)
}

// Has reports whether r includes need.
func (r Role) Has(need Role) bool {
	return r >= need
}

package slackapp

import (
	"context"

	"github.com/nourtalaat/sidekick/internal/roles"
)

// EveryoneResponds is the role lookup until roles can be set for Slack
// users: every Slack user who can see Sidekick's message is a responder,
// as an unmatched user always is, so anyone can rate a verdict or stop a
// run and nobody can approve.
type EveryoneResponds struct{}

var _ roles.Lookup = EveryoneResponds{}

// Role returns roles.Responder for every user.
func (EveryoneResponds) Role(context.Context, string, string) (roles.Role, error) {
	return roles.Responder, nil
}

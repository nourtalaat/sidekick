//go:generate mockgen -source tracker.go -destination mocks_tracker.go -package ticket -write_package_comment=false

package ticket

import (
	"context"
	"errors"
)

// ErrNotFound is returned by a Tracker for a ticket or comment that does
// not exist, or that the credentials cannot see.
var ErrNotFound = errors.New("not found")

// Tracker is one ticket system, such as a Linear workspace or a Jira site.
// Its methods never return text from the ticket system in errors, only
// statuses and codes.
type Tracker interface {
	// KeyFromURL returns the key of the ticket rawURL links to, such as
	// "OPS-123", and false when rawURL is not a ticket link of this
	// workspace or site.
	KeyFromURL(rawURL string) (string, bool)
	// Issue looks up the ticket with key. It returns ErrNotFound when there
	// is none.
	Issue(ctx context.Context, key string) (Issue, error)
	// CreateComment adds c to issue and returns the comment's ID. It must
	// be safe to call again with the same c.IdempotencyKey after a call
	// whose outcome is unknown, for example one that timed out: the second
	// call finds the comment the first one made instead of adding another.
	CreateComment(ctx context.Context, issue Issue, c Comment) (string, error)
	// UpdateComment replaces the comment with c. It returns ErrNotFound when
	// the comment was deleted.
	UpdateComment(ctx context.Context, issue Issue, commentID string, c Comment) error
}

// Issue is a ticket found by Tracker.Issue.
type Issue struct {
	// ID is the ticket system's own ID, which stays the same when the
	// ticket moves to another team or project.
	ID string
	// Key is the ticket's key now, such as "OPS-123".
	Key string
	// Scope is the key of the team (Linear) or project (Jira) the ticket
	// is in now, such as "OPS".
	Scope string
}

// Comment is what a ticket output posts.
type Comment struct {
	// IdempotencyKey is the same for every attempt to post one comment, so
	// a Tracker can tell that an earlier attempt went through.
	IdempotencyKey string
	// Doc is the content, which the Tracker renders in its own markup.
	Doc Doc
}

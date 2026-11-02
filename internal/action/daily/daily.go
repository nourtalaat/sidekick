//go:generate mockgen -source daily.go -destination mocks_daily.go -package daily -write_package_comment=false

// Package daily enforces the daily limit of an action that creates things,
// such as tickets. The day is the one of budgets.daily.timeZone.
package daily

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

var (
	errNoDB      = errors.New("no database to count the uses of the action in")
	errBadID     = errors.New("the proposal ID is not a UUID")
	errBadMax    = errors.New("maxPerDay must be at least 1")
	errDailyFull = errors.New("daily limit reached")
)

// Limiter checks the daily limit of an action. Counter implements it;
// actions take a Limiter so their tests can stand one in.
type Limiter interface {
	// Check returns an error wrapping action.ErrLimit when the proposal may
	// not run because maxPerDay uses of the action are taken today.
	Check(ctx context.Context, actionName, proposalID string, maxPerDay int) error
}

var _ Limiter = Counter{}

// Counter counts the uses of an action against its daily limit.
type Counter struct {
	// DB is Sidekick's database.
	DB db.DBTX
	// Location is the time zone whose midnight starts a day. Nil means UTC.
	Location *time.Location
	// Now returns the time. Nil means time.Now.
	Now func() time.Time
}

// Check returns an error wrapping action.ErrLimit when the proposal
// proposalID of the action actionName may not run because maxPerDay uses
// are taken today. A use is taken by a proposal of the action that
// succeeded today, or that was approved before this one and is not
// finished yet.
//
// Without a lock the count is still safe: a proposal counts the ones
// approved before it and never the ones approved after it, so of two runs
// at the same time the earlier one passes and the later one sees it. The
// count errs on the safe side: a proposal approved earlier that fails
// later keeps its place for the runs that started before it ended, and
// the person can propose again.
func (c Counter) Check(ctx context.Context, actionName, proposalID string, maxPerDay int) error {
	if maxPerDay < 1 {
		return fmt.Errorf("%w: %w", action.ErrPermanent, errBadMax)
	}

	if c.DB == nil {
		return fmt.Errorf("%w: %w", action.ErrPermanent, errNoDB)
	}

	id, err := uuid.Parse(proposalID)
	if err != nil {
		return fmt.Errorf("%w: %w", action.ErrPermanent, errBadID)
	}

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}

	start, end := dayBounds(now(), c.Location)

	used, err := db.New(c.DB).CountActionUses(ctx, db.CountActionUsesParams{
		Action: actionName, ID: id, DayStart: start, DayEnd: end,
	})
	if err != nil {
		return fmt.Errorf("count the uses of %s today: %w", actionName, err)
	}

	if used >= int64(maxPerDay) {
		return fmt.Errorf("%d of %d uses taken today: %w: %w", used, maxPerDay, action.ErrLimit, errDailyFull)
	}

	return nil
}

// dayBounds returns the start of the day that holds t in loc, and the
// start of the next day. A day with a clock change is shorter or longer
// than 24 hours.
func dayBounds(t time.Time, loc *time.Location) (start, end time.Time) {
	if loc == nil {
		loc = time.UTC
	}

	y, m, d := t.In(loc).Date()

	return time.Date(y, m, d, 0, 0, 0, 0, loc), time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

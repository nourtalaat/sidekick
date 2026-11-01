//go:generate mockgen -source ledger.go -destination mocks_ledger.go -package summarize -write_package_comment=false

package summarize

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// spent is what a day's calls have cost, in micro-dollars.
type spent struct {
	route, total int64
}

// ledger reads and adds to the daily spend. It is an interface so the
// logic of a call can be tested without a database.
type ledger interface {
	// dailySpend returns what route and all routes together spent on day.
	dailySpend(ctx context.Context, route string, day pgtype.Date) (spent, error)
	// addSpend adds the cost and tokens of one call to the day's spend.
	addSpend(ctx context.Context, p db.AddSpendParams) error
}

// queryLedger is the ledger of spend_daily.
type queryLedger struct {
	q *db.Queries
}

func (l queryLedger) dailySpend(ctx context.Context, route string, day pgtype.Date) (spent, error) {
	row, err := l.q.GetDailySpend(ctx, db.GetDailySpendParams{Route: route, Day: day})
	if err != nil {
		return spent{}, fmt.Errorf("query the daily spend: %w", err)
	}

	return spent{route: row.RouteMicros, total: row.TotalMicros}, nil
}

func (l queryLedger) addSpend(ctx context.Context, p db.AddSpendParams) error {
	if err := l.q.AddSpend(ctx, p); err != nil {
		return fmt.Errorf("insert the daily spend: %w", err)
	}

	return nil
}

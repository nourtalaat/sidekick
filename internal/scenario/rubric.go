//go:generate mockgen -source rubric.go -destination mocks_rubric.go -package scenario -write_package_comment=false

package scenario

import (
	"context"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// RubricDrafter drafts the rubric of a scenario from the confirmed verdict
// and cause, which are already cleaned.
type RubricDrafter interface {
	// Enabled reports whether drafting does anything. A drafter that is not
	// enabled leaves the rubric empty.
	Enabled() bool
	// Draft returns rubric items for the cause. q is the transaction that
	// keeps the cost of the model call in the day's spend of the route.
	Draft(ctx context.Context, q db.DBTX, route, verdict, cause string) ([]bench.RubricItem, error)
}

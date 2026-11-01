package scenario

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// statusReviewed is the status of a scenario that can be run or downloaded.
const statusReviewed = "reviewed"

// pathPrefix starts the Path of a scenario read from the database.
const pathPrefix = "scenario:"

// LoadReviewed reads every reviewed scenario from the database, by ID, with
// snapshot paths relative to baseDir. It is the only way to read scenarios
// to run them: a draft is never returned.
func LoadReviewed(ctx context.Context, q db.DBTX, baseDir string) ([]bench.Scenario, error) {
	rows, err := db.New(q).ListReviewedScenarios(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reviewed scenarios: %w", err)
	}

	out := make([]bench.Scenario, 0, len(rows))

	for _, row := range rows {
		sc, err := parseRow(row, baseDir)
		if err != nil {
			return nil, fmt.Errorf("load reviewed scenarios: %w", err)
		}

		out = append(out, sc)
	}

	return out, nil
}

// LoadOneReviewed reads one reviewed scenario. A scenario that is not
// reviewed gives ErrNotReviewed, and one that does not exist ErrNotFound.
func LoadOneReviewed(ctx context.Context, q db.DBTX, id, baseDir string) (bench.Scenario, error) {
	row, err := reviewedRow(ctx, db.New(q), id)
	if err != nil {
		return bench.Scenario{}, fmt.Errorf("load scenario %s: %w", id, err)
	}

	return parseRow(row, baseDir)
}

// reviewedRow reads a scenario only when it is reviewed. It tells a draft,
// which gives ErrNotReviewed, from a scenario that is not there.
func reviewedRow(ctx context.Context, q *db.Queries, id string) (db.Scenario, error) {
	row, err := q.GetReviewedScenario(ctx, id)
	if err == nil {
		return row, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Scenario{}, fmt.Errorf("read reviewed scenario %s: %w", id, err)
	}

	if _, gerr := q.GetScenario(ctx, id); gerr == nil {
		return db.Scenario{}, ErrNotReviewed
	} else if !errors.Is(gerr, pgx.ErrNoRows) {
		return db.Scenario{}, fmt.Errorf("read scenario %s: %w", id, gerr)
	}

	return db.Scenario{}, ErrNotFound
}

// parseRow reads the file of a stored scenario.
func parseRow(row db.Scenario, baseDir string) (bench.Scenario, error) {
	sc, err := bench.ParseScenario([]byte(row.Body))
	if err != nil {
		return bench.Scenario{}, fmt.Errorf("read scenario %s: %w", row.ID, err)
	}

	sc.Path = pathPrefix + row.ID
	sc.ResolveSnapshots(baseDir)

	return sc, nil
}

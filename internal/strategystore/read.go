package strategystore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Current returns the current version of every runtime strategy, with its
// body, in name order.
func (s *Store) Current(ctx context.Context) ([]Version, error) {
	rows, err := db.New(s.pool).ListCurrentStrategies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list runtime strategies: %w", err)
	}

	out := make([]Version, len(rows))
	for i, r := range rows {
		out[i] = Version{
			Name: r.Name, Number: int(r.CurrentVersion), Body: r.Body, Author: r.Author, AuthorName: r.AuthorName,
			Reason: r.Reason, CreatedAt: r.CreatedAt,
		}
	}

	return out, nil
}

// Versions returns up to limit versions of the strategy name, newest
// first, without their bodies. With before, it returns the versions below
// that number. A strategy that is not stored has none.
func (s *Store) Versions(ctx context.Context, name string, before *int, limit int) ([]Version, error) {
	var cursor *int32
	if before != nil {
		c := int32(*before) //nolint:gosec // The API bounds the cursor.
		cursor = &c
	}

	rows, err := db.New(s.pool).ListStrategyVersions(ctx, db.ListStrategyVersionsParams{Name: name, Before: cursor, PageSize: int32(limit)}) //nolint:gosec // The API bounds the page size.
	if err != nil {
		return nil, fmt.Errorf("list versions of strategy %s: %w", name, err)
	}

	out := make([]Version, len(rows))
	for i, r := range rows {
		out[i] = Version{
			Name: r.Name, Number: int(r.Version), Author: r.Author, AuthorName: r.AuthorName, Reason: r.Reason,
			CreatedAt: r.CreatedAt, ProposalID: r.ProposalID, BenchRunID: r.BenchRunID,
		}
	}

	return out, nil
}

// Version returns version n of the strategy name, with its body. The error
// wraps ErrNotFound when there is no such version.
func (s *Store) Version(ctx context.Context, name string, n int) (Version, error) {
	row, err := db.New(s.pool).GetStrategyVersion(ctx, db.GetStrategyVersionParams{Name: name, Version: int32(n)}) //nolint:gosec // A number past the limit finds nothing.
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, fmt.Errorf("version %d of strategy %s: %w", n, name, ErrNotFound)
	}

	if err != nil {
		return Version{}, fmt.Errorf("read version %d of strategy %s: %w", n, name, err)
	}

	return Version{
		Name: row.Name, Number: int(row.Version), Body: row.Body, Author: row.Author, AuthorName: row.AuthorName,
		Reason: row.Reason, CreatedAt: row.CreatedAt, ProposalID: row.ProposalID, BenchRunID: row.BenchRunID,
	}, nil
}

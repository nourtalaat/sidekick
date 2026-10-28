//go:build integration

package memory

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The investigations in fixture/investigations.sql.
var (
	confirmedInvestigation   = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	noSummaryInvestigation   = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")
	unconfirmedInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c3")
)

// entry is a memory_entries row as the Sync tests read it.
type entry struct {
	Service     *string
	AlertName   *string
	Fingerprint string
	Verdict     string
	Summary     string
	Cause       *string
	ConfirmedAt time.Time
	Disabled    bool
}

func TestSync(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   uuid.UUID
		// seed runs before Sync.
		seed string
		// want is the entry after Sync, nil for none.
		want *entry
	}{
		{
			name: "a confirmed investigation gets an entry from its first event",
			id:   confirmedInvestigation,
			want: &entry{
				Service: new("checkout-api"), AlertName: new("CheckoutLatencyP99"), Fingerprint: "fp-checkout",
				Verdict: "true_positive", Summary: "p99 latency rose after deploy 4f2c1a.", Cause: new("Deploy 4f2c1a"),
				ConfirmedAt: time.Date(2026, 10, 26, 14, 10, 0, 0, time.UTC),
			},
		},
		{
			name: "the reason stands in for a missing summary",
			id:   noSummaryInvestigation,
			want: &entry{
				Service: new("orders-api"), AlertName: new("OrdersSlow"), Fingerprint: "fp-orders",
				Verdict: "false_positive", Summary: "Only the batch export endpoint was slow.",
				ConfirmedAt: time.Date(2026, 10, 26, 15, 10, 0, 0, time.UTC),
			},
		},
		{
			name: "an unconfirmed investigation gets none",
			id:   unconfirmedInvestigation,
		},
		{
			name: "an entry whose confirmation was taken back is deleted",
			id:   unconfirmedInvestigation,
			seed: `INSERT INTO memory_entries (investigation_id, fingerprint, verdict, summary, confirmed_at)
			       VALUES ('0192d3a4-0000-7000-8000-0000000000c3', 'fp-orders', 'true_positive', 'Slow queries.', now())`,
		},
		{
			name: "a changed confirmation updates the entry and keeps it disabled",
			id:   confirmedInvestigation,
			seed: `INSERT INTO memory_entries (investigation_id, fingerprint, verdict, summary, confirmed_at, disabled)
			       VALUES ('0192d3a4-0000-7000-8000-0000000000c1', 'fp-old', 'false_positive', 'Old summary.', now(), true)`,
			want: &entry{
				Service: new("checkout-api"), AlertName: new("CheckoutLatencyP99"), Fingerprint: "fp-checkout",
				Verdict: "true_positive", Summary: "p99 latency rose after deploy 4f2c1a.", Cause: new("Deploy 4f2c1a"),
				ConfirmedAt: time.Date(2026, 10, 26, 14, 10, 0, 0, time.UTC), Disabled: true,
			},
		},
		{
			name: "a changed confirmation after the group's events are gone keeps the entry's alert fields",
			id:   confirmedInvestigation,
			seed: `INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, confirmed_at)
			       VALUES ('0192d3a4-0000-7000-8000-0000000000c1', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout',
			               'false_positive', 'Old summary.', now());
			       DELETE FROM events WHERE group_id = '0192d3a4-0000-7000-8000-0000000000b1'`,
			want: &entry{
				Service: new("checkout-api"), AlertName: new("CheckoutLatencyP99"), Fingerprint: "fp-checkout",
				Verdict: "true_positive", Summary: "p99 latency rose after deploy 4f2c1a.", Cause: new("Deploy 4f2c1a"),
				ConfirmedAt: time.Date(2026, 10, 26, 14, 10, 0, 0, time.UTC),
			},
		},
		{
			name: "a confirmation after the group's events are gone gets an entry without alert fields",
			id:   noSummaryInvestigation,
			seed: `DELETE FROM events WHERE group_id = '0192d3a4-0000-7000-8000-0000000000b2'`,
			want: &entry{
				Verdict: "false_positive", Summary: "Only the batch export endpoint was slow.",
				ConfirmedAt: time.Date(2026, 10, 26, 15, 10, 0, 0, time.UTC),
			},
		},
		{
			name: "an investigation that does not exist gets none",
			id:   uuid.MustParse("0192d3a4-0000-7000-8000-0000000000cf"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t, "fixture/investigations.sql")

			if tt.seed != "" {
				_, err := pool.Exec(t.Context(), tt.seed)
				require.NoError(t, err)
			}

			err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
				return Sync(t.Context(), tx, tt.id)
			})
			require.NoError(t, err)

			require.Equal(t, tt.want, readEntry(t, pool, tt.id))
		})
	}
}

// readEntry returns the memory entry of the investigation with id, or nil.
func readEntry(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) *entry {
	t.Helper()

	var e entry

	err := pool.QueryRow(t.Context(), `
		SELECT service, alert_name, fingerprint, verdict, summary, cause, confirmed_at, disabled
		FROM memory_entries WHERE investigation_id = $1`, id,
	).Scan(&e.Service, &e.AlertName, &e.Fingerprint, &e.Verdict, &e.Summary, &e.Cause, &e.ConfirmedAt, &e.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	require.NoError(t, err)

	e.ConfirmedAt = e.ConfirmedAt.UTC()

	return &e
}

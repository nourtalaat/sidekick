//go:build integration

package slack

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestThread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		investigation string
		want          Target
		wantErr       error
	}{
		{name: "the thread the first Slack output published, not a later one or the Teams one", investigation: slackAlertWithThread, want: Target{Channel: "C0123456789", TS: "1700000000.000100"}},
		{name: "the thread of an alert that is a reply", investigation: slackAlertInThread, want: Target{Channel: "C0123456789", TS: "1700000400.000100"}},
		{name: "a thread published for a webhook alert", investigation: webhookAlertWithPost, want: Target{Channel: "C0000000002", TS: "1700000900.000300"}},
		{name: "no Slack message at all", investigation: webhookAlertNoMessage, wantErr: errNoThread},
		{name: "a mention that joined a webhook alert's group is not its thread", investigation: webhookThenMention, wantErr: errNoThread},
		{name: "a person's alert in a channel", investigation: personPostedAlert, want: Target{Channel: "C0123456789", TS: "1700000700.000300"}},
		{name: "an investigation that does not exist", investigation: uuid.NewString(), wantErr: errNoThread},
		{name: "an ID that is not a UUID", investigation: "nope", wantErr: errBadID},
	}

	pool := setup(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dbTargets{db: pool}.Thread(t.Context(), tt.investigation)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrPermanent)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAlert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		investigation string
		want          Target
		wantErr       error
	}{
		{name: "an alert message", investigation: slackAlertWithThread, want: Target{Channel: "C0123456789", TS: "1700000000.000100"}},
		{name: "an alert that is a reply is reacted to itself", investigation: slackAlertInThread, want: Target{Channel: "C0123456789", TS: "1700000500.000200"}},
		{name: "a webhook alert", investigation: webhookAlertWithPost, wantErr: errNoAlert},
		{name: "a mention that joined a webhook alert's group is not the alert", investigation: webhookThenMention, wantErr: errNoAlert},
		{name: "a person's alert in a channel", investigation: personPostedAlert, want: Target{Channel: "C0123456789", TS: "1700000700.000300"}},
		{name: "an ID that is not a UUID", investigation: "nope", wantErr: errBadID},
	}

	pool := setup(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dbTargets{db: pool}.Alert(t.Context(), tt.investigation)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrPermanent)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTargetsWithoutADatabase(t *testing.T) {
	t.Parallel()

	_, err := dbTargets{}.Thread(t.Context(), slackAlertWithThread)
	require.ErrorIs(t, err, errNoDB)
	require.ErrorIs(t, err, action.ErrPermanent)
}

// TestQueryPlans runs EXPLAIN ANALYZE on the two target queries over a busy
// database, and checks that each reads by key.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "../proposals/fixture/busy.sql")

	// Events for the groups of the busy fixture, one Slack alert each, and
	// an output ref for every investigation.
	_, err := pool.Exec(t.Context(), `
		INSERT INTO events (id, intake, key, kind, trusted, fingerprint, slack, status, group_id, received_at)
		SELECT md5('event' || n)::uuid, 'alerts-channel', 'k' || n, 'alert', true, 'fp-' || n,
		       jsonb_build_object('Channel', 'C0123456789', 'TS', '1700000000.' || lpad(n::text, 6, '0')), 'grouped',
		       md5('group' || n)::uuid, now() - make_interval(secs => n)
		FROM generate_series(1, 5000) AS n;
		INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
		SELECT md5('investigation' || n)::uuid, 'slack-thread', 'C0123456789:1700000000.' || lpad(n::text, 6, '0'), '{}', 1, now()
		FROM generate_series(1, 5000) AS n;
		ANALYZE;`)
	require.NoError(t, err)

	var investigation uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('investigation' || 2500)::uuid").Scan(&investigation))

	tests := []struct {
		name      string
		run       func(q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "ListInvestigationOutputRefs",
			run: func(q *db.Queries) error {
				_, err := q.ListInvestigationOutputRefs(t.Context(), investigation)

				return err //nolint:wrapcheck // the test checks for ErrExplained
			},
			wantIndex: []string{"output_refs_pkey"},
		},
		{
			name: "GetInvestigationAlertSlackRef",
			run: func(q *db.Queries) error {
				_, err := q.GetInvestigationAlertSlackRef(t.Context(), investigation)

				return err //nolint:wrapcheck // the test checks for ErrExplained
			},
			wantIndex: []string{"investigations_pkey", "events_group"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}

}

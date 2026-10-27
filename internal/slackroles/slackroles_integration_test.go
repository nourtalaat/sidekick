//go:build integration

package slackroles_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The user groups of slackConfig, in the order they are asked about.
var allGroups = []string{"S01ONCALL", "S01SRE"}

func TestLookup(t *testing.T) {
	t.Parallel()

	type lookupFunc func(l *slackroles.Lookup, ctx context.Context, team, user string) (roles.Role, error)

	role := (*slackroles.Lookup).Role
	refresh := (*slackroles.Lookup).Refresh

	tests := []struct {
		name       string
		user       string
		lookup     lookupFunc
		responses  []string
		want       roles.Role
		wantErr    bool
		wantCalls  []string
		wantGroups []string
	}{
		{
			name:       "the first lookup reads the groups from Slack and stores them",
			user:       "U01ANA",
			lookup:     role,
			responses:  []string{noneFile, membersFile},
			want:       roles.Admin,
			wantCalls:  allGroups,
			wantGroups: []string{"S01SRE"},
		},
		{
			name:       "recent stored groups make no Slack call",
			user:       "U01FRESH",
			lookup:     role,
			want:       roles.Admin,
			wantGroups: []string{"S01SRE"},
		},
		{
			name:       "stale stored groups are read again",
			user:       "U01STALE",
			lookup:     role,
			responses:  []string{noneFile, noneFile},
			want:       roles.Responder,
			wantCalls:  allGroups,
			wantGroups: []string{},
		},
		{
			name:       "Refresh reads the groups even when they are recent",
			user:       "U01FRESH",
			lookup:     refresh,
			responses:  []string{membersFile, noneFile},
			want:       roles.Responder,
			wantCalls:  allGroups,
			wantGroups: []string{},
		},
		{
			name:       "stored groups are used when Slack fails",
			user:       "U01STALE",
			lookup:     role,
			responses:  []string{errorFile},
			want:       roles.Admin,
			wantCalls:  allGroups[:1],
			wantGroups: []string{"S01SRE"},
		},
		{
			name:      "a Slack failure without stored groups is an error",
			user:      "U01ANA",
			lookup:    role,
			responses: []string{errorFile},
			wantErr:   true,
			wantCalls: allGroups[:1],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookup, api, pool := setup(t)

			if len(tt.responses) > 0 {
				responses := make([]fakehttp.Response, len(tt.responses))
				for i, file := range tt.responses {
					responses[i] = fakehttp.Response{BodyFile: file, Headers: map[string]string{"Content-Type": "application/json"}}
				}

				api.Handle(http.MethodPost, listPath, responses...)
				api.ExpectAllCalled()
			}

			got, err := tt.lookup(lookup, t.Context(), "T01TEAM", tt.user)
			require.Equal(t, tt.wantCalls, groupCalls(t, api))

			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			stored, err := db.New(pool).GetSlackIdentity(t.Context(), tt.user)
			require.NoError(t, err)
			require.ElementsMatch(t, tt.wantGroups, stored.Groups)
		})
	}
}

// TestQueryPlans runs EXPLAIN ANALYZE on the slack_identities queries over
// 5,000 users.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	role := "admin"

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex string
	}{
		{
			name: "GetSlackIdentity",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetSlackIdentity(ctx, "U2500")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "slack_identities_pkey",
		},
		{
			name: "SaveSlackIdentity",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.SaveSlackIdentity(ctx, db.SaveSlackIdentityParams{SlackUserID: "U2500", Groups: []string{"S01SRE"}, Role: &role, RefreshedAt: time.Now()})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "slack_identities_pkey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)
			require.Contains(t, explain.Plan, tt.wantIndex)
			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

//go:build integration

package slackroles_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// usersInfoPath is the path of users.info on the fake Slack.
const usersInfoPath = "/api/users.info"

// leadID is the web UI user Lead of fixture/emails.sql.
var leadID = uuid.MustParse("01920000-0000-7000-8000-000000000050")

func TestLookupMatchesEmails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user string
		// info is the users.info answer, if Slack is asked.
		info       string
		want       roles.Role
		wantEmail  *string
		wantUserID *uuid.UUID
	}{
		{
			name: "a Slack user with a web UI user's email gets that user's role", user: "U01NEW",
			info: "fixture/slack/user-lead.json", want: roles.Approver, wantEmail: ptr("Lead@Example.com"), wantUserID: &leadID,
		},
		{
			name: "a matched viewer is a viewer, not an unmatched responder", user: "U01NEW",
			info: "fixture/slack/user-viewer.json", want: roles.Viewer, wantEmail: ptr("vi@example.com"),
			wantUserID: ptr(uuid.MustParse("01920000-0000-7000-8000-000000000060")),
		},
		{
			name: "a slack.roleUsers role above the web UI role wins", user: "U01ADMIN",
			info: "fixture/slack/user-viewer.json", want: roles.Admin, wantEmail: ptr("vi@example.com"),
			wantUserID: ptr(uuid.MustParse("01920000-0000-7000-8000-000000000060")),
		},
		{
			name: "an email no web UI user has", user: "U01NEW",
			info: "fixture/slack/user-stranger.json", want: roles.Responder, wantEmail: ptr("someone@elsewhere.example"),
		},
		{
			name: "without the users:read.email scope the user is unmatched", user: "U01NEW",
			info: "fixture/slack/user-no-email-scope.json", want: roles.Responder,
		},
		{
			name: "a recent match makes no Slack call and uses the web UI user's current role", user: "U01KNOWN",
			want: roles.Approver, wantEmail: ptr("lead@example.com"), wantUserID: &leadID,
		},
		{
			name: "an old match whose email Slack no longer gives keeps the stored email", user: "U01OLDMAIL",
			info: "fixture/slack/user-no-email-scope.json", want: roles.Approver, wantEmail: ptr("lead@example.com"), wantUserID: &leadID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			storetest.LoadFixture(t, pool, "fixture/emails.sql")

			api := fakehttp.New(t)
			if tt.info != "" {
				api.Handle(http.MethodPost, usersInfoPath, fakehttp.Response{BodyFile: tt.info, Headers: map[string]string{"Content-Type": "application/json"}})
				api.ExpectAllCalled()
			}

			lookup, err := slackroles.New(slackroles.Options{
				Slack: &config.Slack{
					BotTokenSecret: registry.SecretRef{Name: "sidekick-slack", Key: "botToken"},
					APIURL:         api.URL() + "/api/",
					RoleUsers:      map[string]config.RoleName{"U01ADMIN": config.RoleAdmin},
				},
				Secrets:     staticSecrets{},
				Pool:        pool,
				HTTP:        api.Client(),
				MatchEmails: true,
			})
			require.NoError(t, err)

			got, err := lookup.Role(t.Context(), "T01TEAM", tt.user)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			if tt.info == "" {
				require.Empty(t, api.Requests())
			}

			stored, err := db.New(pool).GetSlackIdentity(t.Context(), tt.user)
			require.NoError(t, err)
			require.Equal(t, tt.wantEmail, stored.Email)
			require.Equal(t, tt.wantUserID, stored.UserID)
		})
	}
}

func ptr[T any](v T) *T {
	return &v
}

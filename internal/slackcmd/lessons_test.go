package slackcmd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/lessons"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
)

func TestLessonCommand(t *testing.T) {
	t.Parallel()

	const user = "U01ANA"

	actor := lessons.Actor{Kind: audit.ActorUser, ID: user}
	created := func(n lessons.New) (lessons.Stored, error) {
		return lessons.Stored{ID: uuid.New(), Text: n.Text, Scope: n.Scope, Status: n.Status, Origin: n.Origin}, nil
	}

	tests := []struct {
		name       string
		text       string
		setupMocks func(r *MockRoleLookup, w *MockLessonWriter)
		// wantContains is in the reply; wantNot is not.
		wantContains, wantNot string
	}{
		{
			name: "a lesson for a service",
			text: `lesson add "Check the deploys first." service:checkout-api`,
			setupMocks: func(r *MockRoleLookup, w *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
				w.EXPECT().Create(gomock.Any(), actor, lessons.New{
					Text: "Check the deploys first.", Scope: lessons.Scope{Service: "checkout-api"}, Origin: lessons.OriginSlack, Status: lessons.StatusActive,
				}).DoAndReturn(func(_ any, _ lessons.Actor, n lessons.New) (lessons.Stored, error) { return created(n) })
			},
			wantContains: "Lesson added for service checkout-api: Check the deploys first.",
		},
		{
			name: "every scope option, in any case of the subcommand",
			text: `lesson ADD "Look at traffic." service:a alert:b route:c strategy:d`,
			setupMocks: func(r *MockRoleLookup, w *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
				w.EXPECT().Create(gomock.Any(), actor, lessons.New{
					Text: "Look at traffic.", Scope: lessons.Scope{Service: "a", Alert: "b", Route: "c", Strategy: "d"},
					Origin: lessons.OriginSlack, Status: lessons.StatusActive,
				}).DoAndReturn(func(_ any, _ lessons.Actor, n lessons.New) (lessons.Stored, error) { return created(n) })
			},
			wantContains: "Lesson added for service a, alert b, route c, strategy d:",
		},
		{
			name: "the text is escaped in the reply",
			text: `lesson add "Tell <!channel> & wait." route:payments`,
			setupMocks: func(r *MockRoleLookup, w *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
				w.EXPECT().Create(gomock.Any(), actor, gomock.Any()).DoAndReturn(func(_ any, _ lessons.Actor, n lessons.New) (lessons.Stored, error) { return created(n) })
			},
			wantContains: "Tell &lt;!channel&gt; &amp; wait.",
			wantNot:      "<!channel>",
		},
		{
			name: "a role too low, also after a refresh",
			text: `lesson add "Check the deploys first." service:checkout-api`,
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
				r.EXPECT().Refresh(gomock.Any(), "T01", user).Return(roles.Responder, nil)
			},
			wantContains: "This needs the admin role, and you are a responder.",
		},
		{
			name:         "no subcommand shows the usage",
			text:         "lesson",
			setupMocks:   func(*MockRoleLookup, *MockLessonWriter) {},
			wantContains: "Usage: `/sidekick lesson add",
		},
		{
			name:         "another subcommand shows the usage",
			text:         "lesson remove 3",
			setupMocks:   func(*MockRoleLookup, *MockLessonWriter) {},
			wantContains: "Usage: `/sidekick lesson add",
		},
		{
			name: "no text",
			text: "lesson add",
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "Usage: `/sidekick lesson add",
		},
		{
			name: "no scope option",
			text: `lesson add "Check the deploys first."`,
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "A lesson from Slack needs at least one scope option.",
		},
		{
			name: "text that is not in quotes",
			text: "lesson add Check the deploys first service:checkout-api",
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "`the` is not a scope option.",
		},
		{
			name: "an option that does not exist",
			text: `lesson add "Check." colour:blue`,
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "`colour` is not a scope option.",
		},
		{
			name: "an option with no value",
			text: `lesson add "Check." service:`,
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "`service:` is not a scope option.",
		},
		{
			name: "an option given twice",
			text: `lesson add "Check." service:a service:b`,
			setupMocks: func(r *MockRoleLookup, _ *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "`service` is given twice.",
		},
		{
			name: "a lesson the store refuses",
			text: `lesson add "Check." service:a`,
			setupMocks: func(r *MockRoleLookup, w *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
				w.EXPECT().Create(gomock.Any(), actor, gomock.Any()).Return(lessons.Stored{}, &lessons.InvalidError{Field: "text", Detail: "The text is longer than 400 characters"})
			},
			wantContains: "The text is longer than 400 characters.",
		},
		{
			name: "a store that fails",
			text: `lesson add "Check." service:a`,
			setupMocks: func(r *MockRoleLookup, w *MockLessonWriter) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Admin, nil)
				w.EXPECT().Create(gomock.Any(), actor, gomock.Any()).Return(lessons.Stored{}, errSlackDown)
			},
			wantContains: msgInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			lookup := NewMockRoleLookup(ctrl)
			writer := NewMockLessonWriter(ctrl)
			tt.setupMocks(lookup, writer)

			cfg := fileConfig()
			h := New(Options{
				File:    cfg,
				Rules:   rules.NewCache(t.Context(), rules.Options{File: cfg}),
				Roles:   lookup,
				Lessons: writer,
				Version: "test",
				Timeout: time.Minute,
			})

			body, err := h.HandleCommand(t.Context(), slack.SlashCommand{TeamID: "T01", UserID: user, Command: "/sidekick", Text: tt.text})
			require.NoError(t, err)

			var reply response
			require.NoError(t, json.Unmarshal(body, &reply))
			require.Contains(t, reply.Text, tt.wantContains)

			if tt.wantNot != "" {
				require.NotContains(t, reply.Text, tt.wantNot)
			}
		})
	}
}

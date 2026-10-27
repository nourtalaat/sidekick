package slackcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/slackapp"
)

// errSlackDown stands for a Slack failure.
var errSlackDown = errors.New("slack is down")

// fileConfig is a loaded config with one file listener and two file
// routes, built by hand.
func fileConfig() *config.Config {
	payments := config.NewRoute("payments")
	payments.Priority = 50
	payments.Match = &config.Match{Service: "payments-*"}

	return &config.Config{
		Slack: &config.Slack{
			BotTokenSecret: registry.SecretRef{Name: "slack", Key: "botToken"},
			AppTokenSecret: registry.SecretRef{Name: "slack", Key: "appToken"},
			Mode:           config.SlackModeSocket,
		},
		Listeners: map[string]config.Listener{"alerts": {Channel: "#alerts"}},
		Routes:    []config.Route{config.NewRoute(config.DefaultRouteName), payments},
		Budgets:   config.Budgets{PerInvestigation: config.Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute}},
	}
}

func TestHandleCommand(t *testing.T) {
	t.Parallel()

	const user = "U01ANA"

	tests := []struct {
		name         string
		text         string
		setupMocks   func(r *MockRoleLookup, i *MockInvestigator)
		wantContains string
	}{
		{name: "help", text: "help", setupMocks: func(*MockRoleLookup, *MockInvestigator) {}, wantContains: "*/sidekick* commands:"},
		{name: "an unknown subcommand", text: "nope", setupMocks: func(*MockRoleLookup, *MockInvestigator) {}, wantContains: "There is no `/sidekick nope` command."},
		{
			name: "a role too low, also after a refresh",
			text: "route set checkout priority 5",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
				r.EXPECT().Refresh(gomock.Any(), "T01", user).Return(roles.Responder, nil)
			},
			wantContains: "This needs the admin role, and you are a responder.",
		},
		{
			name: "a role that a refresh raises",
			text: "route set checkout colour blue",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
				r.EXPECT().Refresh(gomock.Any(), "T01", user).Return(roles.Admin, nil)
			},
			wantContains: "`colour` is not a field Slack can set.",
		},
		{
			name: "a role lookup that fails",
			text: "listeners",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Role(0), errSlackDown)
			},
			wantContains: msgInternal,
		},
		{
			name: "listeners",
			text: "listeners",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Viewer, nil)
			},
			wantContains: "• `alerts` (config file) in #alerts: every message",
		},
		{
			name: "routes",
			text: "routes",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Viewer, nil)
			},
			wantContains: "1. `payments` (config file, priority 50): matches service `payments-*`",
		},
		{
			name: "investigate",
			text: "investigate https://acme.slack.com/archives/C01/p1761572520123456",
			setupMocks: func(r *MockRoleLookup, i *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
				i.EXPECT().InvestigateLink(gomock.Any(), "T01", "A01", "command:trigger-1", "https://acme.slack.com/archives/C01/p1761572520123456").Return(nil)
			},
			wantContains: "Sidekick is looking into that message",
		},
		{
			name: "investigate a message Sidekick posted",
			text: "investigate https://acme.slack.com/archives/C01/p1761572520123456",
			setupMocks: func(r *MockRoleLookup, i *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
				i.EXPECT().InvestigateLink(gomock.Any(), "T01", "A01", "command:trigger-1", gomock.Any()).Return(fmt.Errorf("check: %w", slackapp.ErrOwnMessage))
			},
			wantContains: "That is a message from Sidekick itself",
		},
		{
			name: "investigate when Slack fails",
			text: "investigate https://acme.slack.com/archives/C01/p1761572520123456",
			setupMocks: func(r *MockRoleLookup, i *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
				i.EXPECT().InvestigateLink(gomock.Any(), "T01", "A01", "command:trigger-1", gomock.Any()).Return(errSlackDown)
			},
			wantContains: msgInternal,
		},
		{
			name: "investigate without a link",
			text: "investigate",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Responder, nil)
			},
			wantContains: "Usage: `/sidekick investigate <message link>`",
		},
		{
			name: "status",
			text: "status",
			setupMocks: func(r *MockRoleLookup, _ *MockInvestigator) {
				r.EXPECT().Role(gomock.Any(), "T01", user).Return(roles.Approver, nil)
			},
			wantContains: "Your role: approver.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			lookup := NewMockRoleLookup(ctrl)
			investigator := NewMockInvestigator(ctrl)
			tt.setupMocks(lookup, investigator)

			cfg := fileConfig()
			h := New(Options{
				File:         cfg,
				Rules:        rules.NewCache(t.Context(), rules.Options{File: cfg}),
				Roles:        lookup,
				Investigator: investigator,
				Version:      "test",
			})

			body, err := h.HandleCommand(t.Context(), slack.SlashCommand{
				TeamID: "T01", UserID: user, Command: "/sidekick", Text: tt.text, TriggerID: "trigger-1", APIAppID: "A01",
			})
			require.NoError(t, err)

			var reply response
			require.NoError(t, json.Unmarshal(body, &reply))
			require.Equal(t, responseEphemeral, reply.ResponseType)
			require.Contains(t, reply.Text, tt.wantContains)
		})
	}
}

func TestRouteFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		field   string
		value   string
		check   func(t *testing.T, r rules.Route)
		wantErr string
	}{
		{field: "priority", value: "5", check: func(t *testing.T, r rules.Route) { t.Helper(); require.Equal(t, 5, r.Config.Priority) }},
		{field: "priority", value: "high", wantErr: "priority must be a whole number"},
		{field: "enabled", value: "false", check: func(t *testing.T, r rules.Route) { t.Helper(); require.False(t, r.Enabled) }},
		{field: "enabled", value: "maybe", wantErr: "enabled must be true or false"},
		{field: "mode", value: "read-write", wantErr: "Slack can set only mode `read-only`"},
		{field: "sources", value: "signoz, k8s", check: func(t *testing.T, r rules.Route) {
			t.Helper()
			require.Equal(t, []string{"signoz", "k8s"}, r.Config.Sources)
		}},
		{field: "outputs", value: "none", check: func(t *testing.T, r rules.Route) { t.Helper(); require.Nil(t, r.Config.Outputs) }},
		{field: "severity", value: "critical,warning", check: func(t *testing.T, r rules.Route) {
			t.Helper()
			require.Len(t, r.Config.Match.Severity, 2)
		}},
		{field: "labels", value: "team=payments,env=prod", check: func(t *testing.T, r rules.Route) {
			t.Helper()
			require.Equal(t, map[string]string{"team": "payments", "env": "prod"}, r.Config.Match.Labels)
		}},
		{field: "labels", value: "team", wantErr: "labels must be <name>=<value> pairs"},
		{field: "service", value: "none", check: func(t *testing.T, r rules.Route) { t.Helper(); require.Nil(t, r.Config.Match) }},
		{field: "maxWallTime", value: "5m", check: func(t *testing.T, r rules.Route) {
			t.Helper()
			require.Equal(t, 5*time.Minute, r.Config.Budgets.MaxWallTime)
		}},
		{field: "maxWallTime", value: "soon", wantErr: "is not a duration"},
		{field: "window", value: "0", check: func(t *testing.T, r rules.Route) { t.Helper(); require.Zero(t, r.Config.Grouping.Window) }},
	}

	for _, tt := range tests {
		t.Run(tt.field+" "+tt.value, func(t *testing.T) {
			t.Parallel()

			r := rules.Route{Config: config.NewRoute("checkout"), Enabled: true}
			r.Config.Match = &config.Match{Service: "checkout-*"}

			var field routeField

			for _, f := range routeFields {
				if f.name == tt.field {
					field = f
				}
			}

			require.NotNil(t, field.set)

			err := field.set(&r, tt.value)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			tt.check(t, r)
		})
	}
}

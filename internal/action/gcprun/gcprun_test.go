package gcprun

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	longName := "payments-" + strings.Repeat("a", maxServiceLen-len("payments-"))

	tests := []struct {
		name    string
		cfg     func(*Config)
		params  string
		wantErr string
		wantIs  error
	}{
		{name: "a proposal inside the limits", params: paramsJSON},
		{name: "project and location left out when one of each is listed", params: `{"service":"checkout"}`},
		{name: "a service the glob matches", params: `{"service":"payments-api"}`},
		{name: "a service the glob matches with one character for the star", params: `{"service":"payments-a"}`},
		{name: "a service the glob matches but that ends with a hyphen", params: `{"service":"payments-"}`, wantErr: "is not a Cloud Run service name", wantIs: action.ErrParams},
		{name: "a service name of 49 characters", params: `{"service":"` + longName + `"}`},
		{name: "a service name of 50 characters", params: `{"service":"` + longName + `a"}`, wantErr: "is not a Cloud Run service name", wantIs: action.ErrParams},
		{name: "a service the globs do not match", params: `{"service":"checkout-2"}`, wantErr: "not one of limits.services", wantIs: action.ErrLimit},
		{name: "a service name with capitals", params: `{"service":"Checkout"}`, wantErr: "is not a Cloud Run service name", wantIs: action.ErrParams},
		{name: "a project outside the limits", params: `{"project":"other-prod","service":"checkout"}`, wantErr: "not one of limits.projects", wantIs: action.ErrLimit},
		{name: "a location outside the limits", params: `{"location":"us-east1","service":"checkout"}`, wantErr: "not one of limits.locations", wantIs: action.ErrLimit},
		{
			name:    "no project when several are listed",
			cfg:     func(c *Config) { c.Limits.Projects = append(c.Limits.Projects, "other-prod") },
			params:  `{"location":"europe-west1","service":"checkout"}`,
			wantErr: "project is needed", wantIs: action.ErrParams,
		},
		{
			name:    "no location when several are listed",
			cfg:     func(c *Config) { c.Limits.Locations = append(c.Limits.Locations, "us-east1") },
			params:  `{"project":"shop-prod","service":"checkout"}`,
			wantErr: "location is needed", wantIs: action.ErrParams,
		},
		{name: "no service", params: `{}`, wantErr: "is not a Cloud Run service name", wantIs: action.ErrParams},
		{name: "a key the action does not have", params: `{"service":"checkout","x":1}`, wantErr: "decode the parameters", wantIs: action.ErrParams},
		{name: "revisionsBack at the default limit", params: `{"service":"checkout","revisionsBack":1}`},
		{name: "revisionsBack just above the default limit", params: `{"service":"checkout","revisionsBack":2}`, wantErr: "more than maxRevisionsBack allows (1)", wantIs: action.ErrLimit},
		{name: "revisionsBack of zero", params: `{"service":"checkout","revisionsBack":0}`, wantErr: "0 revisions back", wantIs: action.ErrParams},
		{
			name:   "revisionsBack at a raised limit",
			cfg:    func(c *Config) { c.MaxRevisionsBack = new(3) },
			params: `{"service":"checkout","revisionsBack":3}`,
		},
		{
			name:    "revisionsBack just above a raised limit",
			cfg:     func(c *Config) { c.MaxRevisionsBack = new(3) },
			params:  `{"service":"checkout","revisionsBack":4}`,
			wantErr: "more than maxRevisionsBack allows (3)", wantIs: action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			cfg := limitedConfig()

			if tt.cfg != nil {
				tt.cfg(&cfg)
			}

			err := buildAction(t, api, cfg).Validate(context.Background(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, tt.wantIs)
			}

			require.Empty(t, api.Requests(), "Validate makes no call")
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	cfg := limitedConfig()
	cfg.MaxRevisionsBack = new(2)

	spec := buildAction(t, fakehttp.New(t), cfg).Spec()

	require.Equal(t, "cloud-run-rollback", spec.Name)
	require.Contains(t, spec.Description, "pinned to that revision")
	require.Contains(t, spec.Description, "later deploys get no traffic")
	require.Contains(t, spec.Description, "Allowed projects: shop-prod")
	require.Contains(t, spec.Description, "Allowed services: checkout, payments-*")
	require.Contains(t, spec.Description, "from 1 to 2")

	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(spec.ParamsSchema, &schema))
	require.Equal(t, []string{"service"}, schema.Required)
	require.Len(t, schema.Properties, 4)
}

func TestApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allow   bool
		blocked bool
	}{
		{name: "without the opt-in", allow: false, blocked: true},
		{name: "with the opt-in", allow: true, blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := limitedConfig()
			cfg.AllowReadWrite = tt.allow

			require.Equal(t, tt.blocked, action.ReadWriteBlocked(&cfg))
			require.Equal(t, tt.blocked, action.ReadWriteBlocked(buildAction(t, fakehttp.New(t), cfg)))
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     func(*Config)
		deps    registry.Deps
		wantErr error
	}{
		{name: "with Application Default Credentials and no secrets", deps: registry.Deps{}},
		{
			name:    "with a credential file and no secrets",
			cfg:     func(c *Config) { c.CredentialsSecret = &registry.SecretRef{Name: "run", Key: "credentials.json"} },
			wantErr: errNoSecrets,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := limitedConfig()

			if tt.cfg != nil {
				tt.cfg(&cfg)
			}

			a, err := New(context.Background(), "cloud-run-rollback", cfg, tt.deps)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, a)
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.True(t, slices.Contains(registry.Actions.Types(), Type))
}

func TestFixesNothing(t *testing.T) {
	t.Parallel()

	_, ok := action.Action(&Rollback{}).(action.MonitorFixer)
	require.False(t, ok, "a rollback fixes the cause, not a noisy monitor")
}

// requireRefusal checks that err is a refusal with text.
func requireRefusal(t *testing.T, err error, text string) {
	t.Helper()

	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, text)
}

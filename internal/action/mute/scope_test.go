package mute_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

func TestCheckScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scopes  []mute.Scope
		wantErr []string
	}{
		{name: "one scope", scopes: []mute.Scope{{Labels: map[string][]string{"alertname": {"HighLatency", "HighErrorRate"}, "service": {"checkout-*"}}}}},
		{name: "two scopes", scopes: []mute.Scope{{Labels: map[string][]string{"team": {"payments"}}}, {Labels: map[string][]string{"alertname": {"A"}}}}},
		{name: "no scopes", wantErr: []string{"limits.scopes: at least one scope is required"}},
		{name: "a scope with no labels", scopes: []mute.Scope{{}}, wantErr: []string{"limits.scopes[0].labels: at least one label is required"}},
		{name: "a scope with an empty label set", scopes: []mute.Scope{{Labels: map[string][]string{}}}, wantErr: []string{"limits.scopes[0].labels: at least one label is required"}},
		{
			name:    "a label with no values",
			scopes:  []mute.Scope{{Labels: map[string][]string{"service": nil}}},
			wantErr: []string{"limits.scopes[0].labels.service: at least one value is required"},
		},
		{
			name:    "an empty value",
			scopes:  []mute.Scope{{Labels: map[string][]string{"service": {"a", ""}}}},
			wantErr: []string{"limits.scopes[0].labels.service: a value must not be empty"},
		},
		{
			name:    "a label that is not a name",
			scopes:  []mute.Scope{{Labels: map[string][]string{"my-label": {"a"}}}},
			wantErr: []string{`limits.scopes[0].labels.my-label: "my-label" is not a label name`},
		},
		{
			name:    "the second scope is the bad one",
			scopes:  []mute.Scope{{Labels: map[string][]string{"a": {"b"}}}, {}},
			wantErr: []string{"limits.scopes[1].labels: at least one label is required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mute.CheckScopes(tt.scopes)

			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestDescribeScopes(t *testing.T) {
	t.Parallel()

	got := mute.DescribeScopes([]mute.Scope{
		{Labels: map[string][]string{"service": {"checkout-*"}, "alertname": {"A", "B"}}},
		{Labels: map[string][]string{"team": {"payments"}}},
	})

	require.Equal(t, "scope 1 needs alertname in [A, B] and service in [checkout-*]; scope 2 needs team in [payments]", got)
}

func TestPermits(t *testing.T) {
	t.Parallel()

	scopes := []mute.Scope{
		{Labels: map[string][]string{"alertname": {"HighLatency", "HighErrorRate"}, "service": {"checkout-*"}}},
		{Labels: map[string][]string{"team": {"payments"}}},
	}

	tests := []struct {
		name     string
		matchers []mute.Matcher
		wantOK   bool
	}{
		{name: "every label of the first scope", matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "checkout-api")}, wantOK: true},
		{name: "the other scope", matchers: []mute.Matcher{eq("team", "payments")}, wantOK: true},
		{name: "extra matchers of any kind only narrow", matchers: []mute.Matcher{
			eq("team", "payments"), {Name: "env", Value: "prod|stage", IsRegex: true, IsEqual: true}, {Name: "severity", Value: "page"},
		}, wantOK: true},
		{name: "a value that is only the text before the star", matchers: []mute.Matcher{eq("alertname", "HighErrorRate"), eq("service", "checkout-")}, wantOK: true},
		{name: "one label of the scope missing", matchers: []mute.Matcher{eq("alertname", "HighLatency")}},
		{name: "a value outside the globs", matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "payments-api")}},
		{name: "a regex matcher on a scope label", matchers: []mute.Matcher{
			eq("alertname", "HighLatency"), {Name: "service", Value: "checkout-.*", IsRegex: true, IsEqual: true},
		}},
		{name: "a not-equal matcher on a scope label", matchers: []mute.Matcher{eq("alertname", "HighLatency"), {Name: "service", Value: "checkout-api"}}},
		{name: "labels of two scopes, none complete", matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("env", "prod")}},
		{name: "no matchers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mute.Permits(scopes, tt.matchers)

			if tt.wantOK {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, "scope 1 needs alertname in [HighLatency, HighErrorRate] and service in [checkout-*]")
			require.ErrorContains(t, err, "scope 2 needs team in [payments]")
		})
	}

	t.Run("a scope with no labels allows nothing", func(t *testing.T) {
		t.Parallel()

		require.ErrorIs(t, mute.Permits([]mute.Scope{{}}, []mute.Matcher{eq("a", "b")}), action.ErrLimit)
	})

	t.Run("no scopes allow nothing", func(t *testing.T) {
		t.Parallel()

		require.ErrorIs(t, mute.Permits(nil, []mute.Matcher{eq("a", "b")}), action.ErrLimit)
	})
}

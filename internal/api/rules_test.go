package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/rules"
)

func TestFieldOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		root string
		item string
		want string
	}{
		{name: "a field", path: "routes.checkout.budgets.maxTokens", root: "routes", item: "checkout", want: "budgets.maxTokens"},
		{name: "a list item", path: "routes.checkout.match.intake[1]", root: "routes", item: "checkout", want: "match.intake[1]"},
		{name: "the whole rule", path: "routes.checkout", root: "routes", item: "checkout", want: ""},
		{name: "a name that needs brackets", path: `listeners["Billing Team"].channel`, root: "listeners", item: "Billing Team", want: "channel"},
		{name: "the whole rule with brackets", path: `listeners["Billing Team"]`, root: "listeners", item: "Billing Team", want: ""},
		{name: "a path of another rule", path: "routes.other.mode", root: "routes", item: "checkout", want: "routes.other.mode"},
		{name: "a name that is the start of another", path: "routes.checkout-2.mode", root: "routes", item: "checkout", want: "routes.checkout-2.mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, fieldOf(tt.path, tt.root, tt.item))
		})
	}
}

func TestRuleError(t *testing.T) {
	t.Parallel()

	problems := config.Errors{
		{Path: "routes.checkout.budgets.maxTokens", Message: "too high"},
		{Path: "routes.checkout", Message: `route name "checkout" is taken`},
		{Path: "routes.checkout", Message: "something about the whole rule"},
	}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantFields []problem.FieldError
	}{
		{
			name: "failed checks", err: fmt.Errorf("set route: %w", problems), wantStatus: http.StatusUnprocessableEntity, wantCode: codeValidation,
			wantFields: []problem.FieldError{
				{Field: "budgets.maxTokens", Message: "too high"},
				{Field: "name", Message: `route name "checkout" is taken`},
				{Field: "", Message: "something about the whole rule"},
			},
		},
		{name: "a file rule", err: fmt.Errorf("x: %w", rules.ErrFileRule), wantStatus: http.StatusConflict, wantCode: codeFileRule},
		{name: "a name in use", err: fmt.Errorf("x: %w", rules.ErrExists), wantStatus: http.StatusConflict, wantCode: codeExists},
		{name: "an old version", err: fmt.Errorf("x: %w", rules.ErrStale), wantStatus: http.StatusConflict, wantCode: codeStale},
		{name: "a change at the same moment", err: fmt.Errorf("x: %w", rules.ErrConflict), wantStatus: http.StatusConflict, wantCode: codeConflict},
		{name: "no such rule", err: fmt.Errorf("x: %w", rules.ErrNotFound), wantStatus: http.StatusNotFound, wantCode: codeNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := errors.AsType[*Error](ruleError(tt.err, rules.KindRoute, "checkout"))
			require.True(t, ok)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantCode, got.Code)
			require.Equal(t, tt.wantFields, got.Fields)
		})
	}

	t.Run("an error it does not know is a server error", func(t *testing.T) {
		t.Parallel()

		err := ruleError(errors.New("disk full"), rules.KindRoute, "checkout")

		_, ok := errors.AsType[*Error](err)
		require.False(t, ok)
		require.ErrorContains(t, err, "disk full")
	})
}

func TestRouteOf(t *testing.T) {
	t.Parallel()

	t.Run("fields left out keep the defaults of a file route", func(t *testing.T) {
		t.Parallel()

		got, err := routeOf("search", apigen.RouteSpec{})
		require.NoError(t, err)
		require.Equal(t, config.NewRoute("search"), got)
	})

	t.Run("every field", func(t *testing.T) {
		t.Parallel()

		mode := apigen.RouteMode("read-only")

		got, err := routeOf("search", apigen.RouteSpec{
			Priority: new(7),
			Match: &apigen.RouteMatch{
				Intake: &[]string{"am"}, Labels: &map[string]string{"team": "core"}, Service: new("search-*"),
				Severity: &[]string{"critical"}, Text: new("boom"),
			},
			Strategy: new("latency"),
			Sources:  &[]string{"signoz"},
			Outputs:  &[]string{"slack-thread"},
			Mode:     &mode,
			Budgets:  &apigen.RouteBudgets{MaxTokens: new(1000), MaxToolCalls: new(5), MaxWallTime: new("2m"), MaxCostUSD: new(1.5)},
			Grouping: &apigen.RouteGrouping{Window: new("10m"), FingerprintLabels: &[]string{"pod"}, RelatedWindow: new("5m")},
		})
		require.NoError(t, err)

		want := config.NewRoute("search")
		want.Priority = 7
		want.Match = &config.Match{
			Intake: []string{"am"}, Labels: map[string]string{"team": "core"}, Service: "search-*",
			Severity: []event.Severity{event.SeverityCritical}, Text: "boom",
		}
		want.Strategy = "latency"
		want.Sources = []string{"signoz"}
		want.Outputs = []string{"slack-thread"}
		want.Mode = action.ReadOnly
		want.Budgets.MaxTokens, want.Budgets.MaxToolCalls, want.Budgets.MaxWallTime, want.Budgets.MaxCostUSD = 1000, 5, 2*time.Minute, 1.5
		want.Grouping.Window, want.Grouping.FingerprintLabels, want.Grouping.RelatedWindow = 10*time.Minute, []string{"pod"}, 5*time.Minute

		require.Equal(t, want, got)
	})

	t.Run("a match that sets nothing is no match", func(t *testing.T) {
		t.Parallel()

		got, err := routeOf("search", apigen.RouteSpec{Match: &apigen.RouteMatch{Intake: &[]string{}}})
		require.NoError(t, err)
		require.Nil(t, got.Match)
	})

	t.Run("durations that do not parse are refused with their fields", func(t *testing.T) {
		t.Parallel()

		_, err := routeOf("search", apigen.RouteSpec{
			Budgets:  &apigen.RouteBudgets{MaxWallTime: new("5 minutes")},
			Grouping: &apigen.RouteGrouping{Window: new("soon"), RelatedWindow: new("0")},
		})

		got, ok := errors.AsType[*Error](err)
		require.True(t, ok)
		require.Equal(t, http.StatusUnprocessableEntity, got.Status)
		require.Equal(t, codeValidation, got.Code)
		require.Len(t, got.Fields, 2)
		require.Equal(t, "budgets.maxWallTime", got.Fields[0].Field)
		require.Equal(t, "grouping.window", got.Fields[1].Field)
	})
}

func TestListenerOfAndView(t *testing.T) {
	t.Parallel()

	spec := apigen.ListenerSpec{
		Source:   new("slack"),
		Channel:  "#billing",
		Extract:  &[]string{"(?P<service>[a-z]+)"},
		Recovery: new("resolved"),
		Match: &apigen.ListenerMatch{
			Text:   new("firing"),
			Sender: &apigen.ListenerSender{Kind: new("bot"), Names: &[]string{"Grafana"}, Bots: &[]string{"B01ABCDEF"}, Users: &[]string{"U01ABCDEF"}},
		},
	}

	l := listenerOf(spec)
	require.Equal(t, config.Listener{
		Source: config.ListenerSourceSlack, Channel: "#billing", Extract: []string{"(?P<service>[a-z]+)"}, Recovery: "resolved",
		Match: config.ListenerMatch{
			Text:   "firing",
			Sender: config.ListenerSender{Kind: event.SenderBot, Names: []string{"Grafana"}, Bots: []string{"B01ABCDEF"}, Users: []string{"U01ABCDEF"}},
		},
	}, l)

	view := listenerView("billing", l)
	require.Equal(t, apigen.Listener{
		Name: "billing", Channel: "#billing", Source: spec.Source, Extract: spec.Extract, Recovery: spec.Recovery, Match: spec.Match,
	}, view)

	t.Run("a listener that matches every message has no match", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, listenerView("all", config.Listener{Channel: "#all"}).Match)
	})
}

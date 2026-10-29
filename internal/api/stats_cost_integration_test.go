//go:build integration

package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/config"
)

// costBody is the response of GET /stats/cost.
type costBody struct {
	From           string      `json:"from"`
	To             string      `json:"to"`
	IgnoredFilters []string    `json:"ignoredFilters"`
	Totals         spendFigure `json:"totals"`
	Previous       spendFigure `json:"previous"`
	Days           []struct {
		Day string `json:"day"`
		spendFigure
	} `json:"days"`
	DayModels []struct {
		Day       string `json:"day"`
		Connector string `json:"connector"`
		Model     string `json:"model"`
		spendFigure
	} `json:"dayModels"`
	ByModel []struct {
		Connector string `json:"connector"`
		Model     string `json:"model"`
		spendFigure
	} `json:"byModel"`
	ByRoute []struct {
		Route string `json:"route"`
		spendFigure
	} `json:"byRoute"`
	PerInvestigation []struct {
		Day            string `json:"day"`
		Investigations int64  `json:"investigations"`
		CostMicros     int64  `json:"costMicros"`
	} `json:"perInvestigation"`
	PreviousPerInvestigation struct {
		Investigations int64 `json:"investigations"`
		CostMicros     int64 `json:"costMicros"`
	} `json:"previousPerInvestigation"`
	Today struct {
		Day         string `json:"day"`
		SpentMicros int64  `json:"spentMicros"`
		CapMicros   *int64 `json:"capMicros"`
		Routes      []struct {
			Route       string `json:"route"`
			SpentMicros int64  `json:"spentMicros"`
			CapMicros   *int64 `json:"capMicros"`
		} `json:"routes"`
	} `json:"today"`
}

// spendFigure are the cost and tokens of a row.
type spendFigure struct {
	CostMicros   int64 `json:"costMicros"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

func TestGetCostStats(t *testing.T) {
	t.Parallel()

	s := setupStats(t)

	t.Run("the default range", func(t *testing.T) {
		t.Parallel()

		var got costBody

		decodeStats(t, getStats(t, s, "/stats/cost"), &got)

		require.Equal(t, "2026-09-29", got.From)
		require.Equal(t, "2026-10-28", got.To)
		require.Empty(t, got.IgnoredFilters)
		require.Equal(t, spendFigure{106000, 44000, 22000}, got.Totals)
		require.Equal(t, spendFigure{10000, 5000, 2500}, got.Previous)

		type day struct {
			day string
			spendFigure
		}

		var days []day
		for _, d := range got.Days {
			days = append(days, day{d.Day, d.spendFigure})
		}

		require.Equal(t, []day{
			{"2026-10-20", spendFigure{25000, 12000, 6000}},
			{"2026-10-21", spendFigure{16000, 7000, 3500}},
			{"2026-10-28", spendFigure{65000, 25000, 12500}},
		}, days)

		type dayModel struct {
			day, connector, model string
			cost                  int64
		}

		var dayModels []dayModel
		for _, d := range got.DayModels {
			dayModels = append(dayModels, dayModel{d.Day, d.Connector, d.Model, d.CostMicros})
		}

		require.Equal(t, []dayModel{
			{"2026-10-20", "claude", "m1", 20000},
			{"2026-10-20", "claude", "m2", 5000},
			{"2026-10-21", "claude", "m1", 10000},
			{"2026-10-21", "openai", "m2", 6000},
			{"2026-10-28", "claude", "m1", 40000},
			{"2026-10-28", "claude", "m2", 25000},
		}, dayModels)

		require.Len(t, got.ByModel, 3)
		require.Equal(t, "claude", got.ByModel[0].Connector)
		require.Equal(t, "m1", got.ByModel[0].Model)
		require.Equal(t, spendFigure{70000, 30000, 15000}, got.ByModel[0].spendFigure)
		require.Equal(t, spendFigure{30000, 11000, 5500}, got.ByModel[1].spendFigure)
		require.Equal(t, "openai", got.ByModel[2].Connector)
		require.Equal(t, spendFigure{6000, 3000, 1500}, got.ByModel[2].spendFigure)

		require.Len(t, got.ByRoute, 2)
		require.Equal(t, "r1", got.ByRoute[0].Route)
		require.Equal(t, spendFigure{70000, 30000, 15000}, got.ByRoute[0].spendFigure)
		require.Equal(t, "r2", got.ByRoute[1].Route)
		require.Equal(t, spendFigure{36000, 14000, 7000}, got.ByRoute[1].spendFigure)

		require.Len(t, got.PerInvestigation, 3)
		require.Equal(t, "2026-10-20", got.PerInvestigation[0].Day)
		require.Equal(t, int64(14), got.PerInvestigation[0].Investigations)
		require.Equal(t, int64(40000), got.PerInvestigation[0].CostMicros)
		require.Equal(t, int64(8), got.PerInvestigation[1].Investigations)
		require.Equal(t, int64(7000), got.PerInvestigation[1].CostMicros)
		require.Equal(t, int64(1), got.PerInvestigation[2].Investigations)
		require.Equal(t, int64(300), got.PerInvestigation[2].CostMicros)
		require.Equal(t, int64(5), got.PreviousPerInvestigation.Investigations)
		require.Equal(t, int64(10000), got.PreviousPerInvestigation.CostMicros)

		// With no caps in the config, none is shown.
		require.Equal(t, "2026-10-28", got.Today.Day)
		require.Equal(t, int64(65000), got.Today.SpentMicros)
		require.Nil(t, got.Today.CapMicros)
		require.Len(t, got.Today.Routes, 2)
		require.Equal(t, int64(40000), got.Today.Routes[0].SpentMicros)
		require.Nil(t, got.Today.Routes[0].CapMicros)
	})

	t.Run("a route", func(t *testing.T) {
		t.Parallel()

		var got costBody

		decodeStats(t, getStats(t, s, "/stats/cost?route=r2"), &got)

		require.Equal(t, spendFigure{36000, 14000, 7000}, got.Totals)
		require.Len(t, got.ByRoute, 1)
		require.Len(t, got.PerInvestigation, 1)
		require.Equal(t, int64(8), got.PerInvestigation[0].Investigations)
		require.Equal(t, int64(7000), got.PerInvestigation[0].CostMicros)
		require.Equal(t, int64(0), got.PreviousPerInvestigation.Investigations)
	})

	t.Run("a model", func(t *testing.T) {
		t.Parallel()

		var got costBody

		decodeStats(t, getStats(t, s, "/stats/cost?model=m1"), &got)

		require.Equal(t, spendFigure{70000, 30000, 15000}, got.Totals)
		require.Len(t, got.ByModel, 1)
		require.Len(t, got.PerInvestigation, 1)
		require.Equal(t, int64(14), got.PerInvestigation[0].Investigations)
	})

	t.Run("a service and a verdict only filter the cost per investigation", func(t *testing.T) {
		t.Parallel()

		var got costBody

		decodeStats(t, getStats(t, s, "/stats/cost?service=checkout&verdict=false_positive"), &got)

		require.Equal(t, []string{"service", "verdict"}, got.IgnoredFilters)
		require.Equal(t, spendFigure{106000, 44000, 22000}, got.Totals)
		require.Len(t, got.PerInvestigation, 1)
		require.Equal(t, "2026-10-20", got.PerInvestigation[0].Day)
		require.Equal(t, int64(10), got.PerInvestigation[0].Investigations)
		require.Equal(t, int64(20000), got.PerInvestigation[0].CostMicros)
		require.Equal(t, int64(5), got.PreviousPerInvestigation.Investigations)
	})

	t.Run("a range before the spend", func(t *testing.T) {
		t.Parallel()

		rec := getStats(t, s, "/stats/cost?from=2026-01-01&to=2026-01-31")
		require.Equal(t, http.StatusOK, rec.Code)

		for _, empty := range []string{"days", "dayModels", "byModel", "byRoute", "perInvestigation"} {
			require.Contains(t, rec.Body.String(), `"`+empty+`":[]`)
		}
	})

	t.Run("the caps in the config", func(t *testing.T) {
		t.Parallel()

		capped := setupWith(t, func(o *api.Options) {
			o.Now = func() time.Time { return statsNow }

			cfg := *o.Config
			cfg.Budgets.Daily.MaxCostUSD = 0.1
			cfg.Routes = []config.Route{{Name: "r1", Budgets: config.RouteBudgets{Daily: config.RouteDaily{MaxCostUSD: 0.05}}}}
			o.Config = &cfg
		}, "fixture/stats.sql")

		var got costBody

		decodeStats(t, getStats(t, capped, "/stats/cost"), &got)

		require.Equal(t, int64(65000), got.Today.SpentMicros)
		require.Equal(t, new(int64(100000)), got.Today.CapMicros)
		require.Len(t, got.Today.Routes, 2)
		require.Equal(t, "r1", got.Today.Routes[0].Route)
		require.Equal(t, int64(40000), got.Today.Routes[0].SpentMicros)
		require.Equal(t, new(int64(50000)), got.Today.Routes[0].CapMicros)
		require.Equal(t, "r2", got.Today.Routes[1].Route)
		require.Nil(t, got.Today.Routes[1].CapMicros)
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/stats/cost", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	requireRefusals(t, s, "/stats/cost")
}

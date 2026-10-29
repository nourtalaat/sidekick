//go:build integration

package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The benchmark runs of fixture/bench.sql.
const (
	benchA = "0192d3a4-0000-7000-8000-0000000000a1"
	benchB = "0192d3a4-0000-7000-8000-0000000000a2"
)

// benchSummaryBody is a run in the list and in the comparison.
type benchSummaryBody struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Version    string `json:"sidekickVersion"`
	Status     string `json:"status"`
	Trigger    string `json:"triggeredBy"`
	Repeats    int    `json:"repeats"`
	Connectors map[string]struct {
		Connector string `json:"connector"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
	} `json:"connectors"`
	Strategies map[string]string `json:"strategies"`
	Metrics    struct {
		Runs            int      `json:"runs"`
		Overall         *float64 `json:"overall"`
		VerdictAccuracy *float64 `json:"verdictAccuracy"`
		FpPrecision     *float64 `json:"fpPrecision"`
		TpPrecision     *float64 `json:"tpPrecision"`
		TpRecall        *float64 `json:"tpRecall"`
		RootCauseMatch  *float64 `json:"rootCauseMatch"`
		MeanCost        *float64 `json:"meanCostPerRunUsd"`
		MeanDurationMs  *float64 `json:"meanDurationMs"`
		CostMicros      int64    `json:"costMicros"`
		JudgeErrors     int      `json:"judgeErrors"`
	} `json:"metrics"`
}

func setupBench(t *testing.T) server {
	t.Helper()

	return setupWith(t, nil, "fixture/bench.sql")
}

func TestListBenchRuns(t *testing.T) {
	t.Parallel()

	s := setupBench(t)

	t.Run("newest first", func(t *testing.T) {
		t.Parallel()

		var got struct {
			Runs       []benchSummaryBody `json:"runs"`
			NextCursor *string            `json:"nextCursor"`
		}

		decodeStats(t, getStats(t, s, "/bench/runs"), &got)

		require.Nil(t, got.NextCursor)
		require.Len(t, got.Runs, 2)

		b, a := got.Runs[0], got.Runs[1]

		require.Equal(t, benchB, b.ID)
		require.Empty(t, b.Label)
		require.Equal(t, "failed", b.Status)
		require.Equal(t, "ci", b.Trigger)
		require.Equal(t, "small-model", b.Connectors["investigate"].Model)
		require.Empty(t, b.Strategies)
		require.InDelta(t, 0.4, *b.Metrics.Overall, 1e-9)
		require.Nil(t, b.Metrics.FpPrecision, "a share with nothing to count is left out")
		require.Equal(t, 1, b.Metrics.JudgeErrors)

		require.Equal(t, benchA, a.ID)
		require.Equal(t, "baseline", a.Label)
		require.Equal(t, "v0.9.0", a.Version)
		require.Equal(t, "passed", a.Status)
		require.Equal(t, "cli", a.Trigger)
		require.Equal(t, 2, a.Repeats)
		require.Equal(t, "claude", a.Connectors["investigate"].Connector)
		require.Equal(t, "anthropic", a.Connectors["investigate"].Provider)
		require.Equal(t, "big-model", a.Connectors["investigate"].Model)
		require.Equal(t, map[string]string{"generic-alert": "v1"}, a.Strategies)
		require.Equal(t, 4, a.Metrics.Runs)
		require.InDelta(t, 0.7, *a.Metrics.Overall, 1e-9)
		require.InDelta(t, 0.75, *a.Metrics.VerdictAccuracy, 1e-9)
		require.InDelta(t, 0.5, *a.Metrics.TpRecall, 1e-9)
		require.Nil(t, a.Metrics.TpPrecision)
		require.Nil(t, a.Metrics.RootCauseMatch)
		require.InDelta(t, 0.002, *a.Metrics.MeanCost, 1e-9)
		require.InDelta(t, 1500, *a.Metrics.MeanDurationMs, 1e-9)
		require.Equal(t, int64(8000), a.Metrics.CostMicros)
	})

	t.Run("pages", func(t *testing.T) {
		t.Parallel()

		var first struct {
			Runs       []benchSummaryBody `json:"runs"`
			NextCursor *string            `json:"nextCursor"`
		}

		decodeStats(t, getStats(t, s, "/bench/runs?limit=1"), &first)

		require.Len(t, first.Runs, 1)
		require.Equal(t, benchB, first.Runs[0].ID)
		require.NotNil(t, first.NextCursor)

		var second struct {
			Runs       []benchSummaryBody `json:"runs"`
			NextCursor *string            `json:"nextCursor"`
		}

		decodeStats(t, getStats(t, s, "/bench/runs?limit=1&cursor="+url.QueryEscape(*first.NextCursor)), &second)

		require.Len(t, second.Runs, 1)
		require.Equal(t, benchA, second.Runs[0].ID)
		require.Nil(t, second.NextCursor)
	})

	refusals := []struct {
		name       string
		query      string
		wantDetail string
	}{
		{name: "a limit of 0", query: "?limit=0", wantDetail: "limit must be 1 to 200."},
		{name: "a limit over 200", query: "?limit=201", wantDetail: "limit must be 1 to 200."},
		{name: "a cursor that is not one", query: "?cursor=nonsense", wantDetail: "cursor is not one this API returned."},
	}

	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := getStats(t, s, "/bench/runs"+tt.query)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.JSONEq(t, problem(http.StatusBadRequest, tt.wantDetail, "/api/v1/bench/runs"), rec.Body.String())
		})
	}

	t.Run("no session", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, http.StatusUnauthorized, s.do(t, http.MethodGet, "/api/v1/bench/runs", "", "", nil).Code)
	})
}

func TestGetBenchRun(t *testing.T) {
	t.Parallel()

	s := setupBench(t)

	t.Run("a run with its results", func(t *testing.T) {
		t.Parallel()

		var got struct {
			benchSummaryBody
			ConfigHash         string `json:"configHash"`
			PromptVersion      string `json:"promptVersion"`
			JudgePromptVersion string `json:"judgePromptVersion"`
			Intervals          map[string]struct {
				Lower float64 `json:"lower"`
				Upper float64 `json:"upper"`
			} `json:"intervals"`
			Thresholds []struct {
				Name   string   `json:"name"`
				Limit  float64  `json:"limit"`
				Value  *float64 `json:"value"`
				Passed bool     `json:"passed"`
				Detail string   `json:"detail"`
			} `json:"thresholds"`
			Results []struct {
				ScenarioID      string   `json:"scenarioId"`
				Repeat          int      `json:"repeat"`
				Verdict         *string  `json:"verdict"`
				ExpectedVerdict string   `json:"expectedVerdict"`
				VerdictCorrect  bool     `json:"verdictCorrect"`
				RootCauseScore  *float64 `json:"rootCauseScore"`
				RequiredMet     *bool    `json:"requiredMet"`
				EvidenceScore   *float64 `json:"evidenceScore"`
				CostMicros      int64    `json:"costMicros"`
				DurationMs      int64    `json:"durationMs"`
				ToolCalls       int      `json:"toolCalls"`
			} `json:"results"`
		}

		rec := getStats(t, s, "/bench/runs/"+benchA)
		decodeStats(t, rec, &got)

		require.NotContains(t, rec.Body.String(), "secret judge text", "the judge's reasons are model output and are left out")

		require.Equal(t, benchA, got.ID)
		require.Equal(t, "baseline", got.Label)
		require.Equal(t, "cfg1", got.ConfigHash)
		require.Equal(t, "p1", got.PromptVersion)
		require.Equal(t, "j1", got.JudgePromptVersion)
		require.InDelta(t, 0.5, got.Intervals["overall"].Lower, 1e-9)
		require.InDelta(t, 0.9, got.Intervals["overall"].Upper, 1e-9)

		require.Len(t, got.Thresholds, 1)
		require.Equal(t, "overall", got.Thresholds[0].Name)
		require.InDelta(t, 0.6, got.Thresholds[0].Limit, 1e-9)
		require.InDelta(t, 0.7, *got.Thresholds[0].Value, 1e-9)
		require.True(t, got.Thresholds[0].Passed)

		require.Len(t, got.Results, 4)

		r := got.Results[0]
		require.Equal(t, "s1", r.ScenarioID)
		require.Equal(t, 1, r.Repeat)
		require.Equal(t, "false_positive", *r.Verdict)
		require.Equal(t, "false_positive", r.ExpectedVerdict)
		require.True(t, r.VerdictCorrect)
		require.InDelta(t, 0.9, *r.RootCauseScore, 1e-6)
		require.True(t, *r.RequiredMet)
		require.InDelta(t, 1.0, *r.EvidenceScore, 1e-6)
		require.Equal(t, int64(2000), r.CostMicros)
		require.Equal(t, int64(1200), r.DurationMs)
		require.Equal(t, 3, r.ToolCalls)

		// The ungraded run has no root cause score, and the order is by scenario and repeat.
		last := got.Results[3]
		require.Equal(t, "s2", last.ScenarioID)
		require.Equal(t, 2, last.Repeat)
		require.False(t, last.VerdictCorrect)
		require.Nil(t, last.RootCauseScore)
		require.Nil(t, last.RequiredMet)
	})

	t.Run("a failed threshold", func(t *testing.T) {
		t.Parallel()

		var got struct {
			Thresholds []struct {
				Passed bool   `json:"passed"`
				Detail string `json:"detail"`
			} `json:"thresholds"`
		}

		decodeStats(t, getStats(t, s, "/bench/runs/"+benchB), &got)

		require.Len(t, got.Thresholds, 1)
		require.False(t, got.Thresholds[0].Passed)
		require.Equal(t, "below the limit", got.Thresholds[0].Detail)
	})

	t.Run("a run that does not exist", func(t *testing.T) {
		t.Parallel()

		rec := getStats(t, s, "/bench/runs/0192d3a4-0000-7000-8000-0000000000ff")
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.JSONEq(t,
			problem(http.StatusNotFound, "No benchmark run has this ID.", "/api/v1/bench/runs/0192d3a4-0000-7000-8000-0000000000ff"),
			rec.Body.String())
	})

	t.Run("an ID that is not a UUID", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, http.StatusBadRequest, getStats(t, s, "/bench/runs/nope").Code)
	})
}

func TestCompareBenchRuns(t *testing.T) {
	t.Parallel()

	s := setupBench(t)

	t.Run("two runs", func(t *testing.T) {
		t.Parallel()

		var got struct {
			A       benchSummaryBody `json:"a"`
			B       benchSummaryBody `json:"b"`
			Metrics []struct {
				Key        string   `json:"key"`
				Label      string   `json:"label"`
				A          *float64 `json:"a"`
				B          *float64 `json:"b"`
				Change     *float64 `json:"change"`
				Difference string   `json:"difference"`
			} `json:"metrics"`
			Scenarios []struct {
				ScenarioID      string `json:"scenarioId"`
				ExpectedVerdict string `json:"expectedVerdict"`
				A               *struct {
					Runs          int      `json:"runs"`
					Correct       int      `json:"correct"`
					RootCauseMean *float64 `json:"rootCauseMean"`
				} `json:"a"`
				B *struct {
					Runs          int      `json:"runs"`
					Correct       int      `json:"correct"`
					RootCauseMean *float64 `json:"rootCauseMean"`
				} `json:"b"`
			} `json:"scenarios"`
		}

		decodeStats(t, getStats(t, s, "/bench/compare?a="+benchA+"&b="+benchB), &got)

		require.Equal(t, benchA, got.A.ID)
		require.Equal(t, benchB, got.B.ID)

		rows := map[string]int{}
		for i, m := range got.Metrics {
			rows[m.Key] = i
		}

		overall := got.Metrics[rows["overall"]]
		require.Equal(t, "overall score", overall.Label)
		require.InDelta(t, 0.7, *overall.A, 1e-9)
		require.InDelta(t, 0.4, *overall.B, 1e-9)
		require.InDelta(t, -0.3, *overall.Change, 1e-9)
		require.Equal(t, "clear", overall.Difference, "the intervals 0.5 to 0.9 and 0.2 to 0.45 are apart")

		accuracy := got.Metrics[rows["verdictAccuracy"]]
		require.InDelta(t, -0.25, *accuracy.Change, 1e-9)
		require.Equal(t, "none", accuracy.Difference, "the intervals 0.4 to 1 and 0.2 to 0.8 overlap")

		recall := got.Metrics[rows["tpRecall"]]
		require.NotNil(t, recall.A)
		require.Nil(t, recall.B)
		require.Nil(t, recall.Change)
		require.Equal(t, "unknown", recall.Difference)

		cost := got.Metrics[rows["meanCostPerRunUSD"]]
		require.InDelta(t, 0.002, *cost.Change, 1e-9)
		require.Equal(t, "unknown", cost.Difference)

		duration := got.Metrics[rows["meanDurationMs"]]
		require.InDelta(t, 1000, *duration.Change, 1e-9)

		require.Len(t, got.Scenarios, 3)

		s1, s2, s3 := got.Scenarios[0], got.Scenarios[1], got.Scenarios[2]

		require.Equal(t, "s1", s1.ScenarioID)
		require.Equal(t, "false_positive", s1.ExpectedVerdict)
		require.Equal(t, 2, s1.A.Runs)
		require.Equal(t, 2, s1.A.Correct)
		require.InDelta(t, 0.85, *s1.A.RootCauseMean, 1e-6)
		require.Equal(t, 1, s1.B.Runs)
		require.Equal(t, 0, s1.B.Correct)
		require.InDelta(t, 0.2, *s1.B.RootCauseMean, 1e-6)

		require.Equal(t, "s2", s2.ScenarioID)
		require.Equal(t, 2, s2.A.Runs)
		require.Equal(t, 1, s2.A.Correct)
		require.InDelta(t, 0.5, *s2.A.RootCauseMean, 1e-6)
		require.Nil(t, s2.B, "run b lacks the scenario")

		require.Equal(t, "s3", s3.ScenarioID)
		require.Nil(t, s3.A, "run a lacks the scenario")
		require.Equal(t, 1, s3.B.Correct)
	})

	t.Run("a run against itself", func(t *testing.T) {
		t.Parallel()

		var got struct {
			Metrics []struct {
				Key        string   `json:"key"`
				Change     *float64 `json:"change"`
				Difference string   `json:"difference"`
			} `json:"metrics"`
		}

		decodeStats(t, getStats(t, s, "/bench/compare?a="+benchA+"&b="+benchA), &got)

		for _, m := range got.Metrics {
			if m.Change != nil {
				require.InDelta(t, 0, *m.Change, 1e-9, m.Key)
			}

			require.NotEqual(t, "clear", m.Difference, m.Key)
		}
	})

	refusals := []struct {
		name       string
		query      string
		wantStatus int
		wantDetail string
	}{
		{name: "a run that does not exist", query: "?a=" + benchA + "&b=0192d3a4-0000-7000-8000-0000000000ff", wantStatus: http.StatusNotFound, wantDetail: "No benchmark run has this ID."},
		{name: "the first run does not exist", query: "?a=0192d3a4-0000-7000-8000-0000000000ff&b=" + benchB, wantStatus: http.StatusNotFound, wantDetail: "No benchmark run has this ID."},
		{name: "a missing parameter", query: "?a=" + benchA, wantStatus: http.StatusBadRequest},
		{name: "an ID that is not a UUID", query: "?a=x&b=y", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := getStats(t, s, "/bench/compare"+tt.query)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantDetail != "" {
				require.True(t, strings.Contains(rec.Body.String(), tt.wantDetail), rec.Body.String())
			}
		})
	}
}

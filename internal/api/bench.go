package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The page sizes of GET /bench/runs.
const (
	defaultBenchPage = 50
	maxBenchPage     = 200
)

// errNoBenchRun answers a request for a benchmark run that does not exist.
var errNoBenchRun = problemf(http.StatusNotFound, "No benchmark run has this ID.")

// ListBenchRuns returns a page of benchmark runs, newest first.
func (s *server) ListBenchRuns(ctx context.Context, req apigen.ListBenchRunsRequestObject) (apigen.ListBenchRunsResponseObject, error) {
	params, limit, err := benchParams(req.Params)
	if err != nil {
		return nil, err
	}

	rows, err := db.New(customPlan{db: s.pool}).ListBenchRuns(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list benchmark runs: %w", err)
	}

	list := apigen.ListBenchRuns200JSONResponse{Runs: make([]apigen.BenchRunSummary, 0, min(len(rows), limit))}

	for i, row := range rows {
		if i == limit {
			cursor := investigationCursor{createdAt: rows[i-1].StartedAt, id: rows[i-1].ID}.String()
			list.NextCursor = &cursor

			break
		}

		run, err := benchSummaryOf(benchRunFields{
			id: row.ID, label: row.Label, startedAt: row.StartedAt, finishedAt: row.FinishedAt, version: row.SidekickVersion,
			sha: row.GitSha, connectors: row.Connectors, strategies: row.Strategies, scenarioSet: row.ScenarioSet,
			repeats: row.Repeats, status: row.Status, triggeredBy: row.TriggeredBy, metrics: row.Metrics,
		})
		if err != nil {
			return nil, fmt.Errorf("benchmark run %s: %w", row.ID, err)
		}

		list.Runs = append(list.Runs, run)
	}

	return list, nil
}

// GetBenchRun returns one benchmark run with its summary and results.
func (s *server) GetBenchRun(ctx context.Context, req apigen.GetBenchRunRequestObject) (apigen.GetBenchRunResponseObject, error) {
	q := db.New(s.pool)

	run, summary, err := loadBenchRun(ctx, q, req.Id)
	if err != nil {
		return nil, err
	}

	results, err := q.ListBenchResults(ctx, req.Id)
	if err != nil {
		return nil, fmt.Errorf("list the results of benchmark run %s: %w", req.Id, err)
	}

	out := apigen.BenchRun{
		Id: run.Id, Label: run.Label, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, SidekickVersion: run.SidekickVersion,
		GitSha: run.GitSha, Connectors: run.Connectors, Strategies: run.Strategies, ScenarioSet: run.ScenarioSet, Repeats: run.Repeats,
		Status: run.Status, TriggeredBy: run.TriggeredBy, Metrics: run.Metrics,
		ConfigHash: summary.configHash, PromptVersion: summary.PromptVersion, JudgePromptVersion: summary.JudgePromptVersion,
		Intervals: map[string]apigen.BenchInterval{}, Thresholds: make([]apigen.BenchThreshold, 0, len(summary.Thresholds)),
		Results: make([]apigen.BenchResult, 0, len(results)),
	}

	for key, i := range summary.Intervals {
		out.Intervals[key] = apigen.BenchInterval{Lower: i.Lower, Upper: i.Upper}
	}

	for _, t := range summary.Thresholds {
		out.Thresholds = append(out.Thresholds, apigen.BenchThreshold{
			Name: t.Name, Limit: t.Limit, Value: t.Value, Passed: t.Passed, Detail: nonEmptyText(t.Detail),
		})
	}

	for _, r := range results {
		out.Results = append(out.Results, benchResultOf(r))
	}

	return apigen.GetBenchRun200JSONResponse(out), nil
}

// CompareBenchRuns returns the metrics and the scenarios of two benchmark
// runs side by side.
func (s *server) CompareBenchRuns(ctx context.Context, req apigen.CompareBenchRunsRequestObject) (apigen.CompareBenchRunsResponseObject, error) {
	q := db.New(s.pool)

	a, sumA, err := loadBenchRun(ctx, q, req.Params.A)
	if err != nil {
		return nil, err
	}

	b, sumB, err := loadBenchRun(ctx, q, req.Params.B)
	if err != nil {
		return nil, err
	}

	scenariosA, err := q.SummarizeBenchScenarios(ctx, req.Params.A)
	if err != nil {
		return nil, fmt.Errorf("summarize the scenarios of benchmark run %s: %w", req.Params.A, err)
	}

	scenariosB, err := q.SummarizeBenchScenarios(ctx, req.Params.B)
	if err != nil {
		return nil, fmt.Errorf("summarize the scenarios of benchmark run %s: %w", req.Params.B, err)
	}

	rows := bench.CompareMetrics(sumA.StoredSummary, sumB.StoredSummary)
	out := apigen.CompareBenchRuns200JSONResponse{A: a, B: b, Metrics: make([]apigen.BenchMetricRow, 0, len(rows)), Scenarios: scenarioRows(scenariosA, scenariosB)}

	for _, r := range rows {
		out.Metrics = append(out.Metrics, apigen.BenchMetricRow{
			Key: r.Key, Label: r.Label, A: r.A, B: r.B, Change: r.Change, Difference: string(r.Difference),
		})
	}

	return out, nil
}

// benchRunFields are the columns of a benchmark run that its summary in the
// API shows.
type benchRunFields struct {
	id                              uuid.UUID
	label                           *string
	startedAt, finishedAt           time.Time
	version, sha, scenarioSet       string
	connectors, strategies, metrics json.RawMessage
	repeats                         int32
	status, triggeredBy             string
}

// benchSummary is a stored summary with the config hash of its run.
type benchSummary struct {
	bench.StoredSummary
	configHash string
}

// benchParams checks the paging parameters of GET /bench/runs and returns
// the query's parameters, which ask for one row more than the page size,
// and the page size.
func benchParams(p apigen.ListBenchRunsParams) (db.ListBenchRunsParams, int, error) {
	limit := defaultBenchPage
	if p.Limit != nil {
		limit = *p.Limit
	}

	if limit < 1 || limit > maxBenchPage {
		return db.ListBenchRunsParams{}, 0, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxBenchPage)
	}

	params := db.ListBenchRunsParams{PageSize: int32(limit + 1)} //nolint:gosec // limit is at most maxBenchPage.

	if p.Cursor != nil {
		c, err := parseInvestigationCursor(*p.Cursor)
		if err != nil {
			return db.ListBenchRunsParams{}, 0, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		params.CursorAt, params.CursorID = &c.createdAt, &c.id
	}

	return params, limit, nil
}

// loadBenchRun reads a benchmark run and its summary, or returns
// errNoBenchRun.
func loadBenchRun(ctx context.Context, q *db.Queries, id uuid.UUID) (apigen.BenchRunSummary, benchSummary, error) {
	row, err := q.GetBenchRun(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return apigen.BenchRunSummary{}, benchSummary{}, errNoBenchRun
	}

	if err != nil {
		return apigen.BenchRunSummary{}, benchSummary{}, fmt.Errorf("read benchmark run %s: %w", id, err)
	}

	stored, err := bench.ParseStoredSummary(row.Summary)
	if err != nil {
		return apigen.BenchRunSummary{}, benchSummary{}, fmt.Errorf("benchmark run %s: %w", id, err)
	}

	metrics, err := json.Marshal(stored.Metrics)
	if err != nil {
		return apigen.BenchRunSummary{}, benchSummary{}, fmt.Errorf("benchmark run %s: encode the metrics: %w", id, err)
	}

	run, err := benchSummaryOf(benchRunFields{
		id: row.ID, label: row.Label, startedAt: row.StartedAt, finishedAt: row.FinishedAt, version: row.SidekickVersion,
		sha: row.GitSha, connectors: row.Connectors, strategies: row.Strategies, scenarioSet: row.ScenarioSet,
		repeats: row.Repeats, status: row.Status, triggeredBy: row.TriggeredBy, metrics: metrics,
	})
	if err != nil {
		return apigen.BenchRunSummary{}, benchSummary{}, fmt.Errorf("benchmark run %s: %w", id, err)
	}

	return run, benchSummary{StoredSummary: stored, configHash: row.ConfigHash}, nil
}

// benchSummaryOf is the API's view of a benchmark run's settings and
// metrics.
func benchSummaryOf(f benchRunFields) (apigen.BenchRunSummary, error) {
	var (
		connectors map[string]bench.ConnectorInfo
		strategies map[string]string
		metrics    bench.Metrics
	)

	if err := json.Unmarshal(f.connectors, &connectors); err != nil {
		return apigen.BenchRunSummary{}, fmt.Errorf("decode the connectors: %w", err)
	}

	if err := json.Unmarshal(f.strategies, &strategies); err != nil {
		return apigen.BenchRunSummary{}, fmt.Errorf("decode the strategies: %w", err)
	}

	if err := json.Unmarshal(f.metrics, &metrics); err != nil {
		return apigen.BenchRunSummary{}, fmt.Errorf("decode the metrics: %w", err)
	}

	out := apigen.BenchRunSummary{
		Id: f.id, Label: f.label, StartedAt: f.startedAt, FinishedAt: f.finishedAt, SidekickVersion: f.version, GitSha: f.sha,
		Connectors: make(map[string]apigen.BenchConnector, len(connectors)), Strategies: orEmptyMap(strategies),
		ScenarioSet: f.scenarioSet, Repeats: int(f.repeats), Status: f.status, TriggeredBy: f.triggeredBy,
		Metrics: benchMetricsOf(metrics),
	}

	for role, c := range connectors {
		out.Connectors[role] = apigen.BenchConnector{Connector: c.Connector, Provider: nonEmptyText(c.Provider), Model: nonEmptyText(c.Model)}
	}

	return out, nil
}

// orEmptyMap returns m, or an empty map for nil, so JSON shows {}.
func orEmptyMap[V any](m map[string]V) map[string]V {
	if m == nil {
		return map[string]V{}
	}

	return m
}

// benchMetricsOf is the API's view of the metrics of a run.
func benchMetricsOf(m bench.Metrics) apigen.BenchMetrics {
	return apigen.BenchMetrics{
		Runs: m.Runs, Overall: m.Overall, VerdictAccuracy: m.VerdictAccuracy,
		FpPrecision: m.FalsePositive.Precision, FpRecall: m.FalsePositive.Recall,
		TpPrecision: m.TruePositive.Precision, TpRecall: m.TruePositive.Recall,
		RootCauseMatch: m.RootCauseMatch, RootCauseScore: m.RootCauseScore, Evidence: m.Evidence,
		SourceFailuresReported: m.SourceFailuresReported, MeanCostPerRunUsd: m.MeanCostPerRunUSD, MeanDurationMs: m.MeanDurationMs,
		ToolCalls: m.ToolCalls, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
		CostMicros: m.CostMicros, JudgeCostMicros: m.JudgeCostMicros, JudgeErrors: m.JudgeErrors,
	}
}

// benchResultOf is the API's view of the result of one run of a scenario.
func benchResultOf(r db.ListBenchResultsRow) apigen.BenchResult {
	return apigen.BenchResult{
		ScenarioId: r.ScenarioID, Repeat: int(r.Repeat), Verdict: r.Verdict, ExpectedVerdict: r.ExpectedVerdict,
		VerdictCorrect: r.VerdictCorrect, RootCauseScore: float64Of(r.RootCauseScore), RequiredMet: r.RequiredMet,
		EvidenceScore: float64Of(r.EvidenceScore), CostMicros: r.CostMicros, DurationMs: int64(r.DurationMs),
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, ToolCalls: int(r.ToolCalls),
	}
}

// float64Of widens a score, or returns nil for none.
func float64Of(v *float32) *float64 {
	if v == nil {
		return nil
	}

	return new(float64(*v))
}

// scenarioRows lines up the scenarios of two runs: those of a in their
// order, then those only b has.
func scenarioRows(a, b []db.SummarizeBenchScenariosRow) []apigen.BenchScenarioRow {
	inB := make(map[string]db.SummarizeBenchScenariosRow, len(b))
	for _, r := range b {
		inB[r.ScenarioID] = r
	}

	seen := make(map[string]bool, len(a))
	out := make([]apigen.BenchScenarioRow, 0, len(a)+len(b))

	for _, r := range a {
		seen[r.ScenarioID] = true

		row := apigen.BenchScenarioRow{ScenarioId: r.ScenarioID, ExpectedVerdict: r.ExpectedVerdict, A: scenarioCounts(r)}
		if other, ok := inB[r.ScenarioID]; ok {
			row.B = scenarioCounts(other)
		}

		out = append(out, row)
	}

	for _, r := range b {
		if !seen[r.ScenarioID] {
			out = append(out, apigen.BenchScenarioRow{ScenarioId: r.ScenarioID, ExpectedVerdict: r.ExpectedVerdict, B: scenarioCounts(r)})
		}
	}

	return out
}

// scenarioCounts is the API's view of a scenario's runs in one benchmark
// run.
func scenarioCounts(r db.SummarizeBenchScenariosRow) *apigen.BenchScenarioCounts {
	out := &apigen.BenchScenarioCounts{Runs: int(r.Runs), Correct: int(r.Correct)}

	if r.Graded > 0 {
		out.RootCauseMean = new(r.RootCauseSum / float64(r.Graded))
	}

	return out
}

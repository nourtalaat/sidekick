package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// resultStoreConns is the size of the pool that writes the results: one
// transaction at the end of the runs.
const resultStoreConns = 1

// Statuses of a stored run.
const (
	statusPassed = "passed"
	statusFailed = "failed"
)

// ErrSchemaNotCurrent is returned when the database a --db run stores its
// results in lacks migrations this Sidekick has.
var ErrSchemaNotCurrent = errors.New("the database's schema is older than this Sidekick; run sidekick migrate first")

// ResultStore writes benchmark results into bench_runs and bench_results
// of a database's default schema, where serve and the web UI read them. Its
// pool is its own, apart from the pool the runs use in their schema, so no
// run can touch the default schema's tables. Close it when done.
type ResultStore struct {
	pool *pgxpool.Pool
}

// storedSummary is bench_runs.summary: what report.json holds besides the
// run's settings and its scenarios.
type storedSummary struct {
	SchemaVersion      int                 `json:"schemaVersion"`
	PromptVersion      string              `json:"promptVersion"`
	JudgePromptVersion string              `json:"judgePromptVersion"`
	Seed               uint64              `json:"seed"`
	Resamples          int                 `json:"resamples"`
	ConfidenceLevel    float64             `json:"confidenceLevel"`
	Weights            Weights             `json:"weights"`
	Metrics            Metrics             `json:"metrics"`
	Intervals          map[string]Interval `json:"intervals"`
	Thresholds         []ThresholdResult   `json:"thresholds"`
}

// OpenResultStore connects to the default schema of the database at url
// and checks that it has every migration this Sidekick has. It never
// migrates it: that is for `sidekick migrate`. A schema that is not
// current gives an error that wraps ErrSchemaNotCurrent; a database that
// cannot be reached one that wraps ErrInfrastructure.
func OpenResultStore(ctx context.Context, url string) (*ResultStore, error) {
	pool, err := store.Open(ctx, url, resultStoreConns)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to the database for the results: %w", ErrInfrastructure, err)
	}

	current, target, err := store.SchemaVersion(ctx, pool)
	if err != nil {
		pool.Close()

		return nil, fmt.Errorf("%w: read the database's schema version: %w", ErrInfrastructure, err)
	}

	if current < target {
		pool.Close()

		return nil, fmt.Errorf("%w (it is at version %d, this Sidekick needs %d)", ErrSchemaNotCurrent, current, target)
	}

	return &ResultStore{pool: pool}, nil
}

// Save writes rep as one bench_runs row and one bench_results row per run,
// in one transaction, and returns the run's ID. A database failure gives an
// error that wraps ErrInfrastructure.
func (s *ResultStore) Save(ctx context.Context, rep *JSONReport) (uuid.UUID, error) {
	run, err := runRow(rep)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store the results: %w", err)
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		if err := q.InsertBenchRun(ctx, run); err != nil {
			return fmt.Errorf("insert the run: %w", err)
		}

		for _, sc := range rep.Scenarios {
			for _, r := range sc.Runs {
				if err := q.InsertBenchResult(ctx, resultRow(run.ID, sc, r)); err != nil {
					return fmt.Errorf("insert the result of %s run %d: %w", sc.ID, r.Run, err)
				}
			}
		}

		return nil
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: store the results: %w", ErrInfrastructure, err)
	}

	return run.ID, nil
}

// Close closes the store's pool.
func (s *ResultStore) Close() {
	s.pool.Close()
}

// runRow returns the bench_runs row of rep.
func runRow(rep *JSONReport) (db.InsertBenchRunParams, error) {
	connectors, err := json.Marshal(rep.Connectors)
	if err != nil {
		return db.InsertBenchRunParams{}, fmt.Errorf("encode the connectors: %w", err)
	}

	strategies, err := json.Marshal(rep.Strategies)
	if err != nil {
		return db.InsertBenchRunParams{}, fmt.Errorf("encode the strategies: %w", err)
	}

	summary, err := json.Marshal(storedSummary{
		SchemaVersion:      rep.SchemaVersion,
		PromptVersion:      rep.PromptVersion,
		JudgePromptVersion: rep.JudgePromptVersion,
		Seed:               rep.Seed,
		Resamples:          rep.Resamples,
		ConfidenceLevel:    rep.ConfidenceLevel,
		Weights:            rep.Weights,
		Metrics:            rep.Metrics,
		Intervals:          rep.Intervals,
		Thresholds:         rep.Thresholds,
	})
	if err != nil {
		return db.InsertBenchRunParams{}, fmt.Errorf("encode the summary: %w", err)
	}

	status := statusPassed
	if len(Failed(rep.Thresholds)) > 0 {
		status = statusFailed
	}

	var label *string
	if rep.Label != "" {
		label = &rep.Label
	}

	return db.InsertBenchRunParams{
		ID:              ids.New(),
		Label:           label,
		StartedAt:       rep.StartedAt,
		FinishedAt:      rep.FinishedAt,
		SidekickVersion: rep.SidekickVersion,
		GitSha:          rep.GitSHA,
		ConfigHash:      rep.ConfigHash,
		Connectors:      connectors,
		Strategies:      strategies,
		ScenarioSet:     rep.ScenarioSet,
		Repeats:         clampInt32(rep.Repeat),
		Summary:         summary,
		Status:          status,
		TriggeredBy:     rep.TriggeredBy,
	}, nil
}

// resultRow returns the bench_results row of run r of scenario sc.
func resultRow(runID uuid.UUID, sc ScenarioReport, r RunReport) db.InsertBenchResultParams {
	row := db.InsertBenchResultParams{
		RunID:           runID,
		ScenarioID:      sc.ID,
		Repeat:          clampInt32(r.Run),
		ExpectedVerdict: string(sc.ExpectedVerdict),
		VerdictCorrect:  r.VerdictCorrect,
		EvidenceScore:   float32Of(r.EvidenceScore),
		CostMicros:      r.CostMicros,
		DurationMs:      clampInt32(r.DurationMs),
		InputTokens:     r.InputTokens,
		OutputTokens:    r.OutputTokens,
		ToolCalls:       clampInt32(r.ToolCalls),
	}

	if r.Verdict != "" {
		verdict := string(r.Verdict)
		row.Verdict = &verdict
	}

	if rc := r.RootCause; rc != nil {
		score := float32(rc.Score)
		reason := judgeReason(rc)
		row.RootCauseScore = &score
		row.RequiredMet = &rc.RequiredMet
		row.JudgeReason = &reason
	}

	return row
}

// judgeReason is bench_results.judge_reason: one line per rubric item, as
// "<id>: met|not met: <reason>", or why the judge gave no grades.
func judgeReason(rc *RootCauseReport) string {
	if rc.Error != "" {
		return "no grades: " + rc.Error
	}

	lines := make([]string, len(rc.Items))

	for i, item := range rc.Items {
		met := "not met"
		if item.Met {
			met = "met"
		}

		lines[i] = fmt.Sprintf("%s: %s: %s", item.ID, met, item.Reason)
	}

	return strings.Join(lines, "\n")
}

// float32Of returns v as a float32, or nil when v is nil.
func float32Of(v *float64) *float32 {
	if v == nil {
		return nil
	}

	f := float32(*v)

	return &f
}

// clampInt32 returns n, capped to the range of an int32 column.
func clampInt32[T int | int64](n T) int32 {
	return int32(min(max(n, math.MinInt32), math.MaxInt32)) //nolint:gosec // n is clamped to the int32 range first.
}

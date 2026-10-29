package api

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// confirmedUnknown is the confirmed value of an investigation people rated
// wrong without giving the real verdict.
const confirmedUnknown = "unknown"

// GetQualityStats returns the accuracy of the verdicts over the range, the
// confusion matrix, precision and recall of each verdict, and the accuracy
// by model and strategy, from the daily rollup.
func (s *server) GetQualityStats(ctx context.Context, req apigen.GetQualityStatsRequestObject) (apigen.GetQualityStatsResponseObject, error) {
	p := req.Params

	w, err := s.statsWindowOf(p.From, p.To)
	if err != nil {
		return nil, err
	}

	f, err := statsFiltersOf(p.Route, p.Service, p.Model, p.Verdict)
	if err != nil {
		return nil, err
	}

	q := db.New(customPlan{db: s.pool})

	totals, err := qualityTotals(ctx, q, w, f)
	if err != nil {
		return nil, fmt.Errorf("total the range: %w", err)
	}

	previous, err := qualityTotals(ctx, q, w.before(), f)
	if err != nil {
		return nil, fmt.Errorf("total the range before: %w", err)
	}

	days, err := qualityDays(ctx, q, w, f)
	if err != nil {
		return nil, fmt.Errorf("list the days: %w", err)
	}

	matrix, err := qualityMatrix(ctx, q, w, f)
	if err != nil {
		return nil, fmt.Errorf("read the matrix: %w", err)
	}

	comparison, err := qualityComparison(ctx, q, w, f)
	if err != nil {
		return nil, fmt.Errorf("compare models and strategies: %w", err)
	}

	return apigen.GetQualityStats200JSONResponse{
		From:       dateOf(w.from),
		To:         dateOf(w.to),
		Totals:     totals,
		Previous:   previous,
		Days:       days,
		Matrix:     matrix,
		PerVerdict: verdictScores(matrix),
		Comparison: comparison,
	}, nil
}

func qualityTotals(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) (apigen.QualityTotals, error) {
	from, to := w.pgDays()

	row, err := q.QualityTotals(ctx, db.QualityTotalsParams{
		FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return apigen.QualityTotals{}, fmt.Errorf("read totals: %w", err)
	}

	return apigen.QualityTotals{Rated: row.Rated, Correct: row.Correct, Accuracy: share(row.Correct, row.Rated)}, nil
}

func qualityDays(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) ([]apigen.QualityDay, error) {
	from, to := w.pgDays()

	rows, err := q.QualityByDay(ctx, db.QualityByDayParams{
		FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, fmt.Errorf("read days: %w", err)
	}

	out := make([]apigen.QualityDay, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.QualityDay{Day: dateOf(r.Day.Time), Rated: r.Rated, Correct: r.Correct})
	}

	return out, nil
}

func qualityMatrix(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) ([]apigen.ConfusionCell, error) {
	from, to := w.pgDays()

	rows, err := q.QualityMatrix(ctx, db.QualityMatrixParams{
		FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, fmt.Errorf("read cells: %w", err)
	}

	out := make([]apigen.ConfusionCell, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.ConfusionCell{Predicted: apigen.Verdict(r.Predicted), Confirmed: r.Confirmed, N: r.N})
	}

	return out, nil
}

func qualityComparison(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) ([]apigen.QualityComparison, error) {
	from, to := w.pgDays()

	rows, err := q.QualityComparison(ctx, db.QualityComparisonParams{
		FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}

	out := make([]apigen.QualityComparison, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.QualityComparison{Model: r.Model, Strategy: r.Strategy, Rated: r.Rated, Correct: r.Correct})
	}

	return out, nil
}

// verdictScores works out precision and recall of each verdict from the
// confusion matrix. Cells whose confirmed verdict is unknown count for
// neither: they are not known to be hits or misses of any verdict.
func verdictScores(matrix []apigen.ConfusionCell) []apigen.VerdictScore {
	predicted := map[apigen.Verdict]int64{}
	actual := map[apigen.Verdict]int64{}
	hits := map[apigen.Verdict]int64{}

	for _, c := range matrix {
		if c.Confirmed == confirmedUnknown {
			continue
		}

		predicted[c.Predicted] += c.N
		actual[apigen.Verdict(c.Confirmed)] += c.N

		if string(c.Predicted) == c.Confirmed {
			hits[c.Predicted] += c.N
		}
	}

	out := []apigen.VerdictScore{}

	for _, v := range report.Verdicts {
		verdict := apigen.Verdict(v)
		if predicted[verdict] == 0 && actual[verdict] == 0 {
			continue
		}

		out = append(out, apigen.VerdictScore{
			Verdict:   verdict,
			Predicted: predicted[verdict],
			Actual:    actual[verdict],
			Hits:      hits[verdict],
			Precision: share(hits[verdict], predicted[verdict]),
			Recall:    share(hits[verdict], actual[verdict]),
		})
	}

	return out
}

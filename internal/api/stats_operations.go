package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/rollup"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// maxHourlyDays is the longest range the series may be asked for by
	// the hour.
	maxHourlyDays = 7
	// verdictNone names the investigations that have no verdict.
	verdictNone = "none"
)

// opsFigures are the counts the operations panels add up.
type opsFigures struct {
	investigations, finished, durationN, durationMsSum, firstFindingN, firstFindingMsSum int64
	toolCalls, noisyFixed, actionsProposed, actionsApproved, actionsExecuted             int64
	recoveries, downtimeMsSum, watchTimeouts                                             int64
}

// GetOperationsStats returns the volume, speed, tool calls, false
// positives, fixes, actions and downtime of the range from the hourly
// rollup, with the same totals for the range before.
func (s *server) GetOperationsStats(ctx context.Context, req apigen.GetOperationsStatsRequestObject) (apigen.GetOperationsStatsResponseObject, error) {
	p := req.Params

	w, err := s.statsWindowOf(p.From, p.To)
	if err != nil {
		return nil, err
	}

	f, err := statsFiltersOf(p.Route, p.Service, p.Model, p.Verdict)
	if err != nil {
		return nil, err
	}

	interval := apigen.OperationsStatsIntervalDay
	if p.Interval != nil {
		interval = apigen.OperationsStatsInterval(*p.Interval)
	}

	if interval == apigen.OperationsStatsIntervalHour && w.days() > maxHourlyDays {
		return nil, problemf(http.StatusBadRequest, "An interval of an hour needs a range of at most %d days.", maxHourlyDays)
	}

	q := db.New(customPlan{db: s.pool})

	series, err := operationsSeries(ctx, q, w, f, string(interval))
	if err != nil {
		return nil, fmt.Errorf("read the series: %w", err)
	}

	totals, err := operationsTotals(ctx, q, w, f, series.total)
	if err != nil {
		return nil, fmt.Errorf("total the range: %w", err)
	}

	before := w.before()

	previous, err := operationsSeries(ctx, q, before, f, string(apigen.OperationsStatsIntervalDay))
	if err != nil {
		return nil, fmt.Errorf("read the range before: %w", err)
	}

	previousTotals, err := operationsTotals(ctx, q, before, f, previous.total)
	if err != nil {
		return nil, fmt.Errorf("total the range before: %w", err)
	}

	downtime, err := downtimeRows(ctx, q, w, f)
	if err != nil {
		return nil, fmt.Errorf("read the downtime: %w", err)
	}

	return apigen.GetOperationsStats200JSONResponse{
		From: dateOf(w.from), To: dateOf(w.to), Interval: interval,
		Series: series.buckets, Totals: totals, Previous: previousTotals,
		Verdicts: series.verdicts, PreviousVerdicts: previous.verdicts, Downtime: downtime,
	}, nil
}

// opsSeries is the rows of a range folded into buckets.
type opsSeries struct {
	buckets  []apigen.OperationsBucket
	verdicts []apigen.VerdictCount
	total    opsFigures
}

// operationsSeries reads the hourly rows of the window by bucket and
// verdict, and folds them into one bucket per time, the totals by verdict
// and the total.
func operationsSeries(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters, interval string) (opsSeries, error) {
	rows, err := q.OperationsByBucket(ctx, db.OperationsByBucketParams{
		Bucket: interval, FromHour: w.from, ToHour: w.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return opsSeries{}, fmt.Errorf("read the rows: %w", err)
	}

	out := opsSeries{buckets: []apigen.OperationsBucket{}}
	byVerdict := map[string]int64{}

	for _, r := range rows {
		at := r.Bucket.Time.UTC()

		if n := len(out.buckets); n == 0 || !out.buckets[n-1].Bucket.Equal(at) {
			out.buckets = append(out.buckets, apigen.OperationsBucket{Bucket: at, Verdicts: []apigen.VerdictCount{}})
		}

		bucket := &out.buckets[len(out.buckets)-1]
		figures := figuresOf(r)

		addFigures(bucket, figures)
		out.total.add(figures)

		name := verdictName(r.Verdict)
		bucket.Verdicts = append(bucket.Verdicts, apigen.VerdictCount{Verdict: name, Investigations: r.Investigations})
		byVerdict[name] += r.Investigations
	}

	out.verdicts = verdictCounts(byVerdict)

	return out, nil
}

func figuresOf(r db.OperationsByBucketRow) opsFigures {
	return opsFigures{
		investigations: r.Investigations, finished: r.Finished, durationN: r.DurationN, durationMsSum: r.DurationMsSum,
		firstFindingN: r.FirstFindingN, firstFindingMsSum: r.FirstFindingMsSum, toolCalls: r.ToolCalls, noisyFixed: r.NoisyFixed,
		actionsProposed: r.ActionsProposed, actionsApproved: r.ActionsApproved, actionsExecuted: r.ActionsExecuted,
		recoveries: r.Recoveries, downtimeMsSum: r.DowntimeMsSum, watchTimeouts: r.WatchTimeouts,
	}
}

func (a *opsFigures) add(b opsFigures) {
	a.investigations += b.investigations
	a.finished += b.finished
	a.durationN += b.durationN
	a.durationMsSum += b.durationMsSum
	a.firstFindingN += b.firstFindingN
	a.firstFindingMsSum += b.firstFindingMsSum
	a.toolCalls += b.toolCalls
	a.noisyFixed += b.noisyFixed
	a.actionsProposed += b.actionsProposed
	a.actionsApproved += b.actionsApproved
	a.actionsExecuted += b.actionsExecuted
	a.recoveries += b.recoveries
	a.downtimeMsSum += b.downtimeMsSum
	a.watchTimeouts += b.watchTimeouts
}

// addFigures adds f to the counts of b.
func addFigures(b *apigen.OperationsBucket, f opsFigures) {
	b.Investigations += f.investigations
	b.Finished += f.finished
	b.DurationN += f.durationN
	b.DurationMsSum += f.durationMsSum
	b.FirstFindingN += f.firstFindingN
	b.FirstFindingMsSum += f.firstFindingMsSum
	b.ToolCalls += f.toolCalls
	b.NoisyFixed += f.noisyFixed
	b.ActionsProposed += f.actionsProposed
	b.ActionsApproved += f.actionsApproved
	b.ActionsExecuted += f.actionsExecuted
	b.Recoveries += f.recoveries
	b.DowntimeMsSum += f.downtimeMsSum
	b.WatchTimeouts += f.watchTimeouts
}

// operationsTotals returns the totals of a window with the medians worked
// out from its histograms.
func operationsTotals(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters, total opsFigures) (apigen.OperationsTotals, error) {
	firstFinding, err := q.FirstFindingHistogram(ctx, db.FirstFindingHistogramParams{
		FromHour: w.from, ToHour: w.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return apigen.OperationsTotals{}, fmt.Errorf("read the first finding histogram: %w", err)
	}

	downtime, err := q.DowntimeHistogram(ctx, db.DowntimeHistogramParams{
		FromHour: w.from, ToHour: w.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return apigen.OperationsTotals{}, fmt.Errorf("read the downtime histogram: %w", err)
	}

	out := apigen.OperationsTotals{
		Investigations: total.investigations, Finished: total.finished, DurationN: total.durationN, DurationMsSum: total.durationMsSum,
		FirstFindingN: total.firstFindingN, FirstFindingMsSum: total.firstFindingMsSum, ToolCalls: total.toolCalls,
		NoisyFixed: total.noisyFixed, ActionsProposed: total.actionsProposed, ActionsApproved: total.actionsApproved,
		ActionsExecuted: total.actionsExecuted, Recoveries: total.recoveries, DowntimeMsSum: total.downtimeMsSum,
		WatchTimeouts: total.watchTimeouts,
	}

	out.MedianFirstFindingMs = rollup.HistMedian(rollup.HistCounts(firstFinding, func(r db.FirstFindingHistogramRow) (int32, int64) { return r.Bucket, r.N }))
	out.MedianDowntimeMs = rollup.HistMedian(rollup.HistCounts(downtime, func(r db.DowntimeHistogramRow) (int32, int64) { return r.Bucket, r.N }))

	return out, nil
}

// downtimeRows returns the recoveries and downtime by route and service,
// with the median downtime of each.
func downtimeRows(ctx context.Context, q *db.Queries, w statsWindow, f statsFilters) ([]apigen.DowntimeRow, error) {
	rows, err := q.DowntimeByRouteService(ctx, db.DowntimeByRouteServiceParams{
		FromHour: w.from, ToHour: w.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, fmt.Errorf("read the sums: %w", err)
	}

	hists, err := q.DowntimeHistograms(ctx, db.DowntimeHistogramsParams{
		FromHour: w.from, ToHour: w.end(), Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
	})
	if err != nil {
		return nil, fmt.Errorf("read the histograms: %w", err)
	}

	type key struct{ route, service string }

	counts := map[key][]int64{}

	for _, h := range hists {
		k := key{h.Route, h.Service}
		if counts[k] == nil {
			counts[k] = make([]int64, rollup.HistBuckets)
		}

		if h.Bucket >= 1 && h.Bucket <= rollup.HistBuckets {
			counts[k][h.Bucket-1] += h.N
		}
	}

	out := make([]apigen.DowntimeRow, 0, len(rows))
	for _, r := range rows {
		row := apigen.DowntimeRow{
			Route: r.Route, Service: r.Service, Recoveries: r.Recoveries, DowntimeMsSum: r.DowntimeMsSum, WatchTimeouts: r.WatchTimeouts,
		}

		if c := counts[key{r.Route, r.Service}]; c != nil {
			row.MedianDowntimeMs = rollup.HistMedian(c)
		}

		out = append(out, row)
	}

	return out, nil
}

// verdictName is the name of a verdict in the series, with none for the
// empty verdict of an investigation that has not ended with one.
func verdictName(verdict string) string {
	if verdict == "" {
		return verdictNone
	}

	return verdict
}

// verdictCounts lists the counts in the order of a report's verdicts, then
// none.
func verdictCounts(counts map[string]int64) []apigen.VerdictCount {
	out := []apigen.VerdictCount{}

	for _, v := range report.Verdicts {
		if n, ok := counts[string(v)]; ok {
			out = append(out, apigen.VerdictCount{Verdict: string(v), Investigations: n})
		}
	}

	if n, ok := counts[verdictNone]; ok {
		out = append(out, apigen.VerdictCount{Verdict: verdictNone, Investigations: n})
	}

	return out
}

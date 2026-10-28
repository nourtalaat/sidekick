package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// targetBuckets is about how many time buckets count_logs makes when the
// call gives no interval.
const targetBuckets = 60

// niceIntervals are the bucket lengths count_logs picks from when the call
// gives no interval, shortest first.
var niceIntervals = []time.Duration{
	time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second,
	time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// Names of the aggregations count_logs asks for.
const (
	aggOverTime = "over_time"
	aggSplit    = "split"
)

// countAggs is the part of a count_logs answer's aggregations the source
// reads.
type countAggs struct {
	OverTime histogram `json:"over_time"`
	Split    struct {
		SumOtherDocCount int `json:"sum_other_doc_count"`
		Buckets          []struct {
			Key      any       `json:"key"`
			DocCount int       `json:"doc_count"`
			OverTime histogram `json:"over_time"`
		} `json:"buckets"`
	} `json:"split"`
}

// histogram is a date_histogram aggregation in an answer.
type histogram struct {
	Buckets []struct {
		Key      int64 `json:"key"`
		DocCount int   `json:"doc_count"`
	} `json:"buckets"`
}

// series is one line of counts over time: the count of each bucket, the
// first starting at Start.
type series struct {
	Start  time.Time `json:"start,omitzero"`
	Counts []int     `json:"counts"`
}

// splitValue is the counts of one value of the split field.
type splitValue struct {
	Value any `json:"value"`
	Total int `json:"total"`
	series
}

// countResult is the result of count_logs.
type countResult struct {
	Index           string `json:"index"`
	Total           int    `json:"total"`
	IntervalSeconds int    `json:"intervalSeconds"`
	series
	SplitBy string       `json:"splitBy,omitempty"`
	Values  []splitValue `json:"values,omitempty"`
	// Other counts the logs whose split value is not one of Values.
	Other   int  `json:"other,omitempty"`
	Partial bool `json:"partial,omitempty"`
}

// countLogs runs count_logs: a date_histogram of the logs that match the
// query string, and, with splitBy, one for each of its most common values.
func (s *Source) countLogs(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Index    string `json:"index"`
		Query    string `json:"query"`
		Since    string `json:"since"`
		Until    string `json:"until"`
		Interval int    `json:"interval"`
		SplitBy  string `json:"splitBy"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	q, err := s.checkSearch(in.Index, in.Query, in.Since, in.Until)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	interval, err := intervalOf(in.Interval, q.w)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if in.SplitBy != "" {
		if err := checkField(in.SplitBy, false); err != nil {
			return source.Result{}, fmt.Errorf("read the input: splitBy: %w", err)
		}
	}

	hist := s.histogram(interval, q.w)
	aggs := map[string]any{aggOverTime: hist}

	if in.SplitBy != "" {
		aggs[aggSplit] = map[string]any{
			"terms": map[string]any{"field": in.SplitBy, "size": maxSplitValues},
			"aggs":  map[string]any{aggOverTime: hist},
		}
	}

	req := searchRequest{Size: 0, Query: s.filter(q), TrackTotalHits: true, Aggs: aggs}

	var answer searchAnswer
	if err := s.client.search(ctx, q.index, req, &answer); err != nil {
		return source.Result{}, fmt.Errorf("count in %s: %w", q.index, err)
	}

	var parsed countAggs
	if len(answer.Aggregations) > 0 {
		if err := json.Unmarshal(answer.Aggregations, &parsed); err != nil {
			return source.Result{}, fmt.Errorf("decode the aggregations: %w", err)
		}
	}

	out := countResult{
		Index:           q.index,
		Total:           answer.Hits.Total.Value,
		IntervalSeconds: int(interval.Seconds()),
		series:          parsed.OverTime.series(interval),
		SplitBy:         in.SplitBy,
		Partial:         answer.TimedOut || answer.Shards.Failed > 0,
	}

	if in.SplitBy != "" {
		out.Other = parsed.Split.SumOtherDocCount

		for _, b := range parsed.Split.Buckets {
			value := b.Key
			if text, ok := value.(string); ok {
				value = source.CutText(text, maxNameLen)
			}

			out.Values = append(out.Values, splitValue{Value: value, Total: b.DocCount, series: b.OverTime.series(interval)})
		}
	}

	empty := fmt.Sprintf("No logs in %s matched between %s and %s.", q.index, timestamp(q.w.start), timestamp(q.w.end))

	return structured(out, s.evidence(q.index, q.query, q.w), out.Partial, out.Total == 0, empty)
}

// histogram returns the date_histogram aggregation of count_logs, with a
// bucket for every interval of w, empty ones too.
func (s *Source) histogram(interval time.Duration, w window) map[string]any {
	return map[string]any{"date_histogram": map[string]any{
		"field":          s.cfg.timeField(),
		"fixed_interval": fmt.Sprintf("%ds", int(interval.Seconds())),
		"min_doc_count":  0,
		"extended_bounds": map[string]int64{
			"min": w.start.UnixMilli(),
			"max": w.end.UnixMilli() - 1,
		},
	}}
}

// series returns the counts of the histogram's buckets in time order, the
// first one at Start. A bucket the answer skips counts as zero.
func (h histogram) series(interval time.Duration) series {
	if len(h.Buckets) == 0 {
		return series{Counts: []int{}}
	}

	start := h.Buckets[0].Key
	step := interval.Milliseconds()
	last := h.Buckets[len(h.Buckets)-1].Key

	counts := make([]int, min((last-start)/step+1, maxBuckets+1))

	for _, b := range h.Buckets {
		if i := (b.Key - start) / step; i >= 0 && i < int64(len(counts)) {
			counts[i] += b.DocCount
		}
	}

	return series{Start: time.UnixMilli(start).UTC(), Counts: counts}
}

// intervalOf returns the bucket length of count_logs: the call's interval
// in seconds, or the shortest nice interval that gives at most about
// targetBuckets buckets over w. An interval that gives more than
// maxBuckets buckets is a *source.ToolError.
func intervalOf(seconds int, w window) (time.Duration, error) {
	length := w.end.Sub(w.start).Seconds()

	if seconds > 0 {
		if buckets := int(math.Ceil(length / float64(seconds))); buckets > maxBuckets {
			return 0, &source.ToolError{Message: fmt.Sprintf("interval: %ds gives %d buckets, more than %d; use at least %ds",
				seconds, buckets, maxBuckets, int(math.Ceil(length/maxBuckets)))}
		}

		return time.Duration(seconds) * time.Second, nil
	}

	for _, interval := range niceIntervals {
		if length/interval.Seconds() <= targetBuckets {
			return interval, nil
		}
	}

	return niceIntervals[len(niceIntervals)-1], nil
}

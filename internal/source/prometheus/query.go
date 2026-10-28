package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/prometheus/prometheus/promql/parser"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/promql"
)

// queryResult is the result of query.
type queryResult struct {
	ResultType string    `json:"resultType"`
	Time       time.Time `json:"time"`
	// Value is the value of a scalar or string result.
	Value any `json:"value,omitempty"`
	// SeriesTotal is how many series the query returned, before the cut
	// to maxSeries.
	SeriesTotal int             `json:"seriesTotal,omitempty"`
	Series      []instantSeries `json:"series,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
	Infos       []string        `json:"infos,omitempty"`
}

// instantSeries is one series in the result of query: a value for a
// vector, points for a matrix.
type instantSeries struct {
	Labels map[string]string `json:"labels"`
	Value  any               `json:"value,omitempty"`
	Points []point           `json:"points,omitempty"`
}

// rangeResult is the result of query_range.
type rangeResult struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	StepSeconds int       `json:"stepSeconds"`
	// SeriesTotal is how many series the query returned, before the cut
	// to maxSeries.
	SeriesTotal int           `json:"seriesTotal"`
	Series      []rangeSeries `json:"series"`
	Warnings    []string      `json:"warnings,omitempty"`
	Infos       []string      `json:"infos,omitempty"`
}

// rangeSeries is one series in the result of query_range.
type rangeSeries struct {
	Labels map[string]string `json:"labels"`
	Stats  *stats            `json:"stats,omitempty"`
	Points []point           `json:"points"`
}

// query runs query: an instant query, at the time the call gives or now.
func (s *Source) query(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Query string `json:"query"`
		Time  string `json:"time"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if _, err := s.parseQuery(in.Query); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	at, err := s.at("time", in.Time)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	var answer promql.Answer[promql.QueryData]
	if err := s.prom.get(ctx, "/api/v1/query", url.Values{"query": {in.Query}, "time": {timestamp(at)}}, &answer); err != nil {
		return source.Result{}, fmt.Errorf("run the query: %w", err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	out := queryResult{ResultType: answer.Data.ResultType, Time: at, Warnings: cutTexts(answer.Warnings), Infos: cutTexts(answer.Infos)}
	evidence := []source.Evidence{s.links.query("PromQL query", in.Query, window{start: at.Add(-defaultRange), end: at}, true)}

	if answer.Data.ResultType == promql.TypeScalar || answer.Data.ResultType == promql.TypeString {
		sample, err := answer.Data.Sample()
		if err != nil {
			return source.Result{}, fmt.Errorf("read the answer: %w", err)
		}

		out.Value = sampleValue(sample.V)

		return structured(out, evidence, false, false, "")
	}

	series, err := answer.Data.Series()
	if err != nil {
		return source.Result{}, fmt.Errorf("read the answer: %w", err)
	}

	out.SeriesTotal = len(series)
	b := budget{}
	cut := len(series) > maxSeries

	for _, r := range series[:min(len(series), maxSeries)] {
		labels, labelsCut := cutLabels(r.Metric)
		item := instantSeries{Labels: labels}

		if r.Value != nil {
			item.Value = sampleValue(r.Value.V)
		} else {
			values, valuesCut := newest(r.Values)
			item.Points = points(values)
			cut = cut || valuesCut
		}

		if !b.fits(item) {
			break
		}

		cut = cut || labelsCut
		out.Series = append(out.Series, item)
	}

	empty := fmt.Sprintf("The query returned no series at %s.", timestamp(at))

	return structured(out, evidence, cut || b.cut, len(series) == 0, empty)
}

// queryRange runs query_range, with a step that keeps each series within
// maxPoints points.
func (s *Source) queryRange(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Query string `json:"query"`
		Since string `json:"since"`
		Until string `json:"until"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	expr, err := s.parseQuery(in.Query)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if t := expr.Type(); t != parser.ValueTypeVector && t != parser.ValueTypeScalar {
		kind := string(t)
		if t == parser.ValueTypeMatrix {
			kind = "range vector"
		}

		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("query: a range query needs an expression that returns an instant "+
			"vector or a scalar, not a %s; wrap a range vector such as x[5m] in a function such as rate()", kind)}
	}

	w, err := s.window(in.Since, in.Until)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	step := rangeStep(w)
	query := url.Values{
		"query": {in.Query},
		"start": {timestamp(w.start)},
		"end":   {timestamp(w.end)},
		"step":  {seconds(step)},
	}

	var answer promql.Answer[promql.QueryData]
	if err := s.prom.get(ctx, "/api/v1/query_range", query, &answer); err != nil {
		return source.Result{}, fmt.Errorf("run the range query: %w", err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	series, err := answer.Data.Series()
	if err != nil {
		return source.Result{}, fmt.Errorf("read the answer: %w", err)
	}

	out := rangeResult{
		Start: w.start, End: w.end, StepSeconds: step, SeriesTotal: len(series),
		Series:   make([]rangeSeries, 0, min(len(series), maxSeries)),
		Warnings: cutTexts(answer.Warnings), Infos: cutTexts(answer.Infos),
	}

	b := budget{}
	cut := len(series) > maxSeries

	for _, r := range series[:min(len(series), maxSeries)] {
		labels, labelsCut := cutLabels(r.Metric)
		values, valuesCut := newest(r.Values)
		item := rangeSeries{Labels: labels, Stats: statsOf(values), Points: points(values)}

		if !b.fits(item) {
			break
		}

		cut = cut || labelsCut || valuesCut
		out.Series = append(out.Series, item)
	}

	evidence := []source.Evidence{s.links.query("PromQL range query", in.Query, w, false)}
	empty := fmt.Sprintf("The range query returned no series between %s and %s.", timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, cut || b.cut, len(series) == 0, empty)
}

// newest returns the newest maxPoints samples, and reports whether older
// ones were left out.
func newest(samples []promql.Sample) ([]promql.Sample, bool) {
	if len(samples) <= maxPoints {
		return samples, false
	}

	return samples[len(samples)-maxPoints:], true
}

// points returns the points of samples.
func points(samples []promql.Sample) []point {
	out := make([]point, len(samples))
	for i, s := range samples {
		out[i] = newPoint(s)
	}

	return out
}

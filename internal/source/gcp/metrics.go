package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultMetricRange is the time range of query_metrics and query_promql
// when the call gives no since.
const defaultMetricRange = time.Hour

// Bounds on the points of a time series.
const (
	// maxPoints is the most points a result keeps of one series.
	maxPoints = 120
	// minAlignment is the shortest alignment period Cloud Monitoring
	// takes, in seconds.
	minAlignment = 60
	// minStep is the shortest PromQL step a call may ask for, in seconds.
	minStep = 10
)

// metricsExplorer is the console path of Metrics Explorer.
const metricsExplorer = "/monitoring/metrics-explorer"

// timeSeriesPage is one answer of timeSeries.list.
type timeSeriesPage struct {
	TimeSeries    []timeSeries `json:"timeSeries"`
	NextPageToken string       `json:"nextPageToken"`
}

// timeSeries is one Cloud Monitoring time series.
type timeSeries struct {
	Metric     resource `json:"metric"`
	Resource   resource `json:"resource"`
	MetricKind string   `json:"metricKind"`
	ValueType  string   `json:"valueType"`
	Unit       string   `json:"unit"`
	Points     []struct {
		Interval struct {
			EndTime time.Time `json:"endTime"`
		} `json:"interval"`
		Value typedValue `json:"value"`
	} `json:"points"`
}

// typedValue is the value of one point; one field is set.
type typedValue struct {
	BoolValue         *bool    `json:"boolValue"`
	Int64Value        *string  `json:"int64Value"`
	DoubleValue       *float64 `json:"doubleValue"`
	StringValue       *string  `json:"stringValue"`
	DistributionValue *struct {
		Count string  `json:"count"`
		Mean  float64 `json:"mean"`
	} `json:"distributionValue"`
}

// point is one point of a series in a result. V is a number, a string or a
// bool, or for a distribution the mean and count.
type point struct {
	T time.Time `json:"t"`
	V any       `json:"v"`
}

// seriesItem is one series in the result of query_metrics.
type seriesItem struct {
	Metric   resource `json:"metric"`
	Resource resource `json:"resource"`
	Kind     string   `json:"kind,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Points   []point  `json:"points"`
}

// metricsResult is the result of query_metrics.
type metricsResult struct {
	Project string       `json:"project"`
	Series  []seriesItem `json:"series"`
}

// queryMetrics runs query_metrics. The filter goes to Cloud Monitoring as
// given.
func (s *Source) queryMetrics(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project         string   `json:"project"`
		Filter          string   `json:"filter"`
		Since           string   `json:"since"`
		Until           string   `json:"until"`
		AlignmentPeriod int      `json:"alignmentPeriod"`
		Aligner         string   `json:"aligner"`
		Reducer         string   `json:"reducer"`
		GroupBy         []string `json:"groupBy"`
		Limit           int      `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.project(in.Project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultMetricRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if strings.TrimSpace(in.Filter) == "" {
		return source.Result{}, &source.ToolError{Message: "filter: required, for example metric.type=\"run.googleapis.com/request_count\""}
	}

	if in.Reducer != "" && in.Aligner == "" {
		return source.Result{}, &source.ToolError{Message: "reducer: needs an aligner"}
	}

	limit := limitOf(in.Limit, defaultSeries, maxSeries)
	query := url.Values{
		"filter":             {in.Filter},
		"interval.startTime": {timestamp(w.start)},
		"interval.endTime":   {timestamp(w.end)},
		"pageSize":           {strconv.Itoa(limit)},
	}

	if in.Aligner != "" || in.AlignmentPeriod > 0 {
		aligner := in.Aligner
		if aligner == "" {
			aligner = "ALIGN_MEAN"
		}

		query.Set("aggregation.alignmentPeriod", strconv.Itoa(alignment(in.AlignmentPeriod, w))+"s")
		query.Set("aggregation.perSeriesAligner", aligner)
	}

	if in.Reducer != "" {
		query.Set("aggregation.crossSeriesReducer", in.Reducer)
		query["aggregation.groupByFields"] = in.GroupBy
	}

	var page timeSeriesPage
	if err := s.client.get(ctx, s.apis.monitoring+"/projects/"+url.PathEscape(project)+"/timeSeries", query, &page); err != nil {
		return source.Result{}, fmt.Errorf("read the time series of %s: %w", project, err)
	}

	out := metricsResult{Project: project, Series: make([]seriesItem, 0, len(page.TimeSeries))}
	b := budget{}
	cut := page.NextPageToken != ""

	for _, ts := range page.TimeSeries {
		item := seriesItem{Metric: ts.Metric, Resource: ts.Resource, Kind: ts.MetricKind + " " + ts.ValueType, Unit: ts.Unit}
		item.Kind = strings.TrimSpace(item.Kind)

		for i, p := range ts.Points {
			if i == maxPoints {
				cut = true

				break
			}

			item.Points = append(item.Points, point{T: p.Interval.EndTime, V: p.Value.value()})
		}

		if !b.fits(item) {
			break
		}

		out.Series = append(out.Series, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceMetric,
		Title: "Metrics Explorer in " + project,
		URL:   console(metricsExplorer, project),
		At:    w.end,
	}}

	empty := fmt.Sprintf("No time series in %s matched between %s and %s.", project, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, cut || b.cut, len(out.Series) == 0, empty)
}

// value returns the one value that is set.
func (v typedValue) value() any {
	switch {
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.Int64Value != nil:
		if n, err := strconv.ParseInt(*v.Int64Value, 10, 64); err == nil {
			return n
		}

		return *v.Int64Value
	case v.BoolValue != nil:
		return *v.BoolValue
	case v.StringValue != nil:
		return source.CutText(*v.StringValue, maxNameLen)
	case v.DistributionValue != nil:
		return map[string]any{"mean": v.DistributionValue.Mean, "count": v.DistributionValue.Count}
	default:
		return nil
	}
}

// alignment returns the alignment period a call gives, at least
// minAlignment seconds, or one that splits the window into about
// maxPoints points.
func alignment(period int, w window) int {
	if period <= 0 {
		period = int(w.end.Sub(w.start).Seconds()) / maxPoints
	}

	return max(period, minAlignment)
}

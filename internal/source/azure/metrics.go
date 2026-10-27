package azure

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

// defaultMetricRange is the time range of query_metrics when the call
// gives no since.
const defaultMetricRange = time.Hour

// Bounds on a metrics call.
const (
	// maxPoints is the most points a result keeps of one series, and the
	// most an automatic interval aims for.
	maxPoints = 120
	// maxMetricNames is the most metrics one call reads.
	maxMetricNames = 10
)

// defaultAggregation is the aggregation of query_metrics when the call
// gives none.
const defaultAggregation = "Average"

// grains are the intervals query_metrics picks from, smallest first.
var grains = []struct {
	name string
	size time.Duration
}{
	{"PT1M", time.Minute}, {"PT5M", 5 * time.Minute}, {"PT15M", 15 * time.Minute}, {"PT30M", 30 * time.Minute},
	{"PT1H", time.Hour}, {"PT6H", 6 * time.Hour}, {"PT12H", 12 * time.Hour}, {"P1D", 24 * time.Hour},
}

// metricsAnswer is the answer of the metrics API.
type metricsAnswer struct {
	Value []struct {
		Name       localized `json:"name"`
		Unit       string    `json:"unit"`
		ErrorCode  string    `json:"errorCode"`
		Timeseries []struct {
			Metadata []struct {
				Name  localized `json:"name"`
				Value string    `json:"value"`
			} `json:"metadatavalues"`
			Data []metricPoint `json:"data"`
		} `json:"timeseries"`
	} `json:"value"`
}

// metricPoint is one point; the aggregations the call did not ask for are
// left out.
type metricPoint struct {
	TimeStamp time.Time `json:"timeStamp"`
	Average   *float64  `json:"average,omitempty"`
	Total     *float64  `json:"total,omitempty"`
	Minimum   *float64  `json:"minimum,omitempty"`
	Maximum   *float64  `json:"maximum,omitempty"`
	Count     *float64  `json:"count,omitempty"`
}

// seriesItem is one series in the result of query_metrics.
type seriesItem struct {
	Metric     string            `json:"metric"`
	Unit       string            `json:"unit,omitempty"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	Points     []metricPoint     `json:"points"`
}

// metricsResult is the result of query_metrics.
type metricsResult struct {
	Resource string       `json:"resource"`
	Interval string       `json:"interval"`
	Series   []seriesItem `json:"series"`
}

// queryMetrics runs query_metrics. The dimension filter goes to Azure
// Monitor as given.
func (s *Source) queryMetrics(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Resource    string   `json:"resource"`
		Metrics     []string `json:"metrics"`
		Namespace   string   `json:"namespace"`
		Aggregation string   `json:"aggregation"`
		Interval    string   `json:"interval"`
		Filter      string   `json:"filter"`
		Since       string   `json:"since"`
		Until       string   `json:"until"`
		Limit       int      `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	id, err := s.resource("resource", in.Resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultMetricRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	names, err := metricNames(in.Metrics)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	aggregation := in.Aggregation
	if aggregation == "" {
		aggregation = defaultAggregation
	}

	interval := in.Interval
	if interval == "" {
		interval = grainFor(w)
	}

	limit := limitOf(in.Limit, defaultSeries, maxSeries)
	params := url.Values{
		"metricnames": {strings.Join(names, ",")},
		"timespan":    {w.timespan()},
		"interval":    {interval},
		"aggregation": {aggregation},
		"top":         {strconv.Itoa(limit)},
	}

	if in.Namespace != "" {
		params.Set("metricnamespace", in.Namespace)
	}

	if in.Filter != "" {
		params.Set("$filter", in.Filter)
	}

	var answer metricsAnswer
	if err := s.client.get(ctx, armScope, s.apis.arm+id+"/providers/Microsoft.Insights/metrics", query(metricsVersion, params), &answer); err != nil {
		return source.Result{}, fmt.Errorf("read the metrics of %s: %w", id, err)
	}

	out := metricsResult{Resource: id, Interval: interval, Series: []seriesItem{}}
	b := budget{}
	truncated := false

	for _, m := range answer.Value {
		for _, ts := range m.Timeseries {
			if len(out.Series) == limit {
				truncated = true

				break
			}

			item := seriesItem{Metric: m.Name.text(), Unit: m.Unit, Points: ts.Data}
			if len(item.Points) > maxPoints {
				item.Points = item.Points[len(item.Points)-maxPoints:]
				truncated = true
			}

			for _, d := range ts.Metadata {
				if item.Dimensions == nil {
					item.Dimensions = map[string]string{}
				}

				item.Dimensions[d.Name.text()] = source.CutText(d.Value, maxNameLen)
			}

			if !b.fits(item) {
				break
			}

			out.Series = append(out.Series, item)
		}
	}

	evidence := []source.Evidence{{Kind: source.EvidenceMetric, Title: "Metrics of " + lastPart(id), URL: portal(id, "/metrics"), At: w.end}}
	empty := fmt.Sprintf("Azure Monitor has no points for those metrics of %s between %s and %s.", id, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, truncated || b.cut, !hasPoints(out), empty)
}

// metricNames checks the metric names of a call: one to maxMetricNames,
// none empty, and none with a comma, which separates them in the request.
func metricNames(names []string) ([]string, error) {
	out := make([]string, 0, len(names))

	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || strings.Contains(n, ",") || len(n) > maxNameLen {
			return nil, &source.ToolError{Message: fmt.Sprintf("metrics: %q is not a metric name", source.CutText(n, maxNameLen))}
		}

		out = append(out, n)
	}

	if len(out) == 0 || len(out) > maxMetricNames {
		return nil, &source.ToolError{Message: fmt.Sprintf("metrics: give 1 to %d metric names", maxMetricNames)}
	}

	return out, nil
}

// grainFor returns the smallest interval that splits the window into at
// most maxPoints points.
func grainFor(w window) string {
	span := w.end.Sub(w.start)

	for _, g := range grains {
		if span/g.size <= maxPoints {
			return g.name
		}
	}

	return grains[len(grains)-1].name
}

// hasPoints reports whether any series has a point with a value.
func hasPoints(r metricsResult) bool {
	for _, s := range r.Series {
		for _, p := range s.Points {
			if p.Average != nil || p.Total != nil || p.Minimum != nil || p.Maximum != nil || p.Count != nil {
				return true
			}
		}
	}

	return false
}

// lastPart returns the part of a resource ID after its last slash.
func lastPart(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

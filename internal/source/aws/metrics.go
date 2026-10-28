package aws

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultMetricRange is the time range of query_metrics when the call
// gives no since.
const defaultMetricRange = time.Hour

// Bounds of one query_metrics call.
const (
	// minPeriod is the floor on the period of a point, in seconds: the
	// resolution of standard metrics.
	minPeriod = 60
	// maxPoints is the point budget of one series. The period is raised
	// until a series of the call's range fits it.
	maxPoints = 120
	// maxMetricPages is the most GetMetricData pages one call reads.
	maxMetricPages = 3
	// maxExpressionLen is the longest metric math expression CloudWatch
	// takes.
	maxExpressionLen = 2048
	// maxDimensions is the most dimensions a metric has.
	maxDimensions = 30
	// maxMetricNameLen bounds a namespace, metric or dimension name.
	maxMetricNameLen = 255
	// maxDimensionValueLen bounds a dimension value.
	maxDimensionValueLen = 1024
)

var (
	// queryIDPattern is the shape of a GetMetricData query ID.
	queryIDPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,254}$`)
	// statPattern is the shape of a statistic: a named one, or a
	// percentile or trimmed statistic such as p99 or tm90.
	statPattern = regexp.MustCompile(`^(Average|Sum|Minimum|Maximum|SampleCount|IQM|(p|tm|wm|tc|ts)[0-9]{1,2}(\.[0-9]{1,2})?)$`)
)

// metricQuery is one query of a query_metrics call.
type metricQuery struct {
	ID         string            `json:"id"`
	Namespace  string            `json:"namespace"`
	Metric     string            `json:"metric"`
	Dimensions map[string]string `json:"dimensions"`
	Stat       string            `json:"stat"`
	Expression string            `json:"expression"`
	Label      string            `json:"label"`
	Hidden     bool              `json:"hidden"`
}

// metricPoint is one point of a series.
type metricPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// metricSeries is one series in the result of query_metrics.
type metricSeries struct {
	ID     string        `json:"id"`
	Label  string        `json:"label,omitempty"`
	Status string        `json:"status,omitempty"`
	Points []metricPoint `json:"points"`
}

// metricsResult is the result of query_metrics.
type metricsResult struct {
	Account string         `json:"account"`
	Region  string         `json:"region"`
	Period  int            `json:"period"`
	Series  []metricSeries `json:"series"`
	// Messages are what CloudWatch says about the call or a series, such
	// as a series with too many points.
	Messages []string `json:"messages,omitempty"`
}

// queryMetrics runs query_metrics with GetMetricData.
func (s *Source) queryMetrics(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account string        `json:"account"`
		Region  string        `json:"region"`
		Queries []metricQuery `json:"queries"`
		Period  int           `json:"period"`
		Since   string        `json:"since"`
		Until   string        `json:"until"`
		Limit   int           `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	t, err := s.target(in.Account, in.Region)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultMetricRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	period, err := periodOf(in.Period, w)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	queries, err := metricQueries(in.Queries, period)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	limit := limitOf(in.Limit, defaultSeries, maxSeries)

	out, truncated, err := readMetrics(ctx, s.cloudWatch(t), &cloudwatch.GetMetricDataInput{
		MetricDataQueries: queries,
		StartTime:         awssdk.Time(w.start),
		EndTime:           awssdk.Time(w.end),
		ScanBy:            types.ScanByTimestampAscending,
		MaxDatapoints:     awssdk.Int32(maxSeries * maxPoints),
	}, limit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the metrics of account %s, Region %s: %w", t.account.id, t.region, err)
	}

	out.Account, out.Region, out.Period = t.account.id, t.region, period

	points := 0
	for _, series := range out.Series {
		points += len(series.Points)
	}

	where := fmt.Sprintf("account %s, Region %s", t.account.id, t.region)

	evidence := []source.Evidence{{
		Kind:  source.EvidenceMetric,
		Title: "CloudWatch metrics in " + where,
		URL:   metricsURL(t.region, in.Queries, period, w),
		At:    w.end,
	}}

	empty := fmt.Sprintf("CloudWatch has no points for those queries in %s between %s and %s.", where, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, truncated, points == 0, empty)
}

// periodOf returns the period of a call: the one it gives, a multiple of
// minPeriod, raised to the smallest that keeps one series of the window
// within maxPoints.
func periodOf(given int, w window) (int, error) {
	if given != 0 && (given < minPeriod || given%minPeriod != 0) {
		return 0, &source.ToolError{Message: fmt.Sprintf("period: %d is not a multiple of %d seconds", given, minPeriod)}
	}

	seconds := int(w.end.Sub(w.start) / time.Second)
	budget := (seconds + maxPoints - 1) / maxPoints
	budget = (budget + minPeriod - 1) / minPeriod * minPeriod

	return max(given, minPeriod, budget), nil
}

// metricQueries checks the queries of a call and returns them as
// GetMetricData takes them. Each has a unique ID and either a metric or an
// expression, with names and values that fit CloudWatch's limits.
func metricQueries(in []metricQuery, period int) ([]types.MetricDataQuery, error) {
	if len(in) == 0 || len(in) > maxMetricQueries {
		return nil, &source.ToolError{Message: fmt.Sprintf("queries: give 1 to %d queries", maxMetricQueries)}
	}

	out := make([]types.MetricDataQuery, 0, len(in))
	seen := map[string]bool{}

	for i, q := range in {
		key := fmt.Sprintf("queries[%d]", i)

		switch {
		case !queryIDPattern.MatchString(q.ID):
			return nil, &source.ToolError{Message: fmt.Sprintf("%s.id: %q must start with a lower-case letter and hold only letters, digits and _",
				key, source.CutText(q.ID, maxNameLen))}
		case seen[q.ID]:
			return nil, &source.ToolError{Message: fmt.Sprintf("%s.id: %q is used twice", key, q.ID)}
		case len(q.Label) > maxNameLen || strings.ContainsFunc(q.Label, isControl):
			return nil, &source.ToolError{Message: fmt.Sprintf("%s.label: must be at most %d bytes, on one line", key, maxNameLen)}
		}

		seen[q.ID] = true

		query := types.MetricDataQuery{Id: awssdk.String(q.ID), ReturnData: awssdk.Bool(!q.Hidden)}
		if q.Label != "" {
			query.Label = awssdk.String(q.Label)
		}

		if err := fillQuery(&query, key, q, period); err != nil {
			return nil, err
		}

		out = append(out, query)
	}

	return out, nil
}

// fillQuery sets the metric or the expression of query from q.
func fillQuery(query *types.MetricDataQuery, key string, q metricQuery, period int) error {
	hasMetric := q.Namespace != "" || q.Metric != "" || len(q.Dimensions) > 0 || q.Stat != ""

	switch {
	case hasMetric == (q.Expression != ""):
		return &source.ToolError{Message: key + ": give either namespace and metric, or expression"}
	case q.Expression != "":
		if len(q.Expression) > maxExpressionLen || strings.ContainsFunc(q.Expression, isControl) {
			return &source.ToolError{Message: fmt.Sprintf("%s.expression: must be at most %d bytes, on one line", key, maxExpressionLen)}
		}

		query.Expression = awssdk.String(q.Expression)
		query.Period = awssdk.Int32(int32(period)) //nolint:gosec // The period is at most a week of seconds.

		return nil
	}

	stat := q.Stat
	if stat == "" {
		stat = "Average"
	}

	if err := checkMetric(key, q, stat); err != nil {
		return err
	}

	metric := &types.Metric{Namespace: awssdk.String(q.Namespace), MetricName: awssdk.String(q.Metric)}
	for _, name := range slices.Sorted(maps.Keys(q.Dimensions)) {
		metric.Dimensions = append(metric.Dimensions, types.Dimension{Name: awssdk.String(name), Value: awssdk.String(q.Dimensions[name])})
	}

	query.MetricStat = &types.MetricStat{
		Metric: metric,
		Stat:   awssdk.String(stat),
		Period: awssdk.Int32(int32(period)), //nolint:gosec // The period is at most a week of seconds.
	}

	return nil
}

// checkMetric checks the names, dimensions and statistic of a metric
// query.
func checkMetric(key string, q metricQuery, stat string) error {
	for _, f := range []struct{ name, value string }{{"namespace", q.Namespace}, {"metric", q.Metric}} {
		if f.value == "" || len(f.value) > maxMetricNameLen || strings.ContainsFunc(f.value, isControl) {
			return &source.ToolError{Message: fmt.Sprintf("%s.%s: required with a metric, at most %d bytes on one line", key, f.name, maxMetricNameLen)}
		}
	}

	if len(q.Dimensions) > maxDimensions {
		return &source.ToolError{Message: fmt.Sprintf("%s.dimensions: at most %d", key, maxDimensions)}
	}

	for name, value := range q.Dimensions {
		if name == "" || len(name) > maxMetricNameLen || len(value) > maxDimensionValueLen ||
			strings.ContainsFunc(name, isControl) || strings.ContainsFunc(value, isControl) {
			return &source.ToolError{Message: fmt.Sprintf("%s.dimensions: names must be 1 to %d bytes and values at most %d, on one line",
				key, maxMetricNameLen, maxDimensionValueLen)}
		}
	}

	if !statPattern.MatchString(stat) {
		return &source.ToolError{Message: fmt.Sprintf("%s.stat: %q is not a statistic such as Average, Sum, Maximum or p99",
			key, source.CutText(stat, maxNameLen))}
	}

	return nil
}

// readMetrics reads the series of a GetMetricData request from at most
// maxMetricPages pages, joining the points of a series that runs over
// pages. It keeps at most limit series and maxPoints points of each, and
// reports whether anything was cut or CloudWatch returned partial data.
func readMetrics(ctx context.Context, client *cloudwatch.Client, in *cloudwatch.GetMetricDataInput, limit int) (metricsResult, bool, error) {
	out := metricsResult{Series: []metricSeries{}}
	index := map[string]int{}
	truncated := false

	for page := 1; ; page++ {
		answer, err := client.GetMetricData(ctx, in)
		if err != nil {
			return metricsResult{}, false, fmt.Errorf("read page %d: %w", page, err)
		}

		out.Messages = appendMessages(out.Messages, "", answer.Messages)

		for _, r := range answer.MetricDataResults {
			id := awssdk.ToString(r.Id)

			i, ok := index[id]
			if !ok {
				if len(out.Series) == limit {
					truncated = true

					continue
				}

				i = len(out.Series)
				index[id] = i
				out.Series = append(out.Series, metricSeries{ID: id, Label: source.CutText(awssdk.ToString(r.Label), maxNameLen), Points: []metricPoint{}})
			}

			series := &out.Series[i]
			series.Status = string(r.StatusCode)
			truncated = truncated || r.StatusCode == types.StatusCodePartialData
			out.Messages = appendMessages(out.Messages, id+": ", r.Messages)

			for j, ts := range r.Timestamps[:min(len(r.Timestamps), len(r.Values))] {
				if len(series.Points) == maxPoints {
					truncated = true

					break
				}

				series.Points = append(series.Points, metricPoint{Time: ts.UTC(), Value: r.Values[j]})
			}
		}

		switch {
		case answer.NextToken == nil:
			return out, truncated, nil
		case page == maxMetricPages:
			return out, true, nil
		}

		in.NextToken = answer.NextToken
	}
}

// appendMessages adds CloudWatch's messages to list, each with prefix and
// cut, up to maxMessages in all.
func appendMessages(list []string, prefix string, messages []types.MessageData) []string {
	for _, m := range messages {
		if len(list) == maxMessages {
			return list
		}

		text := strings.TrimSuffix(awssdk.ToString(m.Code)+": "+awssdk.ToString(m.Value), ": ")
		list = append(list, source.CutText(prefix+text, maxMessageLen))
	}

	return list
}

// metricsURL returns the CloudWatch console link that graphs the queries
// of a call over w.
func metricsURL(region string, queries []metricQuery, period int, w window) string {
	var metrics strings.Builder

	for _, q := range queries {
		if q.Expression != "" {
			metrics.WriteString("~(~(expression~'" + jsurl(q.Expression) + "~label~'" + jsurl(cmp.Or(q.Label, q.ID)) + "~id~'" + jsurl(q.ID) + "))")

			continue
		}

		metrics.WriteString("~(~'" + jsurl(q.Namespace) + "~'" + jsurl(q.Metric))

		for _, name := range slices.Sorted(maps.Keys(q.Dimensions)) {
			metrics.WriteString("~'" + jsurl(name) + "~'" + jsurl(q.Dimensions[name]))
		}

		metrics.WriteString("~(stat~'" + jsurl(cmp.Or(q.Stat, "Average")) + "~id~'" + jsurl(q.ID))

		if q.Hidden {
			metrics.WriteString("~visible~false")
		}

		metrics.WriteString("))")
	}

	graph := "~(metrics~(" + metrics.String() + ")~view~'timeSeries~stacked~false~region~'" + jsurl(region) +
		"~start~'" + jsurl(consoleTime(w.start)) + "~end~'" + jsurl(consoleTime(w.end)) + "~period~" + strconv.Itoa(period) + ")"

	return consoleURL(region, "cloudwatch", "metricsV2:graph="+graph)
}

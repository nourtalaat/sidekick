package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// promAnswer is the answer of the Prometheus query_range API.
type promAnswer struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
	Warnings []string `json:"warnings"`
}

// promPoint is one point of a PromQL series: the time and the value as
// Prometheus writes it, such as "0.25" or "NaN".
type promPoint struct {
	T time.Time `json:"t"`
	V string    `json:"v"`
}

// promSeries is one series in the result of query_promql.
type promSeries struct {
	Labels map[string]string `json:"labels"`
	Points []promPoint       `json:"points"`
}

// promResult is the result of query_promql.
type promResult struct {
	Project  string       `json:"project"`
	Series   []promSeries `json:"series"`
	Warnings []string     `json:"warnings,omitempty"`
}

// queryPromQL runs query_promql. The query goes to Cloud Monitoring as
// given.
func (s *Source) queryPromQL(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project string `json:"project"`
		Query   string `json:"query"`
		Since   string `json:"since"`
		Until   string `json:"until"`
		Step    int    `json:"step"`
		Limit   int    `json:"limit"`
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

	if strings.TrimSpace(in.Query) == "" {
		return source.Result{}, &source.ToolError{Message: "query: required"}
	}

	query := url.Values{
		"query": {in.Query},
		"start": {timestamp(w.start)},
		"end":   {timestamp(w.end)},
		"step":  {strconv.Itoa(step(in.Step, w)) + "s"},
	}

	target := s.apis.prometheus + "/projects/" + url.PathEscape(project) + "/location/global/prometheus/api/v1/query_range"

	var answer promAnswer
	if err := s.client.get(ctx, target, query, &answer); err != nil {
		return source.Result{}, fmt.Errorf("run the PromQL query in %s: %w", project, err)
	}

	if answer.Status != "success" {
		return source.Result{}, &source.ToolError{Message: "the PromQL query failed: " + source.CutText(answer.Error, maxMessageLen)}
	}

	limit := limitOf(in.Limit, defaultSeries, maxSeries)
	out := promResult{Project: project, Series: make([]promSeries, 0, min(len(answer.Data.Result), limit))}
	b := budget{}
	cut := len(answer.Data.Result) > limit

	for _, warning := range answer.Warnings {
		out.Warnings = append(out.Warnings, source.CutText(warning, maxNameLen))
	}

	for _, r := range answer.Data.Result[:min(len(answer.Data.Result), limit)] {
		labels, labelsCut := cutLabels(r.Metric)
		item := promSeries{Labels: labels}
		values := r.Values

		if len(values) > maxPoints {
			values = values[len(values)-maxPoints:]
			cut = true
		}

		for _, v := range values {
			item.Points = append(item.Points, promValue(v))
		}

		if !b.fits(item) {
			break
		}

		cut = cut || labelsCut
		out.Series = append(out.Series, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceMetric,
		Title: "Metrics Explorer in " + project,
		URL:   console(metricsExplorer, project),
		At:    w.end,
	}}

	empty := fmt.Sprintf("The PromQL query returned no series in %s between %s and %s.", project, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, cut || b.cut, len(out.Series) == 0, empty)
}

// promValue returns one Prometheus point, [<unix seconds>, "<value>"].
func promValue(v [2]any) promPoint {
	var p promPoint

	if secs, ok := v[0].(float64); ok {
		whole, frac := math.Modf(secs)
		p.T = time.Unix(int64(whole), int64(frac*float64(time.Second))).UTC()
	}

	if s, ok := v[1].(string); ok {
		p.V = source.CutText(s, maxNameLen)
	}

	return p
}

// step returns the step a call gives, at least minStep seconds, or one
// that splits the window into about maxPoints points.
func step(given int, w window) int {
	if given <= 0 {
		given = int(math.Ceil(w.end.Sub(w.start).Seconds() / maxPoints))
	}

	return max(given, minStep)
}

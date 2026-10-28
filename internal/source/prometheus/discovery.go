package prometheus

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/promql"
)

// labelNamePattern is the shape of a label name that list_label_values
// takes: the classic Prometheus label name syntax, which needs no escaping
// in the API path.
var labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// healthUp is the health of a target whose last scrape worked.
const healthUp = "up"

// seriesResult is the result of list_series.
type seriesResult struct {
	Series []map[string]string `json:"series"`
}

// labelValuesResult is the result of list_label_values.
type labelValuesResult struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// promTarget is one active target of the Prometheus targets API.
type promTarget struct {
	Labels     map[string]string `json:"labels"`
	ScrapePool string            `json:"scrapePool"`
	ScrapeURL  string            `json:"scrapeUrl"`
	LastError  string            `json:"lastError"`
	LastScrape *time.Time        `json:"lastScrape"`
	Health     string            `json:"health"`
}

// targetItem is one target in the result of list_targets.
type targetItem struct {
	Pool       string            `json:"pool"`
	URL        string            `json:"url"`
	Health     string            `json:"health"`
	LastError  string            `json:"lastError,omitempty"`
	LastScrape *time.Time        `json:"lastScrape,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// targetsResult is the result of list_targets.
type targetsResult struct {
	// Active is how many active targets the pool, or the server, has.
	Active int `json:"active"`
	// Unhealthy is how many of them are not up, before the cut to the
	// limit.
	Unhealthy int          `json:"unhealthy"`
	Targets   []targetItem `json:"targets"`
}

// listSeries runs list_series.
func (s *Source) listSeries(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Match string `json:"match"`
		Since string `json:"since"`
		Until string `json:"until"`
		Limit int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := s.checkSelector("match", in.Match); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit, defaultSeriesList, maxSeriesList)
	query := url.Values{
		"match[]": {in.Match},
		"start":   {timestamp(w.start)},
		"end":     {timestamp(w.end)},
		"limit":   {strconv.Itoa(limit + 1)},
	}

	var answer promql.Answer[[]map[string]string]
	if err := s.prom.get(ctx, "/api/v1/series", query, &answer); err != nil {
		return source.Result{}, fmt.Errorf("read the series: %w", err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	out := seriesResult{Series: make([]map[string]string, 0, min(len(answer.Data), limit))}
	b := budget{}
	cut := len(answer.Data) > limit

	for _, l := range answer.Data[:min(len(answer.Data), limit)] {
		labels, labelsCut := cutLabels(l)
		if !b.fits(labels) {
			break
		}

		cut = cut || labelsCut
		out.Series = append(out.Series, labels)
	}

	evidence := []source.Evidence{s.links.query("Series "+source.CutText(in.Match, maxNameLen), in.Match, w, false)}
	empty := fmt.Sprintf("No series match %s between %s and %s.", source.CutText(in.Match, maxNameLen), timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, cut || b.cut, len(answer.Data) == 0, empty)
}

// listLabelValues runs list_label_values.
func (s *Source) listLabelValues(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Label string `json:"label"`
		Match string `json:"match"`
		Since string `json:"since"`
		Until string `json:"until"`
		Limit int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if !labelNamePattern.MatchString(in.Label) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("label: %q is not a label name such as service or __name__",
			source.CutText(in.Label, maxNameLen))}
	}

	w, err := s.window(in.Since, in.Until)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit, defaultValues, maxValues)
	query := url.Values{
		"start": {timestamp(w.start)},
		"end":   {timestamp(w.end)},
		"limit": {strconv.Itoa(limit + 1)},
	}

	if in.Match != "" {
		if err := s.checkSelector("match", in.Match); err != nil {
			return source.Result{}, fmt.Errorf("read the input: %w", err)
		}

		query.Set("match[]", in.Match)
	}

	var answer promql.Answer[[]string]
	if err := s.prom.get(ctx, "/api/v1/label/"+in.Label+"/values", query, &answer); err != nil {
		return source.Result{}, fmt.Errorf("read the values of %s: %w", in.Label, err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	out := labelValuesResult{Label: in.Label, Values: make([]string, 0, min(len(answer.Data), limit))}
	b := budget{}
	cut := len(answer.Data) > limit

	for _, v := range answer.Data[:min(len(answer.Data), limit)] {
		value := source.CutText(v, maxNameLen)
		if !b.fits(value) {
			break
		}

		cut = cut || len(value) < len(v)
		out.Values = append(out.Values, value)
	}

	empty := fmt.Sprintf("The label %s has no values between %s and %s.", in.Label, timestamp(w.start), timestamp(w.end))

	return structured(out, nil, cut || b.cut, len(answer.Data) == 0, empty)
}

// listTargets runs list_targets: the active targets that are not up.
func (s *Source) listTargets(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Pool  string `json:"pool"`
		Limit int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	query := url.Values{"state": {"active"}}
	if in.Pool != "" {
		query.Set("scrapePool", in.Pool)
	}

	var answer promql.Answer[struct {
		ActiveTargets []promTarget `json:"activeTargets"`
	}]
	if err := s.prom.get(ctx, "/api/v1/targets", query, &answer); err != nil {
		return source.Result{}, fmt.Errorf("read the targets: %w", err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	var (
		active    int
		unhealthy []promTarget
	)

	for _, t := range answer.Data.ActiveTargets {
		if in.Pool != "" && t.ScrapePool != in.Pool {
			continue
		}

		active++

		if t.Health != healthUp {
			unhealthy = append(unhealthy, t)
		}
	}

	slices.SortFunc(unhealthy, func(a, b promTarget) int {
		return cmp.Or(cmp.Compare(a.ScrapePool, b.ScrapePool), cmp.Compare(a.ScrapeURL, b.ScrapeURL))
	})

	limit := limitOf(in.Limit, defaultTargets, maxTargets)
	out := targetsResult{Active: active, Unhealthy: len(unhealthy), Targets: make([]targetItem, 0, min(len(unhealthy), limit))}
	b := budget{}

	for _, t := range unhealthy[:min(len(unhealthy), limit)] {
		labels, _ := cutLabels(t.Labels)
		item := targetItem{
			Pool:       source.CutText(t.ScrapePool, maxNameLen),
			URL:        source.CutText(t.ScrapeURL, maxNameLen),
			Health:     source.CutText(t.Health, maxNameLen),
			LastError:  source.CutText(t.LastError, maxMessageLen),
			LastScrape: t.LastScrape,
			Labels:     labels,
		}

		if !b.fits(item) {
			break
		}

		out.Targets = append(out.Targets, item)
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "Targets in Prometheus", URL: s.links.prometheus + "/targets"}}
	empty := fmt.Sprintf("All %d active targets are up.", active)

	switch {
	case active == 0 && in.Pool != "":
		empty = fmt.Sprintf("The scrape pool %q has no active targets.", source.CutText(in.Pool, maxNameLen))
	case active == 0:
		empty = "The server has no active scrape targets."
	}

	return structured(out, evidence, len(unhealthy) > limit || b.cut, len(unhealthy) == 0, empty)
}

// checkSelector parses a series selector, such as up{job="api"}, with the
// Prometheus parser, so one that does not parse is a *source.ToolError and
// is never sent.
func (s *Source) checkSelector(key, selector string) error {
	if strings.TrimSpace(selector) == "" {
		return &source.ToolError{Message: key + ": required"}
	}

	if _, err := s.parser.ParseMetricSelector(selector); err != nil {
		return &source.ToolError{Message: key + ": " + source.CutText(err.Error(), maxMessageLen)}
	}

	return nil
}

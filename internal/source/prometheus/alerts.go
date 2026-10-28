package prometheus

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/promql"
)

// maxExpressionLen bounds the PromQL expression of a rule. It is longer
// than other text, because the model reruns the expression.
const maxExpressionLen = 16 << 10

// Where list_alerts read the alerts.
const (
	fromPrometheus   = "prometheus"
	fromAlertmanager = "alertmanager"
)

// alertStateRank orders alerts by state: firing, or active in Alertmanager,
// first.
var alertStateRank = map[string]int{"firing": 0, "active": 0, "pending": 1, "unprocessed": 1, "suppressed": 2}

// promAlert is one alert of the Prometheus alerts API.
type promAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    *time.Time        `json:"activeAt"`
	Value       string            `json:"value"`
}

// amAlert is one alert of the Alertmanager v2 API.
type amAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    *time.Time        `json:"startsAt"`
	Status      struct {
		State       string   `json:"state"`
		SilencedBy  []string `json:"silencedBy"`
		InhibitedBy []string `json:"inhibitedBy"`
	} `json:"status"`
}

// amSilence is one silence of the Alertmanager v2 API.
type amSilence struct {
	ID       string `json:"id"`
	Matchers []struct {
		Name    string `json:"name"`
		Value   string `json:"value"`
		IsRegex bool   `json:"isRegex"`
		// IsEqual is missing from older Alertmanagers, where every
		// matcher is an equality.
		IsEqual *bool `json:"isEqual"`
	} `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
}

// alertItem is one alert in the result of list_alerts.
type alertItem struct {
	Name        string            `json:"name"`
	State       string            `json:"state"`
	Since       *time.Time        `json:"since,omitempty"`
	Value       any               `json:"value,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	SilencedBy  []string          `json:"silencedBy,omitempty"`
	InhibitedBy []string          `json:"inhibitedBy,omitempty"`
}

// silenceItem is one silence in the result of list_alerts.
type silenceItem struct {
	ID        string    `json:"id"`
	Matchers  []string  `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
	Comment   string    `json:"comment,omitempty"`
}

// alertsResult is the result of list_alerts.
type alertsResult struct {
	From string `json:"from"`
	// Total is how many alerts matched, before the cut to the limit.
	Total    int           `json:"total"`
	Alerts   []alertItem   `json:"alerts"`
	Silences []silenceItem `json:"silences,omitempty"`
}

// rulesData is the data of an answer of the Prometheus rules API.
type rulesData struct {
	Groups []struct {
		Name  string     `json:"name"`
		File  string     `json:"file"`
		Rules []promRule `json:"rules"`
	} `json:"groups"`
}

// promRule is one rule of the Prometheus rules API.
type promRule struct {
	Type           string            `json:"type"`
	Name           string            `json:"name"`
	Query          string            `json:"query"`
	Duration       float64           `json:"duration"`
	KeepFiringFor  float64           `json:"keepFiringFor"`
	Labels         map[string]string `json:"labels"`
	Annotations    map[string]string `json:"annotations"`
	Health         string            `json:"health"`
	LastError      string            `json:"lastError"`
	LastEvaluation *time.Time        `json:"lastEvaluation"`
	State          string            `json:"state"`
	Alerts         []struct{}        `json:"alerts"`
}

// ruleItem is one rule in the result of get_rules.
type ruleItem struct {
	Group          string            `json:"group"`
	File           string            `json:"file,omitempty"`
	Name           string            `json:"name"`
	Expression     string            `json:"expression"`
	For            string            `json:"for,omitempty"`
	KeepFiringFor  string            `json:"keepFiringFor,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	Health         string            `json:"health,omitempty"`
	LastError      string            `json:"lastError,omitempty"`
	LastEvaluation *time.Time        `json:"lastEvaluation,omitempty"`
	State          string            `json:"state,omitempty"`
	ActiveAlerts   int               `json:"activeAlerts"`
}

// rulesResult is the result of get_rules.
type rulesResult struct {
	Rules []ruleItem `json:"rules"`
}

// listAlerts runs list_alerts: the alerts of Alertmanager when the config
// has one, otherwise those of Prometheus.
func (s *Source) listAlerts(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Alertname string `json:"alertname"`
		Limit     int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit, defaultAlerts, maxAlerts)

	if s.alertmanager != nil {
		return s.alertmanagerAlerts(ctx, in.Alertname, limit)
	}

	var answer promql.Answer[struct {
		Alerts []promAlert `json:"alerts"`
	}]
	if err := s.prom.get(ctx, "/api/v1/alerts", nil, &answer); err != nil {
		return source.Result{}, fmt.Errorf("read the alerts: %w", err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	var items []alertItem

	for _, a := range answer.Data.Alerts {
		if in.Alertname != "" && a.Labels["alertname"] != in.Alertname {
			continue
		}

		item := newAlertItem(a.Labels, a.Annotations, a.State, a.ActiveAt)
		if a.Value != "" {
			item.Value = sampleValue(a.Value)
		}

		items = append(items, item)
	}

	out, cut := alertsOf(fromPrometheus, items, limit)
	empty := "No alerts are firing or pending in Prometheus."

	if in.Alertname != "" {
		empty = fmt.Sprintf("No alert named %q is firing or pending in Prometheus.", source.CutText(in.Alertname, maxNameLen))
	}

	return structured(out, []source.Evidence{s.links.alerts()}, cut, len(items) == 0, empty)
}

// alertmanagerAlerts reads the alerts of Alertmanager, and the silences
// that mute the alerts it keeps.
func (s *Source) alertmanagerAlerts(ctx context.Context, alertname string, limit int) (source.Result, error) {
	query := url.Values{"active": {"true"}, "silenced": {"true"}, "inhibited": {"true"}, "unprocessed": {"true"}}

	var alerts []amAlert
	if err := s.alertmanager.get(ctx, "/api/v2/alerts", query, &alerts); err != nil {
		return source.Result{}, fmt.Errorf("read the Alertmanager alerts: %w", err)
	}

	var items []alertItem

	for _, a := range alerts {
		if alertname != "" && a.Labels["alertname"] != alertname {
			continue
		}

		item := newAlertItem(a.Labels, a.Annotations, a.Status.State, a.StartsAt)
		item.SilencedBy = cutNames(a.Status.SilencedBy)
		item.InhibitedBy = cutNames(a.Status.InhibitedBy)
		items = append(items, item)
	}

	out, cut := alertsOf(fromAlertmanager, items, limit)

	silenced := map[string]bool{}
	for _, a := range out.Alerts {
		for _, id := range a.SilencedBy {
			silenced[id] = true
		}
	}

	if len(silenced) > 0 {
		silences, silencesCut, err := s.silences(ctx, silenced, maxResultSize-out.size())
		if err != nil {
			return source.Result{}, fmt.Errorf("read the silences: %w", err)
		}

		out.Silences = silences
		cut = cut || silencesCut
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "Alerts in Alertmanager", URL: s.alertmanager.root + "/#/alerts"}}
	empty := "Alertmanager has no alerts."

	if alertname != "" {
		empty = fmt.Sprintf("Alertmanager has no alert named %q.", source.CutText(alertname, maxNameLen))
	}

	return structured(out, evidence, cut, len(items) == 0, empty)
}

// silences returns the silences of Alertmanager whose IDs are in ids, in
// at most room bytes, and reports whether some were left out.
func (s *Source) silences(ctx context.Context, ids map[string]bool, room int) ([]silenceItem, bool, error) {
	var all []amSilence
	if err := s.alertmanager.get(ctx, "/api/v2/silences", nil, &all); err != nil {
		return nil, false, fmt.Errorf("list the silences: %w", err)
	}

	b := budget{used: maxResultSize - room}

	var out []silenceItem

	for _, sl := range all {
		if !ids[sl.ID] {
			continue
		}

		item := silenceItem{
			ID: source.CutText(sl.ID, maxNameLen), StartsAt: sl.StartsAt, EndsAt: sl.EndsAt,
			CreatedBy: source.CutText(sl.CreatedBy, maxNameLen), Comment: source.CutText(sl.Comment, maxMessageLen),
		}

		for _, m := range sl.Matchers {
			item.Matchers = append(item.Matchers, matcherText(m.Name, m.Value, m.IsRegex, m.IsEqual == nil || *m.IsEqual))
		}

		if !b.fits(item) {
			break
		}

		out = append(out, item)
	}

	return out, b.cut, nil
}

// getRules runs get_rules: the alerting rules with one name.
func (s *Source) getRules(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Alertname string `json:"alertname"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if strings.TrimSpace(in.Alertname) == "" {
		return source.Result{}, &source.ToolError{Message: "alertname: required"}
	}

	var answer promql.Answer[rulesData]

	query := url.Values{"type": {"alert"}, "rule_name[]": {in.Alertname}}
	if err := s.prom.get(ctx, "/api/v1/rules", query, &answer); err != nil {
		return source.Result{}, fmt.Errorf("read the rules: %w", err)
	}

	if answer.Status != promql.StatusSuccess {
		return source.Result{}, answerError(answer)
	}

	out := rulesResult{}
	b := budget{}
	cut := false

	for _, g := range answer.Data.Groups {
		for _, r := range g.Rules {
			if r.Name != in.Alertname || (r.Type != "" && r.Type != "alerting") {
				continue
			}

			if len(out.Rules) == maxRules {
				cut = true

				break
			}

			item := newRuleItem(g.Name, g.File, r)
			if !b.fits(item) {
				break
			}

			cut = cut || len(item.Expression) < len(r.Query)
			out.Rules = append(out.Rules, item)
		}
	}

	now := s.now().UTC()

	var evidence []source.Evidence
	if len(out.Rules) > 0 {
		evidence = append(evidence, s.links.query("Alert rule "+source.CutText(in.Alertname, maxNameLen), out.Rules[0].Expression,
			window{start: now.Add(-defaultRange), end: now}, false))
	}

	empty := fmt.Sprintf("No alerting rule is named %q.", source.CutText(in.Alertname, maxNameLen))

	return structured(out, evidence, cut || b.cut, len(out.Rules) == 0, empty)
}

// size returns the length of the result's JSON.
func (r alertsResult) size() int {
	data, err := json.Marshal(r)
	if err != nil {
		return maxResultSize
	}

	return len(data)
}

// alerts returns the evidence of the alert list: Grafana's alert rules
// when a Grafana is set, otherwise the Prometheus alerts page.
func (l links) alerts() source.Evidence {
	if l.grafana != "" {
		return source.Evidence{Kind: source.EvidenceResource, Title: "Alert rules in Grafana", URL: l.grafana + "/alerting/list"}
	}

	return source.Evidence{Kind: source.EvidenceResource, Title: "Alerts in Prometheus", URL: l.prometheus + "/alerts"}
}

// newAlertItem returns an alert item, its name taken out of its labels and
// its labels and annotations cut.
func newAlertItem(labels, annotations map[string]string, state string, since *time.Time) alertItem {
	labels = maps.Clone(labels)
	name := labels["alertname"]
	delete(labels, "alertname")

	labels, _ = cutLabels(labels)

	return alertItem{
		Name:        source.CutText(name, maxNameLen),
		State:       source.CutText(state, maxNameLen),
		Since:       since,
		Labels:      labels,
		Annotations: cutAnnotations(annotations),
	}
}

// alertsOf returns the result of a list of alerts: sorted firing first,
// then newest first, and cut to limit and to maxResultSize. It reports
// whether the list was cut.
func alertsOf(from string, items []alertItem, limit int) (alertsResult, bool) {
	slices.SortStableFunc(items, func(a, b alertItem) int {
		return cmp.Or(
			cmp.Compare(rank(a.State), rank(b.State)),
			compareNewest(a.Since, b.Since),
			cmp.Compare(a.Name, b.Name),
		)
	})

	out := alertsResult{From: from, Total: len(items), Alerts: make([]alertItem, 0, min(len(items), limit))}
	b := budget{}

	for _, item := range items[:min(len(items), limit)] {
		if !b.fits(item) {
			break
		}

		out.Alerts = append(out.Alerts, item)
	}

	return out, len(items) > limit || b.cut
}

// newRuleItem returns the rule item of an alerting rule, with its
// expression and texts cut.
func newRuleItem(group, file string, r promRule) ruleItem {
	labels, _ := cutLabels(maps.Clone(r.Labels))

	return ruleItem{
		Group:          source.CutText(group, maxNameLen),
		File:           source.CutText(file, maxNameLen),
		Name:           source.CutText(r.Name, maxNameLen),
		Expression:     source.CutText(r.Query, maxExpressionLen),
		For:            durationText(r.Duration),
		KeepFiringFor:  durationText(r.KeepFiringFor),
		Labels:         labels,
		Annotations:    cutAnnotations(r.Annotations),
		Health:         source.CutText(r.Health, maxNameLen),
		LastError:      source.CutText(r.LastError, maxMessageLen),
		LastEvaluation: r.LastEvaluation,
		State:          source.CutText(r.State, maxNameLen),
		ActiveAlerts:   len(r.Alerts),
	}
}

// rank returns the sort rank of an alert state; unknown states go last.
func rank(state string) int {
	if r, ok := alertStateRank[state]; ok {
		return r
	}

	return len(alertStateRank)
}

// compareNewest orders a later time first, and a missing time last.
func compareNewest(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	default:
		return b.Compare(*a)
	}
}

// matcherText returns an Alertmanager matcher as text, such as
// service="checkout" or pod=~"api-.*".
func matcherText(name, value string, regex, equal bool) string {
	op := "="

	switch {
	case regex && equal:
		op = "=~"
	case regex:
		op = "!~"
	case !equal:
		op = "!="
	}

	return source.CutText(name, maxNameLen) + op + strconv.Quote(source.CutText(value, maxNameLen))
}

// durationText returns a duration given in seconds as text, such as 5m0s,
// or "" for zero.
func durationText(secs float64) string {
	if secs <= 0 {
		return ""
	}

	return time.Duration(secs * float64(time.Second)).String()
}

// cutAnnotations returns a copy of annotations with each value cut to
// maxMessageLen.
func cutAnnotations(annotations map[string]string) map[string]string {
	if len(annotations) == 0 {
		return nil
	}

	out := make(map[string]string, len(annotations))
	for k, v := range annotations {
		out[source.CutText(k, maxNameLen)] = source.CutText(v, maxMessageLen)
	}

	return out
}

// cutNames returns names with each one cut to maxNameLen.
func cutNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}

	out := make([]string, len(names))
	for i, n := range names {
		out[i] = source.CutText(n, maxNameLen)
	}

	return out
}

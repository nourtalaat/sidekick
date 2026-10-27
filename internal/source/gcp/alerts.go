package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultAlertRange is the time range of list_alerts when the call gives
// no since.
const defaultAlertRange = 24 * time.Hour

// Paging of the alerts list, which Cloud Monitoring sorts by open time,
// newest first, and which the source filters by time itself.
const (
	// alertPageSize is how many alerts one page asks for.
	alertPageSize = 100
	// maxAlertPages is the most pages one call reads.
	maxAlertPages = 5
)

// Alert states as Cloud Monitoring names them.
const (
	alertOpen   = "OPEN"
	alertClosed = "CLOSED"
)

// alertsPage is one answer of alerts.list.
type alertsPage struct {
	Alerts        []alert `json:"alerts"`
	NextPageToken string  `json:"nextPageToken"`
}

// alert is the part of a Cloud Monitoring alert the source reads.
type alert struct {
	Name      string    `json:"name"`
	State     string    `json:"state"`
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Resource  resource  `json:"resource"`
	Metric    resource  `json:"metric"`
	Policy    struct {
		Name        string            `json:"name"`
		DisplayName string            `json:"displayName"`
		Severity    string            `json:"severity"`
		UserLabels  map[string]string `json:"userLabels"`
	} `json:"policy"`
}

// alertItem is one alert in the result of list_alerts.
type alertItem struct {
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Opened   time.Time         `json:"opened"`
	Closed   time.Time         `json:"closed,omitzero"`
	Policy   string            `json:"policy"`
	Severity string            `json:"severity,omitempty"`
	Labels   map[string]string `json:"policyLabels,omitempty"`
	Metric   string            `json:"metric,omitempty"`
	Resource resource          `json:"resource"`
	URL      string            `json:"url"`
}

// alertsResult is the result of list_alerts.
type alertsResult struct {
	Project string      `json:"project"`
	Alerts  []alertItem `json:"alerts"`
}

// listAlerts runs list_alerts. It keeps the alerts that were open at some
// time in the window and are in the state the call asks for.
func (s *Source) listAlerts(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project string `json:"project"`
		State   string `json:"state"`
		Since   string `json:"since"`
		Until   string `json:"until"`
		Limit   int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.project(in.Project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultAlertRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	alerts, more, err := s.readAlerts(ctx, project, in.State, w, limitOf(in.Limit, defaultItems, maxItems))
	if err != nil {
		return source.Result{}, fmt.Errorf("list the alerts of %s: %w", project, err)
	}

	out := alertsResult{Project: project, Alerts: make([]alertItem, 0, len(alerts))}
	evidence := make([]source.Evidence, 0, len(alerts))
	b := budget{}

	for _, a := range alerts {
		labels, _ := cutLabels(a.Policy.UserLabels)
		item := alertItem{
			ID:       lastPart(a.Name),
			State:    a.State,
			Opened:   a.OpenTime,
			Closed:   a.CloseTime,
			Policy:   source.CutText(a.Policy.DisplayName, maxNameLen),
			Severity: a.Policy.Severity,
			Labels:   labels,
			Metric:   a.Metric.Type,
			Resource: a.Resource,
			URL:      console("/monitoring/alerting/incidents/"+url.PathEscape(lastPart(a.Name)), project),
		}

		if !b.fits(item) {
			break
		}

		out.Alerts = append(out.Alerts, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceResource,
			Title: fmt.Sprintf("Alert %s in %s", item.Policy, project),
			URL:   item.URL,
			At:    a.OpenTime,
		})
	}

	empty := fmt.Sprintf("No alerts in %s were open between %s and %s.", project, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, more || b.cut, len(out.Alerts) == 0, empty)
}

// readAlerts reads the alerts of project page by page, newest first, and
// keeps up to limit that match state and were open at some time in w.
func (s *Source) readAlerts(ctx context.Context, project, state string, w window, limit int) (alerts []alert, more bool, err error) {
	target := s.apis.monitoring + "/projects/" + url.PathEscape(project) + "/alerts"
	query := url.Values{"pageSize": {strconv.Itoa(alertPageSize)}}

	for range maxAlertPages {
		var page alertsPage
		if err := s.client.get(ctx, target, query, &page); err != nil {
			return nil, false, fmt.Errorf("read a page of alerts: %w", err)
		}

		for _, a := range page.Alerts {
			if !a.OpenTime.Before(w.end) {
				continue
			}

			if a.State == alertClosed && !a.CloseTime.IsZero() && a.CloseTime.Before(w.start) {
				continue
			}

			if !stateMatches(state, a.State) {
				continue
			}

			if len(alerts) == limit {
				return alerts, true, nil
			}

			alerts = append(alerts, a)
		}

		if page.NextPageToken == "" {
			return alerts, false, nil
		}

		query.Set("pageToken", page.NextPageToken)
	}

	return alerts, true, nil
}

// stateMatches reports whether an alert in state passes the call's state
// filter: open, closed or any.
func stateMatches(filter, state string) bool {
	switch filter {
	case stateOpen:
		return state == alertOpen
	case stateClosed:
		return state == alertClosed
	default:
		return true
	}
}

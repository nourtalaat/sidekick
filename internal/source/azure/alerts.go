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

// defaultAlertRange is the time range of list_alerts when the call gives
// no since.
const defaultAlertRange = 24 * time.Hour

// alertsPage is one page of the alerts list.
type alertsPage struct {
	Value []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			Essentials struct {
				Severity           string    `json:"severity"`
				SignalType         string    `json:"signalType"`
				AlertState         string    `json:"alertState"`
				MonitorCondition   string    `json:"monitorCondition"`
				MonitorService     string    `json:"monitorService"`
				TargetResource     string    `json:"targetResource"`
				TargetResourceType string    `json:"targetResourceType"`
				AlertRule          string    `json:"alertRule"`
				Description        string    `json:"description"`
				StartDateTime      time.Time `json:"startDateTime"`
				LastModifiedTime   time.Time `json:"lastModifiedDateTime"`
				ResolvedTime       time.Time `json:"monitorConditionResolvedDateTime"`
			} `json:"essentials"`
		} `json:"properties"`
	} `json:"value"`
	NextLink string `json:"nextLink"`
}

// alertItem is one alert in the result of list_alerts.
type alertItem struct {
	Name        string    `json:"name"`
	Severity    string    `json:"severity,omitempty"`
	State       string    `json:"state,omitempty"`
	Condition   string    `json:"condition,omitempty"`
	Service     string    `json:"service,omitempty"`
	Signal      string    `json:"signal,omitempty"`
	Rule        string    `json:"rule,omitempty"`
	Target      string    `json:"target,omitempty"`
	TargetType  string    `json:"targetType,omitempty"`
	Description string    `json:"description,omitempty"`
	Started     time.Time `json:"started"`
	Resolved    time.Time `json:"resolved,omitzero"`
	URL         string    `json:"url"`
}

// alertsResult is the result of list_alerts.
type alertsResult struct {
	Subscription string      `json:"subscription"`
	Alerts       []alertItem `json:"alerts"`
}

// listAlerts runs list_alerts.
func (s *Source) listAlerts(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Subscription  string `json:"subscription"`
		ResourceGroup string `json:"resourceGroup"`
		Resource      string `json:"resource"`
		State         string `json:"state"`
		Since         string `json:"since"`
		Until         string `json:"until"`
		Limit         int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	sub, err := s.subscription(in.Subscription)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultAlertRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit, defaultItems, maxItems)
	params := url.Values{
		"customTimeRange": {w.timespan()},
		"pageCount":       {strconv.Itoa(limit)},
		"sortBy":          {"startDateTime"},
		"sortOrder":       {"desc"},
	}

	if err := s.alertFilters(params, in.ResourceGroup, in.Resource, in.State); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	var page alertsPage

	target := s.apis.arm + "/subscriptions/" + url.PathEscape(sub) + "/providers/Microsoft.AlertsManagement/alerts"
	if err := s.client.get(ctx, armScope, target, query(alertsVersion, params), &page); err != nil {
		return source.Result{}, fmt.Errorf("list the alerts of %s: %w", sub, err)
	}

	out := alertsResult{Subscription: sub, Alerts: make([]alertItem, 0, len(page.Value))}
	evidence := make([]source.Evidence, 0, len(page.Value))
	b := budget{}

	for _, a := range page.Value[:min(len(page.Value), limit)] {
		e := a.Properties.Essentials
		item := alertItem{
			Name:        source.CutText(a.Name, maxNameLen),
			Severity:    e.Severity,
			State:       e.AlertState,
			Condition:   e.MonitorCondition,
			Service:     e.MonitorService,
			Signal:      e.SignalType,
			Rule:        source.CutText(e.AlertRule, maxNameLen),
			Target:      source.CutText(e.TargetResource, maxNameLen),
			TargetType:  e.TargetResourceType,
			Description: source.CutText(e.Description, maxMessageLen),
			Started:     e.StartDateTime,
			Resolved:    e.ResolvedTime,
			URL:         portalURL + "/#view/Microsoft_Azure_Monitoring_Alerts/AlertDetails.ReactView/alertId~/" + url.PathEscape(a.ID),
		}

		if !b.fits(item) {
			break
		}

		out.Alerts = append(out.Alerts, item)
		evidence = append(evidence, source.Evidence{Kind: source.EvidenceResource, Title: "Alert " + item.Name, URL: item.URL, At: item.Started})
	}

	empty := fmt.Sprintf("No alerts in subscription %s started between %s and %s.", sub, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, page.NextLink != "" || len(page.Value) > limit || b.cut, len(out.Alerts) == 0, empty)
}

// alertFilters adds the optional filters of a call to params: a resource
// group or a resource, and the monitor condition.
func (s *Source) alertFilters(params url.Values, group, resource, state string) error {
	group = strings.TrimSpace(group)
	resource = strings.TrimSpace(resource)

	switch {
	case group != "" && resource != "":
		return &source.ToolError{Message: "give resourceGroup or resource, not both"}
	case resource != "":
		id, err := s.resource("resource", resource)
		if err != nil {
			return fmt.Errorf("check the resource: %w", err)
		}

		params.Set("targetResource", id)
	case group != "":
		if err := checkGroup(group); err != nil {
			return fmt.Errorf("check the resource group: %w", err)
		}

		params.Set("targetResourceGroup", group)
	}

	switch state {
	case stateFired:
		params.Set("monitorCondition", "Fired")
	case stateResolved:
		params.Set("monitorCondition", "Resolved")
	}

	return nil
}

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultHealthRange is how far back get_resource_health looks for service
// health events when the call gives no since.
const defaultHealthRange = 24 * time.Hour

// available is the availability state of a healthy resource.
const available = "Available"

// statusSuffix ends the ID of a resource's current availability status.
const statusSuffix = "/providers/microsoft.resourcehealth/availabilitystatuses/current"

// serviceHealthPortal is the portal page of Azure service issues.
const serviceHealthPortal = portalURL + "/#view/Microsoft_Azure_Health/AzureHealthBrowseBlade/~/serviceIssues"

// availabilityStatus is the part of a Resource Health status the source
// reads.
type availabilityStatus struct {
	ID         string `json:"id"`
	Properties struct {
		AvailabilityState string    `json:"availabilityState"`
		Title             string    `json:"title"`
		Summary           string    `json:"summary"`
		DetailedStatus    string    `json:"detailedStatus"`
		ReasonType        string    `json:"reasonType"`
		Context           string    `json:"context"`
		Category          string    `json:"category"`
		OccurredTime      time.Time `json:"occuredTime"`
		ReportedTime      time.Time `json:"reportedTime"`
		ResolutionETA     time.Time `json:"resolutionETA"`
	} `json:"properties"`
}

// healthEvent is the part of a service health event the source reads.
type healthEvent struct {
	Name       string `json:"name"`
	Properties struct {
		Title           string    `json:"title"`
		Summary         string    `json:"summary"`
		EventType       string    `json:"eventType"`
		Status          string    `json:"status"`
		Level           string    `json:"level"`
		ImpactStartTime time.Time `json:"impactStartTime"`
		LastUpdateTime  time.Time `json:"lastUpdateTime"`
		Impact          []struct {
			ImpactedService string `json:"impactedService"`
			ImpactedRegions []struct {
				ImpactedRegion string `json:"impactedRegion"`
			} `json:"impactedRegions"`
		} `json:"impact"`
	} `json:"properties"`
}

// statusItem is one resource's health in a result.
type statusItem struct {
	Resource string    `json:"resource"`
	State    string    `json:"state"`
	Title    string    `json:"title,omitempty"`
	Summary  string    `json:"summary,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Context  string    `json:"context,omitempty"`
	Since    time.Time `json:"since,omitzero"`
	Reported time.Time `json:"reported,omitzero"`
	ETA      time.Time `json:"resolutionETA,omitzero"`
}

// eventItem is one service health event in a result.
type eventItem struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Type     string    `json:"type,omitempty"`
	Status   string    `json:"status,omitempty"`
	Level    string    `json:"level,omitempty"`
	Services []string  `json:"services,omitempty"`
	Regions  []string  `json:"regions,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Updated  time.Time `json:"updated,omitzero"`
	Summary  string    `json:"summary,omitempty"`
}

// healthResult is the result of get_resource_health without a resource.
type healthResult struct {
	Subscription string       `json:"subscription"`
	Unhealthy    []statusItem `json:"unhealthy"`
	Events       []eventItem  `json:"serviceHealthEvents"`
}

// getResourceHealth runs get_resource_health.
func (s *Source) getResourceHealth(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Subscription string `json:"subscription"`
		Resource     string `json:"resource"`
		Since        string `json:"since"`
		Until        string `json:"until"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if strings.TrimSpace(in.Resource) != "" {
		id, err := s.resource("resource", in.Resource)
		if err != nil {
			return source.Result{}, fmt.Errorf("read the input: %w", err)
		}

		return s.resourceHealth(ctx, id)
	}

	sub, err := s.subscription(in.Subscription)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultHealthRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	return s.subscriptionHealth(ctx, sub, w)
}

// resourceHealth returns the current availability of one resource.
func (s *Source) resourceHealth(ctx context.Context, id string) (source.Result, error) {
	var status availabilityStatus

	target := s.apis.arm + id + "/providers/Microsoft.ResourceHealth/availabilityStatuses/current"
	if err := s.client.get(ctx, armScope, target, query(resourceHealthVersion, nil), &status); err != nil {
		return source.Result{}, fmt.Errorf("read the health of %s: %w", id, err)
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "Resource Health of " + lastPart(id), URL: portal(id, "/resourceHealth")}}

	return structured(statusOf(status, id), evidence, false, false, "")
}

// subscriptionHealth returns the resources of a subscription that are not
// available, and the service health events updated in w.
func (s *Source) subscriptionHealth(ctx context.Context, sub string, w window) (source.Result, error) {
	base := s.apis.arm + "/subscriptions/" + url.PathEscape(sub) + "/providers/Microsoft.ResourceHealth"
	out := healthResult{Subscription: sub, Unhealthy: []statusItem{}, Events: []eventItem{}}
	b := budget{}

	statuses, more, err := s.readStatuses(ctx, base+"/availabilityStatuses")
	if err != nil {
		return source.Result{}, fmt.Errorf("list the health of the resources in %s: %w", sub, err)
	}

	for _, st := range statuses {
		item := statusOf(st, "")
		if item.State == available || !b.fits(item) {
			continue
		}

		out.Unhealthy = append(out.Unhealthy, item)
	}

	var events struct {
		Value []healthEvent `json:"value"`
	}

	params := url.Values{"queryStartTime": {w.start.Format("1/2/2006")}}
	if err := s.client.get(ctx, armScope, base+"/events", query(resourceHealthVersion, params), &events); err != nil {
		return source.Result{}, fmt.Errorf("list the service health events of %s: %w", sub, err)
	}

	for _, e := range events.Value {
		if e.Properties.LastUpdateTime.Before(w.start) || !e.Properties.LastUpdateTime.Before(w.end) {
			continue
		}

		if item := eventOf(e); b.fits(item) {
			out.Events = append(out.Events, item)
		}
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "Azure service health", URL: serviceHealthPortal, At: w.end}}
	empty := fmt.Sprintf("Every resource in subscription %s is available, and no service health event was updated between %s and %s.",
		sub, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, more || b.cut, len(out.Unhealthy) == 0 && len(out.Events) == 0, empty)
}

// readStatuses reads the availability statuses at target, from at most
// maxPages pages.
func (s *Source) readStatuses(ctx context.Context, target string) (statuses []availabilityStatus, more bool, err error) {
	var page struct {
		Value    []availabilityStatus `json:"value"`
		NextLink string               `json:"nextLink"`
	}

	if err := s.client.get(ctx, armScope, target, query(resourceHealthVersion, nil), &page); err != nil {
		return nil, false, fmt.Errorf("read the first page: %w", err)
	}

	for pages := 1; ; pages++ {
		statuses = append(statuses, page.Value...)

		switch {
		case page.NextLink == "":
			return statuses, false, nil
		case pages == maxPages:
			return statuses, true, nil
		}

		link := page.NextLink
		page.Value, page.NextLink = nil, ""

		if err := s.client.next(ctx, s.apis.arm, link, &page); err != nil {
			return nil, false, fmt.Errorf("read page %d: %w", pages+1, err)
		}
	}
}

// statusOf returns the result item of a status. id is the resource's ID,
// or empty to take it from the status's own ID.
func statusOf(st availabilityStatus, id string) statusItem {
	if id == "" {
		id = st.ID
		if i := strings.Index(strings.ToLower(id), statusSuffix); i >= 0 {
			id = id[:i]
		}
	}

	p := st.Properties

	return statusItem{
		Resource: source.CutText(id, maxNameLen),
		State:    p.AvailabilityState,
		Title:    source.CutText(p.Title, maxNameLen),
		Summary:  source.CutText(p.Summary, maxMessageLen),
		Detail:   source.CutText(p.DetailedStatus, maxMessageLen),
		Reason:   strings.TrimSpace(p.ReasonType + " " + p.Category),
		Context:  p.Context,
		Since:    p.OccurredTime,
		Reported: p.ReportedTime,
		ETA:      p.ResolutionETA,
	}
}

// eventOf returns the result item of a service health event.
func eventOf(e healthEvent) eventItem {
	p := e.Properties
	item := eventItem{
		ID:      e.Name,
		Title:   source.CutText(p.Title, maxNameLen),
		Type:    p.EventType,
		Status:  p.Status,
		Level:   p.Level,
		Started: p.ImpactStartTime,
		Updated: p.LastUpdateTime,
		Summary: source.CutText(p.Summary, maxMessageLen),
	}

	for _, impact := range p.Impact {
		item.Services = appendNew(item.Services, impact.ImpactedService)

		for _, r := range impact.ImpactedRegions {
			item.Regions = appendNew(item.Regions, r.ImpactedRegion)
		}
	}

	return item
}

// appendNew appends s to list unless it is empty or already there.
func appendNew(list []string, s string) []string {
	if s == "" {
		return list
	}

	if slices.Contains(list, s) {
		return list
	}

	return append(list, s)
}

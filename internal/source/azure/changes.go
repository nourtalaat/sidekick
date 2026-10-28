package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultChangeRange is the time range of list_changes when the call gives
// no since.
const defaultChangeRange = 24 * time.Hour

// administrative is the Activity Log category of create, update, delete
// and action operations on resources.
const administrative = "Administrative"

// activityPage is one page of the Activity Log.
type activityPage struct {
	Value    []activityEvent `json:"value"`
	NextLink string          `json:"nextLink"`
}

// localized is an Azure value with its display text.
type localized struct {
	Value          string `json:"value"`
	LocalizedValue string `json:"localizedValue"`
}

// text returns the display text, or the value.
func (l localized) text() string {
	if l.LocalizedValue != "" {
		return l.LocalizedValue
	}

	return l.Value
}

// activityEvent is the part of an Activity Log event the source reads.
type activityEvent struct {
	EventTimestamp    time.Time `json:"eventTimestamp"`
	OperationName     localized `json:"operationName"`
	Status            localized `json:"status"`
	SubStatus         localized `json:"subStatus"`
	Category          localized `json:"category"`
	Level             string    `json:"level"`
	Caller            string    `json:"caller"`
	ResourceID        string    `json:"resourceId"`
	ResourceGroupName string    `json:"resourceGroupName"`
	CorrelationID     string    `json:"correlationId"`
}

// changeItem is one change in the result of list_changes.
type changeItem struct {
	Time          time.Time `json:"time"`
	Operation     string    `json:"operation"`
	Status        string    `json:"status"`
	SubStatus     string    `json:"subStatus,omitempty"`
	Level         string    `json:"level,omitempty"`
	Caller        string    `json:"caller,omitempty"`
	Resource      string    `json:"resource,omitempty"`
	CorrelationID string    `json:"correlationId,omitempty"`
}

// changesResult is the result of list_changes.
type changesResult struct {
	Subscription string       `json:"subscription"`
	Changes      []changeItem `json:"changes"`
}

// listChanges runs list_changes. It keeps the Administrative events that
// ended, not the Started and Accepted events of the same operations.
func (s *Source) listChanges(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Subscription  string `json:"subscription"`
		ResourceGroup string `json:"resourceGroup"`
		Resource      string `json:"resource"`
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

	w, err := s.window(in.Since, in.Until, defaultChangeRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter, scope, err := s.activityFilter(sub, in.ResourceGroup, in.Resource, w)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit, defaultEvents, maxEvents)
	target := s.apis.arm + "/subscriptions/" + url.PathEscape(sub) + "/providers/Microsoft.Insights/eventtypes/management/values"

	events, more, err := s.readActivity(ctx, target, query(activityLogVersion, url.Values{"$filter": {filter}}), limit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the Activity Log of %s: %w", sub, err)
	}

	out := changesResult{Subscription: sub, Changes: make([]changeItem, 0, len(events))}
	b := budget{}

	for _, e := range events {
		item := changeItem{
			Time:          e.EventTimestamp,
			Operation:     source.CutText(e.OperationName.text(), maxNameLen),
			Status:        e.Status.text(),
			SubStatus:     source.CutText(e.SubStatus.text(), maxNameLen),
			Level:         e.Level,
			Caller:        source.CutText(e.Caller, maxNameLen),
			Resource:      source.CutText(e.ResourceID, maxNameLen),
			CorrelationID: e.CorrelationID,
		}

		if !b.fits(item) {
			break
		}

		out.Changes = append(out.Changes, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceDeploy,
		Title: "Activity Log of " + scope,
		URL:   portal(scope, "/eventlogs"),
		At:    w.end,
	}}

	empty := fmt.Sprintf("The Activity Log of %s has no changes between %s and %s.", scope, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, more || b.cut, len(out.Changes) == 0, empty)
}

// activityFilter returns the Activity Log filter of a call, and the scope
// it reads: the subscription, a resource group or one resource. The
// values that go in the filter are checked, so none can hold a quote.
func (s *Source) activityFilter(sub, group, resource string, w window) (filter, scope string, err error) {
	filter = fmt.Sprintf("eventTimestamp ge '%s' and eventTimestamp le '%s'", timestamp(w.start), timestamp(w.end))
	scope = "/subscriptions/" + sub
	group = strings.TrimSpace(group)
	resource = strings.TrimSpace(resource)

	switch {
	case group != "" && resource != "":
		return "", "", &source.ToolError{Message: "give resourceGroup or resource, not both"}
	case resource != "":
		id, err := s.resource("resource", resource)
		if err != nil {
			return "", "", fmt.Errorf("check the resource: %w", err)
		}

		if !strings.HasPrefix(strings.ToLower(id), strings.ToLower(scope)+"/") {
			return "", "", &source.ToolError{Message: "resource: must be in subscription " + sub}
		}

		return filter + " and resourceUri eq '" + id + "'", id, nil
	case group != "":
		if err := checkGroup(group); err != nil {
			return "", "", fmt.Errorf("check the resource group: %w", err)
		}

		return filter + " and resourceGroupName eq '" + group + "'", scope + "/resourceGroups/" + group, nil
	default:
		return filter, scope, nil
	}
}

// readActivity reads up to limit ended Administrative events, newest
// first, from at most maxPages pages.
func (s *Source) readActivity(ctx context.Context, target string, q url.Values, limit int) (events []activityEvent, more bool, err error) {
	var page activityPage
	if err := s.client.get(ctx, armScope, target, q, &page); err != nil {
		return nil, false, fmt.Errorf("read the first page: %w", err)
	}

	for pages := 1; ; pages++ {
		for _, e := range page.Value {
			if e.Category.Value != administrative || e.Status.Value == "Started" || e.Status.Value == "Accepted" {
				continue
			}

			if len(events) == limit {
				return events, true, nil
			}

			events = append(events, e)
		}

		if page.NextLink == "" {
			return events, false, nil
		}

		if pages == maxPages {
			return events, true, nil
		}

		link := page.NextLink
		page = activityPage{}

		if err := s.client.next(ctx, s.apis.arm, link, &page); err != nil {
			return nil, false, fmt.Errorf("read page %d: %w", pages+1, err)
		}
	}
}

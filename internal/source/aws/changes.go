package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultChangeRange is the time range of list_changes when the call gives
// no since.
const defaultChangeRange = 24 * time.Hour

// Bounds of the event history list_changes reads.
const (
	// trailPageSize is the most events one LookupEvents page holds.
	trailPageSize = 50
	// maxTrailPages is the most pages one call reads. LookupEvents takes
	// 2 requests a second per account and Region.
	maxTrailPages = 5
	// maxEventResources bounds the resources one change lists.
	maxEventResources = 10
)

// eventSourcePattern is the shape of an AWS service's event source.
var eventSourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,98}\.amazonaws\.com$`)

// trailRecord is the part of a CloudTrail record the source reads, from
// the JSON of an event.
type trailRecord struct {
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
	UserIdentity struct {
		ARN string `json:"arn"`
	} `json:"userIdentity"`
	RequestParameters json.RawMessage `json:"requestParameters"`
}

// changeItem is one change in the result of list_changes.
type changeItem struct {
	Time      time.Time        `json:"time"`
	Event     string           `json:"event"`
	Service   string           `json:"service"`
	User      string           `json:"user,omitempty"`
	Principal string           `json:"principal,omitempty"`
	Resources []changeResource `json:"resources,omitempty"`
	// Error is the error code and message of a call that failed.
	Error string `json:"error,omitempty"`
	// Request is the call's request parameters, as JSON text, cut.
	Request string `json:"request,omitempty"`
	URL     string `json:"url"`
}

// changeResource is one resource a change names.
type changeResource struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name"`
}

// changesResult is the result of list_changes.
type changesResult struct {
	Account string       `json:"account"`
	Region  string       `json:"region"`
	Changes []changeItem `json:"changes"`
}

// changeFilter is what list_changes asks CloudTrail for, and what it keeps
// of the answer. LookupEvents takes one lookup attribute, so a call for a
// resource or an event source asks for that and leaves out the read-only
// events itself.
type changeFilter struct {
	attribute   types.LookupAttribute
	eventSource string
	// console is the query of the event history page in the console.
	console url.Values
	// about says what the filter narrows to, for the result text.
	about string
}

// listChanges runs list_changes.
func (s *Source) listChanges(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account     string `json:"account"`
		Region      string `json:"region"`
		Resource    string `json:"resource"`
		EventSource string `json:"eventSource"`
		Since       string `json:"since"`
		Until       string `json:"until"`
		Limit       int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	t, err := s.target(in.Account, in.Region)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultChangeRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter, err := newChangeFilter(in.Resource, in.EventSource)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	events, more, err := readEvents(ctx, s.cloudTrail(t), filter, w, limitOf(in.Limit, defaultEvents, maxEvents))
	if err != nil {
		return source.Result{}, fmt.Errorf("look up the events of account %s, Region %s: %w", t.account.id, t.region, err)
	}

	out := changesResult{Account: t.account.id, Region: t.region, Changes: make([]changeItem, 0, len(events))}
	b := budget{}

	for _, e := range events {
		item := changeOf(e, t.region)
		if !b.fits(item) {
			break
		}

		out.Changes = append(out.Changes, item)
	}

	where := fmt.Sprintf("account %s, Region %s", t.account.id, t.region)

	evidence := []source.Evidence{{
		Kind:  source.EvidenceDeploy,
		Title: "CloudTrail event history of " + where,
		URL:   consoleURL(t.region, "cloudtrailv2", "/events?"+filter.console.Encode()),
		At:    w.end,
	}}

	empty := fmt.Sprintf("CloudTrail has no write events%s in %s between %s and %s.", filter.about, where, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, more || b.cut, len(out.Changes) == 0, empty)
}

// keeps reports whether an event CloudTrail returned for the filter is a
// change the call asked for.
func (f changeFilter) keeps(e types.Event) bool {
	if awssdk.ToString(e.ReadOnly) == "true" {
		return false
	}

	return f.eventSource == "" || awssdk.ToString(e.EventSource) == f.eventSource
}

// newChangeFilter returns the filter of a call for one resource, one event
// source, both or neither. The resource is asked for when there is one,
// since it narrows the most.
func newChangeFilter(resource, eventSource string) (changeFilter, error) {
	resource = strings.TrimSpace(resource)
	eventSource = strings.TrimSpace(eventSource)

	if len(resource) > maxNameLen || strings.ContainsFunc(resource, isControl) {
		return changeFilter{}, &source.ToolError{Message: fmt.Sprintf("resource: must be a name or ARN of at most %d bytes, on one line", maxNameLen)}
	}

	if eventSource != "" && !eventSourcePattern.MatchString(eventSource) {
		return changeFilter{}, &source.ToolError{Message: fmt.Sprintf("eventSource: %q is not an AWS event source such as ecs.amazonaws.com",
			source.CutText(eventSource, maxNameLen))}
	}

	f := changeFilter{eventSource: eventSource}

	switch {
	case resource != "":
		f.attribute = lookup(types.LookupAttributeKeyResourceName, resource)
		f.about = " about " + resource
	case eventSource != "":
		f.attribute = lookup(types.LookupAttributeKeyEventSource, eventSource)
	default:
		f.attribute = lookup(types.LookupAttributeKeyReadOnly, "false")
	}

	f.console = url.Values{string(f.attribute.AttributeKey): {awssdk.ToString(f.attribute.AttributeValue)}}

	if eventSource != "" {
		f.about += " from " + eventSource
	}

	return f, nil
}

// readEvents reads up to limit events the filter keeps, newest first, from
// at most maxTrailPages pages, and reports whether there may be more.
func readEvents(ctx context.Context, client *cloudtrail.Client, f changeFilter, w window, limit int) ([]types.Event, bool, error) {
	in := &cloudtrail.LookupEventsInput{
		LookupAttributes: []types.LookupAttribute{f.attribute},
		StartTime:        awssdk.Time(w.start),
		EndTime:          awssdk.Time(w.end),
		MaxResults:       awssdk.Int32(trailPageSize),
	}

	var events []types.Event

	for page := 1; ; page++ {
		out, err := client.LookupEvents(ctx, in)
		if err != nil {
			return nil, false, fmt.Errorf("read page %d: %w", page, err)
		}

		for _, e := range out.Events {
			if !f.keeps(e) {
				continue
			}

			if len(events) == limit {
				return events, true, nil
			}

			events = append(events, e)
		}

		switch {
		case out.NextToken == nil:
			return events, false, nil
		case page == maxTrailPages:
			return events, true, nil
		}

		in.NextToken = out.NextToken
	}
}

// changeOf returns the change of a CloudTrail event, with the details of
// its record cut, and its console link.
func changeOf(e types.Event, region string) changeItem {
	item := changeItem{
		Time:    awssdk.ToTime(e.EventTime).UTC(),
		Event:   source.CutText(awssdk.ToString(e.EventName), maxNameLen),
		Service: source.CutText(awssdk.ToString(e.EventSource), maxNameLen),
		User:    source.CutText(awssdk.ToString(e.Username), maxNameLen),
		URL:     consoleURL(region, "cloudtrailv2", "/events/"+url.PathEscape(awssdk.ToString(e.EventId))),
	}

	for _, r := range e.Resources[:min(len(e.Resources), maxEventResources)] {
		item.Resources = append(item.Resources, changeResource{
			Type: source.CutText(awssdk.ToString(r.ResourceType), maxNameLen),
			Name: source.CutText(awssdk.ToString(r.ResourceName), maxNameLen),
		})
	}

	// A record that does not decode keeps only what the event itself says.
	var record trailRecord
	if json.Unmarshal([]byte(awssdk.ToString(e.CloudTrailEvent)), &record) != nil {
		return item
	}

	item.Principal = source.CutText(record.UserIdentity.ARN, maxNameLen)

	if record.ErrorCode != "" {
		item.Error = source.CutText(strings.TrimSuffix(record.ErrorCode+": "+record.ErrorMessage, ": "), maxMessageLen)
	}

	if params := string(record.RequestParameters); params != "" && params != "null" {
		item.Request = source.CutText(params, maxCellLen)
	}

	return item
}

// lookup returns a lookup attribute.
func lookup(key types.LookupAttributeKey, value string) types.LookupAttribute {
	return types.LookupAttribute{AttributeKey: key, AttributeValue: awssdk.String(value)}
}

// isControl reports whether r is a control character, such as a line
// break.
func isControl(r rune) bool {
	return r < ' ' || r == 0x7f
}

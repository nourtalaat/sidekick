package aws

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/health"
	"github.com/aws/aws-sdk-go-v2/service/health/types"
	"github.com/aws/smithy-go"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultHealthRange is the time range of list_health_events when the call
// gives no since.
const defaultHealthRange = 24 * time.Hour

// maxEventDetails is the most events DescribeEventDetails describes in one
// call.
const maxEventDetails = 10

// globalRegion is the Region AWS Health gives events of global services,
// such as IAM.
const globalRegion = "global"

// healthConsole is the AWS Health dashboard, with the event log the event
// links open.
const healthConsole = "https://health.console.aws.amazon.com/health/home#/account/event-log"

// subscriptionRequired is the error AWS Health answers on an account
// without a support plan that includes its API.
const subscriptionRequired = "SubscriptionRequiredException"

// healthEvent is one event in the result of list_health_events.
type healthEvent struct {
	Service     string    `json:"service,omitempty"`
	Type        string    `json:"type,omitempty"`
	Category    string    `json:"category,omitempty"`
	Region      string    `json:"region,omitempty"`
	Status      string    `json:"status"`
	Started     time.Time `json:"started,omitzero"`
	Ended       time.Time `json:"ended,omitzero"`
	Updated     time.Time `json:"updated,omitzero"`
	Description string    `json:"description,omitempty"`
	URL         string    `json:"url"`
}

// healthResult is the result of list_health_events.
type healthResult struct {
	Account string        `json:"account"`
	Region  string        `json:"region"`
	Events  []healthEvent `json:"events"`
}

// listHealthEvents runs list_health_events.
func (s *Source) listHealthEvents(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account string `json:"account"`
		Region  string `json:"region"`
		Status  string `json:"status"`
		Since   string `json:"since"`
		Until   string `json:"until"`
		Limit   int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	t, err := s.target(in.Account, in.Region)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultHealthRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	status := cmp.Or(in.Status, stateAny)
	if !slices.Contains(healthStates, any(status)) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("status: %q is not open, upcoming, closed or any", source.CutText(status, maxNameLen))}
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	filter := &types.EventFilter{
		Regions:          []string{t.region, globalRegion},
		LastUpdatedTimes: []types.DateTimeRange{{From: awssdk.Time(w.start), To: awssdk.Time(w.end)}},
	}

	if status != stateAny {
		filter.EventStatusCodes = []types.EventStatusCode{types.EventStatusCode(status)}
	}

	limit := limitOf(in.Limit, defaultHealthEvents, maxHealthEvents)
	client := s.health(t)

	answer, err := client.DescribeEvents(ctx, &health.DescribeEventsInput{Filter: filter, MaxResults: awssdk.Int32(int32(limit))}) //nolint:gosec // limit is at most maxHealthEvents.
	if err != nil {
		return source.Result{}, healthError(t, err)
	}

	descriptions, err := eventDescriptions(ctx, client, answer.Events)
	if err != nil {
		return source.Result{}, healthError(t, err)
	}

	out := healthResult{Account: t.account.id, Region: t.region, Events: make([]healthEvent, 0, len(answer.Events))}
	evidence := make([]source.Evidence, 0, len(answer.Events))
	b := budget{}

	for _, e := range answer.Events {
		arn := awssdk.ToString(e.Arn)
		item := healthEvent{
			Service:     awssdk.ToString(e.Service),
			Type:        awssdk.ToString(e.EventTypeCode),
			Category:    string(e.EventTypeCategory),
			Region:      awssdk.ToString(e.Region),
			Status:      string(e.StatusCode),
			Started:     awssdk.ToTime(e.StartTime).UTC(),
			Ended:       awssdk.ToTime(e.EndTime).UTC(),
			Updated:     awssdk.ToTime(e.LastUpdatedTime).UTC(),
			Description: descriptions[arn],
			URL:         healthConsole + "?eventID=" + url.QueryEscape(arn) + "&eventTab=details",
		}

		if !b.fits(item) {
			break
		}

		out.Events = append(out.Events, item)
		evidence = append(evidence, source.Evidence{Kind: source.EvidenceIncident, Title: "AWS Health event " + item.Type, URL: item.URL, At: item.Updated})
	}

	where := fmt.Sprintf("account %s, Region %s", t.account.id, t.region)
	empty := fmt.Sprintf("AWS Health has no events for %s or global services updated between %s and %s.", where, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, answer.NextToken != nil || b.cut, len(out.Events) == 0, empty)
}

// eventDescriptions returns the latest description of the first
// maxEventDetails events, cut, by event ARN.
func eventDescriptions(ctx context.Context, client *health.Client, events []types.Event) (map[string]string, error) {
	arns := make([]string, 0, maxEventDetails)
	for _, e := range events[:min(len(events), maxEventDetails)] {
		arns = append(arns, awssdk.ToString(e.Arn))
	}

	if len(arns) == 0 {
		return nil, nil
	}

	answer, err := client.DescribeEventDetails(ctx, &health.DescribeEventDetailsInput{EventArns: arns})
	if err != nil {
		return nil, fmt.Errorf("describe the events: %w", err)
	}

	out := make(map[string]string, len(answer.SuccessfulSet))

	for _, d := range answer.SuccessfulSet {
		if d.Event != nil && d.EventDescription != nil {
			out[awssdk.ToString(d.Event.Arn)] = source.CutText(awssdk.ToString(d.EventDescription.LatestDescription), maxMessageLen)
		}
	}

	return out, nil
}

// healthError returns the error of a failed Health call. Without a support
// plan that includes the AWS Health API, it is a tool error that says so.
func healthError(t target, err error) error {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok && apiErr.ErrorCode() == subscriptionRequired {
		return &source.ToolError{Message: fmt.Sprintf("the AWS Health API needs a Business, Enterprise On-Ramp or Enterprise support plan, "+
			"which account %s does not have; the AWS Health dashboard in the console works without one", t.account.id)}
	}

	return fmt.Errorf("read the AWS Health events of account %s: %w", t.account.id, err)
}

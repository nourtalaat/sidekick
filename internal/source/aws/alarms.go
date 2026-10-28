package aws

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultHistoryRange is the time range of an alarm's history when the call
// gives no since.
const defaultHistoryRange = 24 * time.Hour

// maxAlarmPages is the most DescribeAlarms pages one call reads.
const maxAlarmPages = 2

// alarmTypes are the alarm types list_alarms reads.
var alarmTypes = []types.AlarmType{types.AlarmTypeMetricAlarm, types.AlarmTypeCompositeAlarm}

// alarmItem is one alarm in the result of list_alarms.
type alarmItem struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"`
	State       string       `json:"state"`
	Reason      string       `json:"reason,omitempty"`
	Since       time.Time    `json:"since,omitzero"`
	Description string       `json:"description,omitempty"`
	Metric      *alarmMetric `json:"metric,omitempty"`
	// Expression is the metric math of an alarm on several metrics.
	Expression string `json:"expression,omitempty"`
	// Rule is the rule of a composite alarm.
	Rule       string   `json:"rule,omitempty"`
	Comparison string   `json:"comparison,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
	URL        string   `json:"url"`
}

// alarmMetric is the metric an alarm watches.
type alarmMetric struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	Stat       string            `json:"stat,omitempty"`
	Period     int32             `json:"period,omitempty"`
}

// historyItem is one state change of an alarm.
type historyItem struct {
	Time    time.Time `json:"time"`
	Summary string    `json:"summary"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
}

// alarmsResult is the result of list_alarms without alarm.
type alarmsResult struct {
	Account string      `json:"account"`
	Region  string      `json:"region"`
	Alarms  []alarmItem `json:"alarms"`
}

// alarmResult is the result of list_alarms for one alarm.
type alarmResult struct {
	Account string        `json:"account"`
	Region  string        `json:"region"`
	Alarm   alarmItem     `json:"alarm"`
	History []historyItem `json:"history"`
}

// listAlarms runs list_alarms: the alarms in a state, or one alarm and its
// state changes.
func (s *Source) listAlarms(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account string `json:"account"`
		Region  string `json:"region"`
		Alarm   string `json:"alarm"`
		Prefix  string `json:"prefix"`
		State   string `json:"state"`
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

	w, err := s.window(in.Since, in.Until, defaultHistoryRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	alarm, prefix := strings.TrimSpace(in.Alarm), strings.TrimSpace(in.Prefix)

	for _, f := range []struct{ key, value string }{{"alarm", alarm}, {"prefix", prefix}} {
		if len(f.value) > maxMetricNameLen || strings.ContainsFunc(f.value, isControl) {
			return source.Result{}, &source.ToolError{Message: fmt.Sprintf("%s: must be at most %d bytes, on one line", f.key, maxMetricNameLen)}
		}
	}

	if alarm != "" && prefix != "" {
		return source.Result{}, &source.ToolError{Message: "give alarm or prefix, not both"}
	}

	if !slices.Contains(alarmStates, any(cmp.Or(in.State, stateAlarm))) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("state: %q is not ALARM, OK, INSUFFICIENT_DATA or any", source.CutText(in.State, maxNameLen))}
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	limit := limitOf(in.Limit, defaultAlarms, maxAlarms)

	if alarm != "" {
		return s.alarmHistory(ctx, t, alarm, w, limit)
	}

	return s.alarmsInState(ctx, t, cmp.Or(in.State, stateAlarm), prefix, limit)
}

// alarmsInState returns the alarms in state, newest change first.
func (s *Source) alarmsInState(ctx context.Context, t target, state, prefix string, limit int) (source.Result, error) {
	in := &cloudwatch.DescribeAlarmsInput{AlarmTypes: alarmTypes, MaxRecords: awssdk.Int32(maxAlarms)}
	if state != stateAny {
		in.StateValue = types.StateValue(state)
	}

	if prefix != "" {
		in.AlarmNamePrefix = awssdk.String(prefix)
	}

	client := s.cloudWatch(t)
	out := alarmsResult{Account: t.account.id, Region: t.region, Alarms: []alarmItem{}}
	more := false

	for page := 1; ; page++ {
		answer, err := client.DescribeAlarms(ctx, in)
		if err != nil {
			return source.Result{}, fmt.Errorf("list the alarms of account %s, Region %s: %w", t.account.id, t.region, err)
		}

		out.Alarms = append(out.Alarms, alarmItems(answer, t.region)...)

		if answer.NextToken == nil {
			break
		}

		if page == maxAlarmPages {
			more = true

			break
		}

		in.NextToken = answer.NextToken
	}

	slices.SortStableFunc(out.Alarms, func(a, b alarmItem) int { return b.Since.Compare(a.Since) })

	if len(out.Alarms) > limit {
		out.Alarms, more = out.Alarms[:limit], true
	}

	kept := make([]alarmItem, 0, len(out.Alarms))
	evidence := make([]source.Evidence, 0, len(out.Alarms))
	b := budget{}

	for _, a := range out.Alarms {
		if !b.fits(a) {
			break
		}

		kept = append(kept, a)
		evidence = append(evidence, source.Evidence{Kind: source.EvidenceResource, Title: "CloudWatch alarm " + a.Name, URL: a.URL, At: a.Since})
	}

	out.Alarms = kept

	where := fmt.Sprintf("account %s, Region %s", t.account.id, t.region)
	empty := fmt.Sprintf("No CloudWatch alarm is in state %s in %s.", state, where)

	if len(kept) == 0 {
		evidence = []source.Evidence{{Kind: source.EvidenceResource, Title: "CloudWatch alarms in " + where, URL: consoleURL(t.region, "cloudwatch", "alarmsV2:")}}
	}

	return structured(out, evidence, more || b.cut, len(kept) == 0, empty)
}

// alarmHistory returns one alarm and its state changes in w, newest first.
func (s *Source) alarmHistory(ctx context.Context, t target, name string, w window, limit int) (source.Result, error) {
	client := s.cloudWatch(t)

	answer, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name}, AlarmTypes: alarmTypes})
	if err != nil {
		return source.Result{}, fmt.Errorf("read alarm %s: %w", name, err)
	}

	items := alarmItems(answer, t.region)
	if len(items) == 0 {
		return source.Result{NoData: true, Text: fmt.Sprintf("No CloudWatch alarm is named %q in account %s, Region %s.", name, t.account.id, t.region)}, nil
	}

	history, err := client.DescribeAlarmHistory(ctx, &cloudwatch.DescribeAlarmHistoryInput{
		AlarmName:       awssdk.String(name),
		AlarmTypes:      alarmTypes,
		HistoryItemType: types.HistoryItemTypeStateUpdate,
		StartDate:       awssdk.Time(w.start),
		EndDate:         awssdk.Time(w.end),
		ScanBy:          types.ScanByTimestampDescending,
		MaxRecords:      awssdk.Int32(int32(limit)), //nolint:gosec // limit is at most maxAlarms.
	})
	if err != nil {
		return source.Result{}, fmt.Errorf("read the history of alarm %s: %w", name, err)
	}

	out := alarmResult{Account: t.account.id, Region: t.region, Alarm: items[0], History: make([]historyItem, 0, len(history.AlarmHistoryItems))}
	b := budget{}

	for _, h := range history.AlarmHistoryItems {
		item := historyOf(h)
		if !b.fits(item) {
			break
		}

		out.History = append(out.History, item)
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "CloudWatch alarm " + out.Alarm.Name, URL: out.Alarm.URL, At: out.Alarm.Since}}

	return structured(out, evidence, history.NextToken != nil || b.cut, false, "")
}

// alarmItems returns the metric and composite alarms of an answer.
func alarmItems(answer *cloudwatch.DescribeAlarmsOutput, region string) []alarmItem {
	items := make([]alarmItem, 0, len(answer.MetricAlarms)+len(answer.CompositeAlarms))

	for _, a := range answer.MetricAlarms {
		item := alarmItem{
			Name:        source.CutText(awssdk.ToString(a.AlarmName), maxNameLen),
			Type:        "metric",
			State:       string(a.StateValue),
			Reason:      source.CutText(awssdk.ToString(a.StateReason), maxMessageLen),
			Since:       awssdk.ToTime(a.StateUpdatedTimestamp).UTC(),
			Description: source.CutText(awssdk.ToString(a.AlarmDescription), maxMessageLen),
			Comparison:  string(a.ComparisonOperator),
			Threshold:   a.Threshold,
			URL:         alarmURL(region, awssdk.ToString(a.AlarmName)),
		}

		if a.MetricName != nil {
			item.Metric = &alarmMetric{
				Namespace:  awssdk.ToString(a.Namespace),
				Name:       awssdk.ToString(a.MetricName),
				Dimensions: dimensionMap(a.Dimensions),
				Stat:       cmp.Or(string(a.Statistic), awssdk.ToString(a.ExtendedStatistic)),
				Period:     awssdk.ToInt32(a.Period),
			}
		}

		for _, m := range a.Metrics {
			if m.Expression != nil && awssdk.ToBool(m.ReturnData) {
				item.Expression = source.CutText(*m.Expression, maxMessageLen)
			}
		}

		items = append(items, item)
	}

	for _, a := range answer.CompositeAlarms {
		items = append(items, alarmItem{
			Name:        source.CutText(awssdk.ToString(a.AlarmName), maxNameLen),
			Type:        "composite",
			State:       string(a.StateValue),
			Reason:      source.CutText(awssdk.ToString(a.StateReason), maxMessageLen),
			Since:       awssdk.ToTime(a.StateUpdatedTimestamp).UTC(),
			Description: source.CutText(awssdk.ToString(a.AlarmDescription), maxMessageLen),
			Rule:        source.CutText(awssdk.ToString(a.AlarmRule), maxMessageLen),
			URL:         alarmURL(region, awssdk.ToString(a.AlarmName)),
		})
	}

	return items
}

// historyOf returns the state change of a history item, with the old and
// new states from its data when they decode.
func historyOf(h types.AlarmHistoryItem) historyItem {
	item := historyItem{Time: awssdk.ToTime(h.Timestamp).UTC(), Summary: source.CutText(awssdk.ToString(h.HistorySummary), maxMessageLen)}

	var data struct {
		OldState struct {
			StateValue string `json:"stateValue"`
		} `json:"oldState"`
		NewState struct {
			StateValue string `json:"stateValue"`
		} `json:"newState"`
	}

	if json.Unmarshal([]byte(awssdk.ToString(h.HistoryData)), &data) == nil {
		item.From = source.CutText(data.OldState.StateValue, maxNameLen)
		item.To = source.CutText(data.NewState.StateValue, maxNameLen)
	}

	return item
}

// dimensionMap returns dimensions by name, or nil when there are none.
func dimensionMap(dimensions []types.Dimension) map[string]string {
	if len(dimensions) == 0 {
		return nil
	}

	out := make(map[string]string, len(dimensions))
	for _, d := range dimensions {
		out[awssdk.ToString(d.Name)] = source.CutText(awssdk.ToString(d.Value), maxNameLen)
	}

	return out
}

// alarmURL returns the CloudWatch console link of an alarm.
func alarmURL(region, name string) string {
	return consoleURL(region, "cloudwatch", "alarmsV2:alarm/"+url.PathEscape(name))
}

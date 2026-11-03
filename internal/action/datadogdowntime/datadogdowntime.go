// Package datadogdowntime is the action datadog.downtime: it mutes the
// notifications of a Datadog monitor for a limited time, by making a
// downtime through the Datadog API after an approver approved it, and it can
// cancel the downtime early. A downtime may only reach the monitors the
// operator's limits allow, only in the scopes the limits list, and may not
// last longer than limits.maxDuration. The rules and the text that every
// mute action shares are in internal/action/mute. A downtime does not change
// the monitor, so the action needs the Monitors Downtime and Monitors Read
// permissions and not Monitors Write.
//
// What the action relies on from Datadog, as its public API specification
// shows, in the repository https://github.com/DataDog/datadog-api-client-go
// at the release tag v2.67.0 (paths below are in that repository):
//
//   - .generator/schemas/v2/openapi.yaml, POST /api/v2/downtime
//     (CreateDowntime): the body is {"data": {"type": "downtime",
//     "attributes": {...}}}. The attributes scope and monitor_identifier are
//     required. monitor_identifier is {"monitor_id": <int>} or
//     {"monitor_tags": [<tag>, ...]}, and a list of tags matches the
//     monitors that have all of them; ["*"] mutes every monitor. The
//     schedule is optional; a one-time schedule is {"start", "end"} and
//     takes no other key, both ISO-8601 with a UTC offset of zero. A missing
//     start means the downtime starts when it is made, and a missing end
//     means it never ends. The answer is {"data": {"id": "<uuid>",
//     "attributes": {...}}}. The scope is a string in Datadog's search
//     syntax. The message may hold @ handles, which Datadog notifies.
//   - The same file, DELETE /api/v2/downtime/{downtime_id}
//     (CancelDowntime): answers 204 and cancels the downtime. A canceled
//     downtime is kept for about two days and may still be listed. It answers
//     404 for an unknown ID.
//   - The same file, GET /api/v2/downtime (ListDowntimes): takes
//     current_only, page[offset] and page[limit] (default 30) and answers
//     {"data": [...], "meta": {"page": {"total_filtered_count": <n>}}}. It
//     has no filter by message or monitor, so the action lists every page and
//     looks at the messages. The status of a downtime is active, canceled,
//     ended or scheduled. Every downtime call needs the monitors_downtime
//     permission, and the description of that scope says that the ability to
//     write monitors is not required to set downtimes.
//   - .generator/schemas/v1/openapi.yaml: GET /api/v1/monitor/{monitor_id}
//     needs monitors_read and answers the monitor's name, type, tags and
//     overall_state; GET /api/v1/monitor takes monitor_tags, page and
//     page_size (at most 1000) and needs monitors_read.
//   - tests/scenarios/cassettes/TestScenarios/v2/Feature_Downtimes/
//     Scenario_Schedule_a_downtime_returns_OK_response.yaml: the answer to a
//     create shows the schedule as {"end": null, "start": "<time>"}, the
//     status as active, the downtime with an id, and canceled as null.
//
// What the specification, the recorded answers and the Datadog documentation
// do not say, so the action does not depend on it for safety:
//
//   - What happens to a downtime whose monitor is deleted. Undo and the
//     search for the marker never read the monitor.
//   - Whether the unfiltered list keeps showing canceled and ended downtimes,
//     and for how long. Execute looks for the marker in whatever the list
//     shows, so a downtime that a person canceled is not made again only
//     while Datadog still lists it.
//   - Whether Datadog keeps a line break in the message. No recorded answer
//     sends a message with a line break. The marker is written on a line of its
//     own, and it is found again when the message, without trailing white
//     space, ends with the marker after white space of any kind.
//   - Whether the list shows a downtime right after it was made. A retry
//     within a moment of a lost answer might not see it.
package datadogdowntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/datadogapi"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: datadog.downtime".
const Type = "datadog.downtime"

var (
	errNoSecrets    = errors.New("no secrets resolver to read the keys with")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errBadSnapshot  = errors.New("the snapshot of the proposal cannot be read")
	errTargetDiffer = errors.New("the snapshot is not of these parameters")
	errNoUndoOf     = errors.New("the undo does not say which proposal it undoes")
	errNotAllowed   = errors.New("the monitor is not one of limits.monitorIds, and it has none of limits.monitorTags")
	errOneMonitor   = errors.New("give exactly one of monitorId and monitorTag")
)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// params are what the model fills in when it proposes the action.
type params struct {
	MonitorID  int64  `json:"monitorId"`
	MonitorTag string `json:"monitorTag"`
	Scope      string `json:"scope"`
	Duration   string `json:"duration"`
	Message    string `json:"message"`
}

// request is params after they were checked against the limits.
type request struct {
	monitorID  int64
	monitorTag string
	scope      string
	duration   time.Duration
	message    string
}

// downtimer is one configured datadog.downtime action.
type downtimer struct {
	name        string
	limits      Limits
	maxDuration time.Duration
	appHost     string
	client      *client
	now         func() time.Time
}

var (
	_ action.Action      = (*downtimer)(nil)
	_ action.Snapshotter = (*downtimer)(nil)
	_ action.Undoer      = (*downtimer)(nil)
)

// New builds the action from its validated config. It does not call
// Datadog: a service that is down must not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	limit, err := mute.MaxDuration(cfg.Limits.MaxDuration)
	if err != nil {
		return nil, fmt.Errorf("limits.maxDuration: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	d := &downtimer{
		name:        name,
		limits:      cfg.Limits,
		maxDuration: limit,
		client:      &client{datadogapi.NewClient(hc, cfg.apiURL(), cfg.APIKeySecret, cfg.AppKeySecret, deps.Secrets)},
		now:         time.Now,
	}

	if cfg.URL == "" {
		d.appHost = datadogapi.AppHost(cfg.Site)
	}

	return d, nil
}

// Spec implements action.Action. The description tells the model which
// monitors and scopes it may mute and for how long.
func (d *downtimer) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Mutes the notifications of a Datadog monitor for a limited time, from " + mute.FormatDuration(mute.MinDuration) +
		" to " + mute.FormatDuration(d.maxDuration) + ", by making a downtime. The downtime starts when it is approved and ends by itself. " +
		"It does not change the monitor. ")
	b.WriteString("Give either monitorId or monitorTag, not both, the scope, the duration as a Go duration such as 30m or 2h, and the reason as the message. ")

	if len(d.limits.MonitorIDs) > 0 {
		b.WriteString("Allowed monitor IDs: " + joinInts(d.limits.MonitorIDs) + ". ")
	}

	if len(d.limits.MonitorTags) > 0 {
		b.WriteString("Monitors with one of the tags: " + strings.Join(d.limits.MonitorTags, ", ") +
			" are allowed too, and monitorTag mutes every monitor that has the tag, as one of these exactly. ")
	}

	b.WriteString("The scope must be one of: " + strings.Join(d.limits.Scopes, " | ") + ". ")
	b.WriteString("Write the message without @ handles and without the text " + markerText + ". ")
	b.WriteString("The proposal shows the monitor, or the monitors with the tag. An approver can cancel the downtime early.")

	return action.Spec{Name: d.name, Description: b.String(), ParamsSchema: d.paramsSchema()}
}

// Validate implements action.Action. It runs without calling Datadog, so it
// checks what the parameters alone show. A monitor that is allowed by its
// tags is checked when the monitor is read. A limit violation wraps
// action.ErrLimit.
func (d *downtimer) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := d.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// parse decodes the parameters and checks them against the limits.
func (d *downtimer) parse(raw json.RawMessage) (request, error) {
	var p params
	if err := action.DecodeParams(raw, &p); err != nil {
		return request{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if (p.MonitorID != 0) == (p.MonitorTag != "") {
		return request{}, fmt.Errorf("%w: %w", action.ErrParams, errOneMonitor)
	}

	if err := d.checkMonitor(p); err != nil {
		return request{}, fmt.Errorf("check the monitor: %w", err)
	}

	if !slices.Contains(d.limits.Scopes, p.Scope) {
		return request{}, fmt.Errorf("scope %q is not one of limits.scopes: %w", p.Scope, action.ErrLimit)
	}

	duration, err := mute.ParseDuration(p.Duration, d.maxDuration)
	if err != nil {
		return request{}, fmt.Errorf("check the duration: %w", err)
	}

	message, err := cleanMessage(p.Message)
	if err != nil {
		return request{}, fmt.Errorf("check the message: %w", err)
	}

	return request{monitorID: p.MonitorID, monitorTag: p.MonitorTag, scope: p.Scope, duration: duration, message: message}, nil
}

// checkMonitor checks what the parameters show of the monitor. A monitor ID
// that is not listed passes when the config has tags, because the monitor's
// own tags decide, once it is read.
func (d *downtimer) checkMonitor(p params) error {
	switch {
	case p.MonitorTag != "":
		if !slices.Contains(d.limits.MonitorTags, p.MonitorTag) {
			return fmt.Errorf("monitorTag %q is not one of limits.monitorTags: %w", p.MonitorTag, action.ErrLimit)
		}
	case p.MonitorID < 1:
		return fmt.Errorf("monitorId %d is not a monitor ID: %w", p.MonitorID, action.ErrParams)
	case len(d.limits.MonitorTags) == 0 && !slices.Contains(d.limits.MonitorIDs, p.MonitorID):
		return fmt.Errorf("monitor %d: %w: %w", p.MonitorID, action.ErrLimit, errNotAllowed)
	}

	return nil
}

// allows reports whether the limits allow a monitor with this ID and these
// tags.
func (d *downtimer) allows(id int64, tags []string) bool {
	return slices.Contains(d.limits.MonitorIDs, id) ||
		slices.ContainsFunc(tags, func(t string) bool { return slices.Contains(d.limits.MonitorTags, t) })
}

// paramsSchema is the JSON Schema of params. Sidekick checks the parameters
// against it before it calls Validate. It does not say that exactly one of
// monitorId and monitorTag is needed, because some model providers refuse a
// schema with oneOf at its top; Validate checks that.
func (d *downtimer) paramsSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["scope", "duration", "message"],
  "properties": {
    "monitorId": {"type": "integer", "minimum": 1, "description": "The ID of the monitor to mute. Give this or monitorTag, not both."},
    "monitorTag": {"type": "string", "minLength": 1, "maxLength": %d, "description": "Mutes every monitor that has this tag, which must be one of the allowed tags exactly. Give this or monitorId, not both."},
    "scope": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The downtime scope, one of the allowed scopes exactly."},
    "duration": {"type": "string", "maxLength": 32, "description": "How long the downtime lasts, as a Go duration such as 30m or 2h, from %s to %s."},
    "message": {"type": "string", "minLength": 1, "maxLength": %d, "description": "Why the monitor is muted. No @ handles."}
  }
}`, maxScopeBytes, maxScopeBytes, mute.FormatDuration(mute.MinDuration), mute.FormatDuration(d.maxDuration), mute.MaxCommentRunes))
}

// joinInts writes IDs separated by commas.
func joinInts(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}

	return strings.Join(parts, ", ")
}

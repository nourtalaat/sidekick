package datadogdowntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What the preview of a snapshot shows. They keep the whole snapshot short
// enough for an approver to read in Slack.
const (
	// previewShown is how many monitors the preview of a tag names.
	previewShown = 5
	// previewNameBytes is the longest monitor name in the preview.
	previewNameBytes = 80
	// previewMessageBytes is the longest error message in the preview.
	previewMessageBytes = 120
	// targetBytes is the longest target text.
	targetBytes = 100
)

// afterDoc is the After of a snapshot: the downtime as the approver reads it.
// Execute makes the downtime from MonitorID or MonitorTag, Scope, Duration
// and Message and nothing else. It starts the downtime when it runs, adds
// the marker of the proposal to the message, and never sends Preview, which
// is only for the approver.
type afterDoc struct {
	MonitorID  int64  `json:"monitorId,omitempty"`
	MonitorTag string `json:"monitorTag,omitempty"`
	Scope      string `json:"scope"`
	Duration   string `json:"duration"`
	Message    string `json:"message"`
	Preview    any    `json:"preview"`
}

// monitorPreview is the monitor of a downtime by ID, as the approver reads it.
type monitorPreview struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	State string `json:"state"`
}

// tagPreview is what a downtime by tag reaches at the time of the proposal.
// AtLeast is true when there are more monitors than the page that was read.
type tagPreview struct {
	Count   int      `json:"count"`
	AtLeast bool     `json:"atLeast,omitempty"`
	First   []string `json:"first"`
}

// previewUnknown stands for the preview when the monitors could not be read.
type previewUnknown struct {
	Unavailable string `json:"unavailable"`
}

// Snapshot implements action.Snapshotter. Nothing exists before the
// downtime, so Before is null. For a monitor ID it reads the monitor and
// refuses one the limits do not allow, and After shows its name, type and
// state. For a tag After shows how many monitors have the tag and the first
// names, as information for the approver; a preview that cannot be read does
// not refuse the proposal.
func (d *downtimer) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	req, err := d.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	doc := afterDoc{
		MonitorID: req.monitorID, MonitorTag: req.monitorTag, Scope: req.scope,
		Duration: mute.FormatDuration(req.duration), Message: req.message,
	}

	if req.monitorTag != "" {
		doc.Preview = d.previewTag(ctx, req.monitorTag)
	} else {
		m, err := d.readMonitor(ctx, req.monitorID)
		if err != nil {
			return action.Snapshot{}, fmt.Errorf("check the monitor against the limits: %w", err)
		}

		doc.Preview = monitorPreview{Name: source.CutText(m.Name, previewNameBytes), Type: m.Type, State: m.State}
	}

	after, err := json.Marshal(doc)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("encode the downtime: %w", err)
	}

	return action.Snapshot{Target: target(req), After: after}, nil
}

// readMonitor reads the monitor with id and checks that the limits allow it.
func (d *downtimer) readMonitor(ctx context.Context, id int64) (monitor, error) {
	m, err := d.client.monitor(ctx, id)
	if err != nil {
		return monitor{}, fmt.Errorf("read the monitor: %w", err)
	}

	if !d.allows(id, m.Tags) {
		return monitor{}, fmt.Errorf("monitor %d: %w: %w", id, action.ErrLimit, errNotAllowed)
	}

	return m, nil
}

// previewTag reads the monitors that have the tag.
func (d *downtimer) previewTag(ctx context.Context, tag string) any {
	monitors, err := d.client.monitorsWithTag(ctx, tag)
	if err != nil {
		return previewUnknown{Unavailable: source.CutText(err.Error(), previewMessageBytes)}
	}

	out := tagPreview{Count: len(monitors), AtLeast: len(monitors) >= monitorPageSize, First: []string{}}

	for _, m := range monitors[:min(len(monitors), previewShown)] {
		out.First = append(out.First, source.CutText(m.Name, previewNameBytes)+" ("+strconv.FormatInt(m.ID, 10)+")")
	}

	return out
}

// target names the downtime, as Snapshot.Target.
func target(req request) string {
	if req.monitorTag != "" {
		return source.CutText("datadog:downtime/tag/"+req.monitorTag, targetBytes)
	}

	return "datadog:downtime/monitor/" + strconv.FormatInt(req.monitorID, 10)
}

// readAfter reads the After of a snapshot and checks that it is the
// snapshot of these parameters, which Execute has just checked against the
// limits as they are now: the same monitor, scope, duration and message.
// Anything else is a permanent failure.
func readAfter(snap *action.Snapshot, req request) (afterDoc, error) {
	var doc afterDoc
	if err := json.Unmarshal(snap.After, &doc); err != nil {
		return afterDoc{}, fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadSnapshot, err)
	}

	if doc.MonitorID != req.monitorID || doc.MonitorTag != req.monitorTag || doc.Scope != req.scope ||
		doc.Duration != mute.FormatDuration(req.duration) || doc.Message != req.message || snap.Target != target(req) {
		return afterDoc{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	return doc, nil
}

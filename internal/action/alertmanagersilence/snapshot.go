package alertmanagersilence

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What the preview of a snapshot shows. They keep the whole snapshot short
// enough for an approver to read in Slack.
const (
	// previewShown is how many alerts the preview names.
	previewShown = 5
	// previewLabelBytes is the longest label text of one alert in the preview.
	previewLabelBytes = 100
	// previewMessageBytes is the longest error message in the preview.
	previewMessageBytes = 120
	// targetBytes is the longest target text.
	targetBytes = 100
)

// afterDoc is the After of a snapshot: the silence as the approver reads it.
// Execute makes the silence from Matchers, Duration, CreatedBy and Comment
// and nothing else. It starts the silence when it runs, adds the marker of
// the proposal to the comment, and never sends FiringNow, which is only for
// the approver.
type afterDoc struct {
	Matchers  []mute.Matcher `json:"matchers"`
	Duration  string         `json:"duration"`
	CreatedBy string         `json:"createdBy"`
	Comment   string         `json:"comment"`
	FiringNow any            `json:"firingNow"`
}

// firingNow is what the silence would mute at the time of the proposal.
type firingNow struct {
	AsOf  string   `json:"asOf"`
	Count int      `json:"count"`
	First []string `json:"first"`
}

// firingUnknown stands for the preview when the alerts could not be read.
type firingUnknown struct {
	AsOf        string `json:"asOf"`
	Unavailable string `json:"unavailable"`
}

// Snapshot implements action.Snapshotter. Nothing exists before the
// silence, so Before is null. After holds the matchers, the duration, the
// creator and the comment, and the firing alerts that match now, as
// information for the approver: a silence ahead of planned work that
// matches no alert is allowed. A preview that cannot be read does not
// refuse the proposal.
func (s *silencer) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	req, err := s.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	doc, err := json.Marshal(afterDoc{
		Matchers:  req.matchers,
		Duration:  mute.FormatDuration(req.duration),
		CreatedBy: s.createdBy,
		Comment:   req.comment,
		FiringNow: s.preview(ctx, req.matchers),
	})
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("encode the silence: %w", err)
	}

	return action.Snapshot{Target: target(req.matchers), After: doc}, nil
}

// preview reads the firing alerts that match the matchers.
func (s *silencer) preview(ctx context.Context, matchers []mute.Matcher) any {
	asOf := s.now().UTC().Format(time.RFC3339)

	alerts, err := s.client.alerts(ctx, matchers)
	if err != nil {
		return firingUnknown{AsOf: asOf, Unavailable: source.CutText(err.Error(), previewMessageBytes)}
	}

	out := firingNow{AsOf: asOf, Count: len(alerts), First: []string{}}

	for _, a := range alerts[:min(len(alerts), previewShown)] {
		out.First = append(out.First, describeAlert(a))
	}

	return out
}

// describeAlert writes an alert as its labels and its state, for the
// preview. The label values are text from alerts, so they are cut.
func describeAlert(a alert) string {
	parts := make([]string, 0, len(a.Labels))

	for _, name := range slices.Sorted(maps.Keys(a.Labels)) {
		parts = append(parts, name+`="`+a.Labels[name]+`"`)
	}

	return source.CutText(strings.Join(parts, " "), previewLabelBytes) + " (" + source.CutText(a.State, 20) + ")"
}

// target names the silence, as Snapshot.Target: the matchers as filters.
func target(matchers []mute.Matcher) string {
	exprs := make([]string, 0, len(matchers))
	for _, m := range matchers {
		exprs = append(exprs, m.Expression())
	}

	return source.CutText("alertmanager:silence{"+strings.Join(exprs, ",")+"}", targetBytes)
}

// readAfter reads the After of a snapshot and checks that it is the
// snapshot of these parameters, which Execute has just checked against the
// limits as they are now: the same matchers, duration and comment, and a
// creator name that is fit to send. Anything else is a permanent failure.
func readAfter(snap *action.Snapshot, req request) (afterDoc, error) {
	var doc afterDoc
	if err := json.Unmarshal(snap.After, &doc); err != nil {
		return afterDoc{}, fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadSnapshot, err)
	}

	if !mute.SameMatchers(doc.Matchers, req.matchers) || snap.Target != target(req.matchers) ||
		doc.Duration != mute.FormatDuration(req.duration) || doc.Comment != req.comment {
		return afterDoc{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	if doc.CreatedBy == "" || checkCreatedBy(doc.CreatedBy) != nil {
		return afterDoc{}, fmt.Errorf("%w: %w: the creator name is not usable", action.ErrPermanent, errBadSnapshot)
	}

	return doc, nil
}

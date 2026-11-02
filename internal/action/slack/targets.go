//go:generate mockgen -source targets.go -destination mocks_targets.go -package slack -write_package_comment=false

package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Target is a Slack message: the channel it is in and its timestamp.
type Target struct {
	Channel string
	TS      string
}

// Targets finds the Slack messages of an investigation.
type Targets interface {
	// Thread returns the root of the thread where the investigation's
	// report is: the thread of the first Slack output that published. When
	// the investigation has no such thread, it returns the thread of the
	// alert message. It fails with an error wrapping action.ErrPermanent
	// when the investigation has neither.
	Thread(ctx context.Context, investigationID string) (Target, error)
	// Alert returns the Slack message that carried the alert. It fails with
	// an error wrapping action.ErrPermanent when the alert did not come
	// from Slack.
	Alert(ctx context.Context, investigationID string) (Target, error)
}

var (
	// channelPattern is what the ID of a public or private channel looks like.
	channelPattern = regexp.MustCompile(`^[CG][A-Z0-9]{6,20}$`)
	// tsPattern is what the timestamp of a Slack message looks like.
	tsPattern = regexp.MustCompile(`^[0-9]{9,11}\.[0-9]{6}$`)
)

var (
	errNoDB     = errors.New("no database to find the Slack message in")
	errNoThread = errors.New("the investigation has no Slack thread to reply in")
	errNoAlert  = errors.New("the alert did not come from a Slack message")
	errBadID    = errors.New("the investigation ID is not a UUID")
)

// dbTargets finds the targets in Sidekick's database.
type dbTargets struct {
	db db.DBTX
}

var _ Targets = dbTargets{}

// Thread implements Targets.
func (t dbTargets) Thread(ctx context.Context, investigationID string) (Target, error) {
	id, err := parseID(investigationID, t.db)
	if err != nil {
		return Target{}, err
	}

	refs, err := db.New(t.db).ListInvestigationOutputRefs(ctx, id)
	if err != nil {
		return Target{}, fmt.Errorf("list where the outputs published: %w", err)
	}

	for _, r := range refs {
		if target, ok := parseExternal(r.External); ok {
			return target, nil
		}
	}

	alert, err := t.alert(ctx, id)
	switch {
	case errors.Is(err, errNoAlert):
		return Target{}, fmt.Errorf("%w: %w: %w", action.ErrPermanent, errNoThread, err)
	case err != nil:
		return Target{}, err
	}

	if alert.thread != "" {
		return Target{Channel: alert.channel, TS: alert.thread}, nil
	}

	return Target{Channel: alert.channel, TS: alert.ts}, nil
}

// Alert implements Targets.
func (t dbTargets) Alert(ctx context.Context, investigationID string) (Target, error) {
	id, err := parseID(investigationID, t.db)
	if err != nil {
		return Target{}, err
	}

	alert, err := t.alert(ctx, id)
	if err != nil {
		return Target{}, err
	}

	return Target{Channel: alert.channel, TS: alert.ts}, nil
}

// alertRef is the Slack message of an alert.
type alertRef struct {
	channel, ts, thread string
}

// alert reads the Slack reference of the investigation's alert.
func (t dbTargets) alert(ctx context.Context, id uuid.UUID) (alertRef, error) {
	raw, err := db.New(t.db).GetInvestigationAlertSlackRef(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return alertRef{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoAlert)
	}

	if err != nil {
		return alertRef{}, fmt.Errorf("read the Slack message of the alert: %w", err)
	}

	var ref event.SlackRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return alertRef{}, fmt.Errorf("%w: decode the Slack message of the alert: %w", action.ErrPermanent, err)
	}

	if !channelPattern.MatchString(ref.Channel) || !tsPattern.MatchString(ref.TS) {
		return alertRef{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoAlert)
	}

	thread := ""
	if tsPattern.MatchString(ref.ThreadTS) {
		thread = ref.ThreadTS
	}

	return alertRef{channel: ref.Channel, ts: ref.TS, thread: thread}, nil
}

// parseID reads the ID of an investigation. A missing database is an error
// too, since nothing can be found without one.
func parseID(investigationID string, conn db.DBTX) (uuid.UUID, error) {
	if conn == nil {
		return uuid.Nil, fmt.Errorf("%w: %w", action.ErrPermanent, errNoDB)
	}

	id, err := uuid.Parse(investigationID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %w", action.ErrPermanent, errBadID)
	}

	return id, nil
}

// parseExternal reads the external ID a Slack output stores, "channel:ts",
// and reports whether it is one. The external IDs of other outputs do not
// have that form.
func parseExternal(external string) (Target, bool) {
	channel, ts, ok := strings.Cut(external, ":")
	if !ok || !channelPattern.MatchString(channel) || !tsPattern.MatchString(ts) {
		return Target{}, false
	}

	return Target{Channel: channel, TS: ts}, true
}

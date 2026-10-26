package slack

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Type is the output type this package registers. A config selects it with
// "type: slack".
const Type = "slack"

// editInterval is the shortest time between two edits of one message.
const editInterval = 3 * time.Second

// errMessageNotFound is the Slack error for a message that was deleted.
const errMessageNotFound = "message_not_found"

// announcedFailed is what Ref.Data["announced"] holds after the short
// reply of a run that could not finish.
const announcedFailed = "failed"

// The keys of Ref.Data.
const (
	dataChannel   = "channel"
	dataTS        = "ts"
	dataThreadTS  = "thread_ts"
	dataSeq       = "seq"
	dataEditedAt  = "edited_at"
	dataAnnounced = "announced"
)

var (
	errNoChannel = errors.New("channel: required")
	errBadAPIURL = errors.New(`apiURL: expected an absolute http or https URL ending in "/", such as https://slack.com/api/`)
	errNoToken   = errors.New("the slack section with botTokenSecret is required for a Slack output")
	errNoSecrets = errors.New("no secrets resolver to read the bot token with")
)

// init registers the output type when the package is linked in.
func init() {
	registry.Outputs.Register(Type, registry.Typed(New))
}

// Config is the section of one Slack output.
type Config struct {
	// Channel is where the message for an event that did not come from
	// Slack is posted, such as a webhook alert: a channel name like
	// "#alerts" or a channel ID. An event from a Slack message is answered
	// in that message's thread instead.
	Channel string `yaml:"channel"`
	// APIURL is the base URL of the Slack Web API, ending in "/". Empty
	// means DefaultAPIURL; GovSlack workspaces use
	// https://slack-gov.com/api/.
	APIURL string `yaml:"apiURL"`
}

// Validate checks that the channel is set and that apiURL, when set, is an
// absolute http or https URL ending in "/".
func (c *Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Channel) == "" {
		errs = append(errs, errNoChannel)
	}

	if c.APIURL != "" {
		u, err := url.Parse(c.APIURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !strings.HasSuffix(u.Path, "/") {
			errs = append(errs, errBadAPIURL)
		}
	}

	return errors.Join(errs...)
}

// Sink posts and edits one Slack message per investigation.
type Sink struct {
	name    string
	channel string
	client  *client
	log     *slog.Logger
	// now and wait are the clock. Tests replace them.
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
}

var _ output.Sink = (*Sink)(nil)

// ref is what Ref.Data holds about the main message.
type ref struct {
	channel  string
	ts       string
	threadTS string
	seq      int64
	editedAt time.Time
	// announced is the verdict the last short reply announced, or
	// announcedFailed.
	announced string
}

// Accepts reports whether the sink shows updates of phase: it shows the
// queued, running and final phases.
func (s *Sink) Accepts(phase output.Phase) bool {
	switch phase {
	case output.PhaseQueued, output.PhaseRunning, output.PhaseFinal:
		return true
	default:
		return false
	}
}

// Publish posts the main message, or edits it when u.Previous is set. An
// update whose Seq is not newer than the one the message shows is dropped,
// and an edit waits until editInterval after the last one. A final update
// also posts the rest of a long report and a short verdict reply in the
// thread, once per verdict, so a retried final does not announce twice. A
// message that was deleted is posted again.
func (s *Sink) Publish(ctx context.Context, u output.Update) (output.Ref, error) {
	if u.Previous == nil {
		return s.post(ctx, u)
	}

	prev, ok := refOf(*u.Previous)
	if !ok {
		return s.post(ctx, u)
	}

	if u.Seq <= prev.seq {
		s.log.DebugContext(ctx, "update dropped: the message shows a newer one", "investigation_id", u.InvestigationID, "seq", u.Seq, "shown_seq", prev.seq)

		return *u.Previous, nil
	}

	if d := prev.editedAt.Add(editInterval).Sub(s.now()); d > 0 {
		if err := s.wait(ctx, d); err != nil {
			return output.Ref{}, fmt.Errorf("wait for the next edit: %w", err)
		}
	}

	v := mainView(u)

	_, err := s.client.update(ctx, message{Channel: prev.channel, TS: prev.ts, Text: v.text, Attachments: []attachment{v.attachment}})
	if apiErr, isAPI := errors.AsType[*apiError](err); isAPI && apiErr.Code == errMessageNotFound {
		s.log.InfoContext(ctx, "the Slack message was deleted; posting it again", "investigation_id", u.InvestigationID)

		return s.post(ctx, u)
	}

	if err != nil {
		return output.Ref{}, fmt.Errorf("edit the message: %w", err)
	}

	prev.seq, prev.editedAt = u.Seq, s.now()

	return s.finish(ctx, u, prev)
}

// post posts the main message: in the thread of the Slack message the
// event came from, or else in the configured channel.
func (s *Sink) post(ctx context.Context, u output.Update) (output.Ref, error) {
	channel, threadTS := s.channel, ""
	if u.Event.Slack != nil && u.Event.Slack.Channel != "" {
		channel, threadTS = u.Event.Slack.Channel, u.Event.Slack.TS
		if u.Event.Slack.ThreadTS != "" {
			threadTS = u.Event.Slack.ThreadTS
		}
	}

	v := mainView(u)

	p, err := s.client.postMessage(ctx, message{Channel: channel, ThreadTS: threadTS, Text: v.text, Attachments: []attachment{v.attachment}})
	if err != nil {
		return output.Ref{}, fmt.Errorf("post the message: %w", err)
	}

	r := ref{channel: p.Channel, ts: p.TS, threadTS: threadTS, seq: u.Seq, editedAt: s.now()}
	if r.threadTS == "" {
		r.threadTS = p.TS
	}

	return s.finish(ctx, u, r)
}

// finish posts the thread replies of a final update that announces a new
// verdict, and returns the ref of the main message.
func (s *Sink) finish(ctx context.Context, u output.Update, r ref) (output.Ref, error) {
	verdict := announcedFailed
	if !failed(u.Report) {
		verdict = string(u.Report.Verdict)
	}

	if u.Phase != output.PhaseFinal || r.announced == verdict {
		return s.outputRef(r), nil
	}

	if more, ok := continuationView(u); ok {
		if err := s.reply(ctx, r, more); err != nil {
			return output.Ref{}, fmt.Errorf("post the rest of the report: %w", err)
		}
	}

	permalink, err := s.client.permalink(ctx, r.channel, r.ts)
	if err != nil {
		// The reply is still worth posting without its link.
		s.log.WarnContext(ctx, "could not get a link to the Slack message", "investigation_id", u.InvestigationID, "error", err)
	}

	if err := s.reply(ctx, r, verdictReplyView(u, permalink)); err != nil {
		return output.Ref{}, fmt.Errorf("post the verdict reply: %w", err)
	}

	r.announced = verdict

	return s.outputRef(r), nil
}

// reply posts v in the thread of the main message.
func (s *Sink) reply(ctx context.Context, r ref, v view) error {
	_, err := s.client.postMessage(ctx, message{Channel: r.channel, ThreadTS: r.threadTS, Text: v.text, Attachments: []attachment{v.attachment}})
	if err != nil {
		return fmt.Errorf("reply in the thread: %w", err)
	}

	return nil
}

// outputRef returns r as the ref the publish job stores.
func (s *Sink) outputRef(r ref) output.Ref {
	data := map[string]string{
		dataChannel:  r.channel,
		dataTS:       r.ts,
		dataThreadTS: r.threadTS,
		dataSeq:      strconv.FormatInt(r.seq, 10),
		dataEditedAt: strconv.FormatInt(r.editedAt.UnixMilli(), 10),
	}

	if r.announced != "" {
		data[dataAnnounced] = r.announced
	}

	return output.Ref{Sink: s.name, External: r.channel + ":" + r.ts, Data: data}
}

// New builds a Slack output. It posts with the bot token of the slack
// section, read through deps.Secrets on every call, and sends requests
// with deps.HTTP, or a client from httpclient.New when it is nil.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error) {
	s, err := newSink(name, cfg, deps, cmp.Or(cfg.APIURL, DefaultAPIURL))
	if err != nil {
		return nil, fmt.Errorf("slack output %q: %w", name, err)
	}

	return s, nil
}

// newSink builds a sink that calls the Web API at apiURL.
func newSink(name string, cfg Config, deps registry.Deps, apiURL string) (*Sink, error) {
	if deps.SlackBotToken == (registry.SecretRef{}) {
		return nil, errNoToken
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	m, err := newMetrics(deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up metrics: %w", err)
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	token := func(ctx context.Context) (string, error) {
		t, err := secrets.ResolveText(ctx, deps.Secrets, deps.SlackBotToken)
		if err != nil {
			return "", fmt.Errorf("read slack.botTokenSecret: %w", err)
		}

		return t, nil
	}

	return &Sink{
		name:    name,
		channel: strings.TrimSpace(cfg.Channel),
		client:  newClient(apiURL, hc, token, m),
		log:     log.With("component", "output", "sink", name),
		now:     time.Now,
		wait:    sleep,
	}, nil
}

// refOf reads the main message from a stored ref. It reports false when
// the ref does not name a message.
func refOf(r output.Ref) (ref, bool) {
	out := ref{
		channel:   r.Data[dataChannel],
		ts:        r.Data[dataTS],
		threadTS:  r.Data[dataThreadTS],
		announced: r.Data[dataAnnounced],
	}

	if out.channel == "" || out.ts == "" {
		return ref{}, false
	}

	if out.threadTS == "" {
		out.threadTS = out.ts
	}

	if seq, err := strconv.ParseInt(r.Data[dataSeq], 10, 64); err == nil {
		out.seq = seq
	}

	if ms, err := strconv.ParseInt(r.Data[dataEditedAt], 10, 64); err == nil {
		out.editedAt = time.UnixMilli(ms)
	}

	return out, true
}

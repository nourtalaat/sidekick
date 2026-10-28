package teams

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	bot "github.com/nourtalaat/sidekick/internal/teams"
)

// Type is the output type this package registers. A config selects it with
// "type: teams".
const Type = "teams"

// editInterval is the shortest time between two edits of one message.
const editInterval = 3 * time.Second

// announcedFailed is what Ref.Data["announced"] holds after the short
// reply of a run that could not finish.
const announcedFailed = "failed"

// The keys of Ref.Data.
const (
	dataServiceURL = "service_url"
	dataChannel    = "channel"
	dataThread     = "thread"
	dataActivity   = "activity"
	dataSeq        = "seq"
	dataEditedAt   = "edited_at"
	dataAnnounced  = "announced"
)

// channelPattern is a Teams channel's conversation ID.
var channelPattern = regexp.MustCompile(`^19:[A-Za-z0-9_.=-]+@thread\.(tacv2|skype)$`)

var (
	errBadChannel = errors.New("channel: expected a Teams channel ID such as 19:abc123@thread.tacv2")
	errNoBot      = errors.New("the teams section is required for a Teams output")
	errNoSecrets  = errors.New("no secrets resolver to read the client secret with")
)

// init registers the output type when the package is linked in.
func init() {
	registry.Outputs.Register(Type, registry.Typed(New))
}

// Config is the section of one Teams output.
type Config struct {
	// Channel is the ID of the channel where the message for an event that
	// did not come from Teams starts a new thread, such as a webhook alert.
	// An event from a Teams message is answered in that message's thread
	// instead.
	Channel string `yaml:"channel"`
}

// Validate checks that the channel is a Teams channel ID.
func (c *Config) Validate() error {
	if !channelPattern.MatchString(strings.TrimSpace(c.Channel)) {
		return errBadChannel
	}

	return nil
}

// Sink posts and edits one Teams message per investigation.
type Sink struct {
	name       string
	channel    string
	tenant     string
	serviceURL string
	client     *client
	log        *slog.Logger
	// now and wait are the clock. Tests replace them.
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error
}

var _ output.Sink = (*Sink)(nil)

// ref is what Ref.Data holds about the main message.
type ref struct {
	serviceURL string
	channel    string
	thread     string
	activity   string
	seq        int64
	editedAt   time.Time
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

	err := s.client.update(ctx, prev.serviceURL, conversationOf(prev.channel, prev.thread), prev.activity, mainView(u))
	if errors.Is(err, errNotFound) {
		s.log.InfoContext(ctx, "the Teams message was deleted; posting it again", "investigation_id", u.InvestigationID)

		return s.post(ctx, u)
	}

	if err != nil {
		return output.Ref{}, fmt.Errorf("edit the message: %w", err)
	}

	prev.seq, prev.editedAt = u.Seq, s.now()

	return s.finish(ctx, u, prev)
}

// post posts the main message: in the thread of the Teams message the
// event came from, or else as a new thread in the configured channel.
func (s *Sink) post(ctx context.Context, u output.Update) (output.Ref, error) {
	r := ref{serviceURL: s.serviceURL, channel: s.channel, seq: u.Seq}
	a := mainView(u)

	if t := u.Event.Teams; t != nil && t.ChannelID != "" && t.ThreadID != "" {
		r.serviceURL, r.channel, r.thread = cmp.Or(t.ServiceURL, s.serviceURL), t.ChannelID, t.ThreadID

		id, err := s.client.reply(ctx, r.serviceURL, conversationOf(r.channel, r.thread), a)
		if err != nil {
			return output.Ref{}, fmt.Errorf("post the message: %w", err)
		}

		r.activity = id
	} else {
		id, err := s.client.startThread(ctx, r.serviceURL, r.channel, s.tenant, a)
		if err != nil {
			return output.Ref{}, fmt.Errorf("post the message: %w", err)
		}

		r.activity, r.thread = id, id
	}

	r.editedAt = s.now()

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

	conversation := conversationOf(r.channel, r.thread)

	if more, ok := continuationView(u); ok {
		if _, err := s.client.reply(ctx, r.serviceURL, conversation, more); err != nil {
			return output.Ref{}, fmt.Errorf("post the rest of the report: %w", err)
		}
	}

	if _, err := s.client.reply(ctx, r.serviceURL, conversation, verdictReplyView(u)); err != nil {
		return output.Ref{}, fmt.Errorf("post the verdict reply: %w", err)
	}

	r.announced = verdict

	return s.outputRef(r), nil
}

// outputRef returns r as the ref the publish job stores.
func (s *Sink) outputRef(r ref) output.Ref {
	data := map[string]string{
		dataServiceURL: r.serviceURL,
		dataChannel:    r.channel,
		dataThread:     r.thread,
		dataActivity:   r.activity,
		dataSeq:        strconv.FormatInt(r.seq, 10),
		dataEditedAt:   strconv.FormatInt(r.editedAt.UnixMilli(), 10),
	}

	if r.announced != "" {
		data[dataAnnounced] = r.announced
	}

	return output.Ref{Sink: s.name, External: r.channel + "/" + r.activity, Data: data}
}

// New builds a Teams output. It posts as the bot of the teams section,
// with a token from deps.TeamsBot's credential, and sends requests with
// deps.HTTP, or a client from httpclient.New when it is nil.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error) {
	s, err := newSink(name, cfg, deps, "")
	if err != nil {
		return nil, fmt.Errorf("teams output %q: %w", name, err)
	}

	return s, nil
}

// newSink builds a sink that gets its tokens from loginURL, or from
// Microsoft Entra ID when it is empty.
func newSink(name string, cfg Config, deps registry.Deps, loginURL string) (*Sink, error) {
	b := deps.TeamsBot
	if b.AppID == "" {
		return nil, errNoBot
	}

	if !b.WorkloadIdentity && deps.Secrets == nil {
		return nil, errNoSecrets
	}

	serviceURL := ensureSlash(cmp.Or(b.ServiceURL, DefaultServiceURL))
	if err := bot.CheckBaseURL(serviceURL); err != nil {
		return nil, fmt.Errorf("service URL: %w", err)
	}

	u, err := url.Parse(serviceURL)
	if err != nil {
		return nil, fmt.Errorf("parse the service URL: %w", err)
	}

	m, err := newMetrics(deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up metrics: %w", err)
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	tokens, err := bot.NewTokens(bot.TokenOptions{
		Credentials: bot.Credentials{AppID: b.AppID, TenantID: b.TenantID, ClientSecret: b.ClientSecret, WorkloadIdentity: b.WorkloadIdentity},
		Scope:       bot.BotFrameworkScope,
		Secrets:     deps.Secrets,
		HTTP:        hc,
		LoginURL:    loginURL,
	})
	if err != nil {
		return nil, fmt.Errorf("set up Bot Framework tokens: %w", err)
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Sink{
		name:       name,
		channel:    strings.TrimSpace(cfg.Channel),
		tenant:     b.TenantID,
		serviceURL: serviceURL,
		client: &client{
			http:        hc,
			tokens:      tokens,
			metrics:     m,
			allowedHost: strings.ToLower(u.Hostname()),
			wait:        sleep,
			limit:       conversationRate,
			limiters:    map[string]*rate.Limiter{},
		},
		log:  log.With("component", "output", "sink", name),
		now:  time.Now,
		wait: sleep,
	}, nil
}

// refOf reads the main message from a stored ref. It reports false when
// the ref does not name a message.
func refOf(r output.Ref) (ref, bool) {
	out := ref{
		serviceURL: r.Data[dataServiceURL],
		channel:    r.Data[dataChannel],
		thread:     r.Data[dataThread],
		activity:   r.Data[dataActivity],
		announced:  r.Data[dataAnnounced],
	}

	if out.serviceURL == "" || out.channel == "" || out.thread == "" || out.activity == "" {
		return ref{}, false
	}

	if seq, err := strconv.ParseInt(r.Data[dataSeq], 10, 64); err == nil {
		out.seq = seq
	}

	if ms, err := strconv.ParseInt(r.Data[dataEditedAt], 10, 64); err == nil {
		out.editedAt = time.UnixMilli(ms)
	}

	return out, true
}

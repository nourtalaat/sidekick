package slackapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/output"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// InteractivityPattern is the route interactions are served at in events
// mode, for http.ServeMux.
const InteractivityPattern = "POST /slack/interactivity"

// noteTimeout bounds posting a note back to the user who clicked, so the
// click is still acknowledged within Slack's 3 seconds.
const noteTimeout = 1500 * time.Millisecond

// payloadField is the form field that holds an interaction's JSON.
const payloadField = "payload"

// The notes the user who clicked sees, only to them.
const (
	noteDenied    = "Only responders can rate verdicts or stop investigations."
	noteNoVerdict = "This investigation has no verdict to rate yet."
	noteStopping  = "Stopping the investigation. The message shows what it found so far once it stops."
	noteEnded     = "This investigation has ended already, or someone stopped it."
	noteFailed    = "Sidekick could not record that click. Try again in a moment."
)

// refresher is a role lookup that can read a user's role again, for a
// user it would refuse, such as one just added to a Slack user group.
// slackroles.Lookup is one.
type refresher interface {
	Refresh(ctx context.Context, teamID, userID string) (roles.Role, error)
}

// errBadResponseURL is returned for a response URL that is not Slack's.
var errBadResponseURL = errors.New("the response URL is not a Slack URL")

// ButtonsOptions set up Buttons. Handler, Pool, Roles and Logger are
// required.
type ButtonsOptions struct {
	// Handler checks the signature of POST /slack/interactivity.
	Handler *Handler
	// Pool is the database the investigations and feedback are in.
	Pool *pgxpool.Pool
	// Roles finds the role of the user who clicked, at the time of the
	// click. When it also has a Refresh method, a user it would refuse
	// has their role read again first.
	Roles roles.Lookup
	// Metrics counts stored ratings. Nil counts nothing.
	Metrics *feedback.Metrics
	// HTTP posts notes to the user who clicked. Nil means
	// httpclient.New().
	HTTP *http.Client
	// Logger gets one line per click, with IDs only.
	Logger *slog.Logger
}

// Buttons handles clicks on the buttons of Sidekick's messages: Correct
// and Wrong store a rating of the verdict, and Stop stops the run. Every
// click needs the responder role at the time of the click. It answers
// interactions from Socket Mode as an InteractionHandler, and at
// POST /slack/interactivity in events mode.
type Buttons struct {
	handler *Handler
	pool    *pgxpool.Pool
	roles   roles.Lookup
	metrics *feedback.Metrics
	http    *http.Client
	log     *slog.Logger
}

var _ InteractionHandler = (*Buttons)(nil)

// NewButtons returns Buttons.
func NewButtons(opts ButtonsOptions) *Buttons {
	hc := opts.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	return &Buttons{
		handler: opts.Handler,
		pool:    opts.Pool,
		roles:   opts.Roles,
		metrics: opts.Metrics,
		http:    hc,
		log:     opts.Logger,
	}
}

// ServeHTTP serves interactions at InteractivityPattern. It checks the
// request's signature before it parses the body, then handles the click
// and answers 200 with an empty body. It answers 413 for a body over
// MaxBodyBytes, 401 when the signature or its timestamp is wrong, and 400
// for a body that is not an interaction.
func (b *Buttons) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h := b.handler

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			h.reject(ctx, w, ingest.ReasonTooLarge, http.StatusRequestEntityTooLarge, msgTooLarge, err)

			return
		}

		h.reject(ctx, w, ingest.ReasonInvalidPayload, http.StatusBadRequest, msgUnreadable, err)

		return
	}

	if err := h.Verify(ctx, r.Header, body); err != nil {
		if !errors.Is(err, intake.ErrUnauthenticated) {
			h.fail(ctx, w, "Slack interaction not checked: the signing secret could not be read", err)

			return
		}

		h.reject(ctx, w, ingest.ReasonUnauthenticated, http.StatusUnauthorized, msgUnauthenticated, err)

		return
	}

	cb, err := decodeInteraction(body)
	if err != nil {
		h.reject(ctx, w, ingest.ReasonInvalidPayload, http.StatusBadRequest, msgInvalidPayload, err)

		return
	}

	if _, err := b.HandleInteraction(ctx, cb); err != nil {
		h.fail(ctx, w, "Slack interaction failed", err)

		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleInteraction handles a click on one of Sidekick's buttons and
// answers with an empty ack. Clicks on other buttons, and other kinds of
// interaction, are acknowledged and ignored. What the user needs to know,
// such as a denied role, is posted to them alone through the response
// URL. It returns an error only when the click could not be recorded.
func (b *Buttons) HandleInteraction(ctx context.Context, cb slack.InteractionCallback) ([]byte, error) {
	if cb.Type != slack.InteractionTypeBlockActions {
		return nil, nil
	}

	action, ok := ourAction(cb.ActionCallback.BlockActions)
	if !ok {
		return nil, nil
	}

	log := b.log.With("action", action.ActionID, "user_id", cb.User.ID)

	id, ok := investigationID(action.Value)
	if !ok {
		log.WarnContext(ctx, "ignoring a click whose value is not an investigation ID")

		return nil, nil
	}

	log = log.With("investigation_id", id.String())

	role, err := b.role(ctx, cb.Team.ID, cb.User.ID)
	if err != nil {
		b.note(ctx, log, cb, noteFailed)

		return nil, fmt.Errorf("look up the role of the user who clicked: %w", err)
	}

	if !role.Has(roles.Responder) {
		log.InfoContext(ctx, "click denied: the user is not a responder")
		b.note(ctx, log, cb, noteDenied)

		return nil, nil
	}

	var note string

	switch action.ActionID {
	case slackout.ActionStop:
		note, err = b.stop(ctx, log, id, cb.User.ID)
	case slackout.ActionVerdictCorrect:
		note, err = b.rate(ctx, log, id, cb.User.ID, output.RatingCorrect)
	default:
		note, err = b.rate(ctx, log, id, cb.User.ID, output.RatingWrong)
	}

	if err != nil {
		b.note(ctx, log, cb, noteFailed)

		return nil, fmt.Errorf("investigation %s: %w", id, err)
	}

	if note != "" {
		b.note(ctx, log, cb, note)
	}

	return nil, nil
}

// role returns the role of the user who clicked. A user below responder
// has their role read again when the lookup can do that, so a user just
// given a higher role is not refused.
func (b *Buttons) role(ctx context.Context, teamID, userID string) (roles.Role, error) {
	return responderRole(ctx, b.roles, teamID, userID)
}

// rate stores a rating of the verdict and shows it in the outputs, in one
// transaction, and returns the note for the user, if any.
func (b *Buttons) rate(ctx context.Context, log *slog.Logger, id uuid.UUID, userID string, rating output.Rating) (string, error) {
	var res feedback.Result

	err := pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		var err error

		res, err = feedback.RateVerdict(ctx, tx, feedback.VerdictRating{InvestigationID: id, Rating: rating, SlackUserID: userID})
		if err != nil {
			return fmt.Errorf("rate the verdict: %w", err)
		}

		if !res.Stored {
			return nil
		}

		if err := publish.Refresh(ctx, tx, id); err != nil {
			return fmt.Errorf("show the rating: %w", err)
		}

		return nil
	})
	if errors.Is(err, feedback.ErrNotRatable) || errors.Is(err, feedback.ErrNotFound) {
		return noteNoVerdict, nil
	}

	if err != nil {
		return "", fmt.Errorf("rate: %w", err)
	}

	if res.Stored {
		b.metrics.Stored(feedback.TargetVerdict, rating)
	}

	log.InfoContext(ctx, "verdict rated", "rating", string(rating), "stored", res.Stored, "confirmation", string(res.Confirmation))

	return "", nil
}

// stop asks the run to stop and returns the note for the user.
func (b *Buttons) stop(ctx context.Context, log *slog.Logger, id uuid.UUID, userID string) (string, error) {
	var asked bool

	err := pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error {
		var err error

		asked, err = investigator.RequestCancel(ctx, tx, id, investigator.Requester{SlackUserID: userID})
		if err != nil {
			return fmt.Errorf("ask the run to stop: %w", err)
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("stop: %w", err)
	}

	log.InfoContext(ctx, "stop asked for", "asked", asked)

	if !asked {
		return noteEnded, nil
	}

	return noteStopping, nil
}

// note posts text to the user who clicked, visible only to them, through
// the interaction's response URL. A note that cannot be posted is logged
// and dropped: the click itself has been handled.
func (b *Buttons) note(ctx context.Context, log *slog.Logger, cb slack.InteractionCallback, text string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noteTimeout)
	defer cancel()

	if err := b.postNote(ctx, cb.ResponseURL, text); err != nil {
		log.WarnContext(ctx, "could not post a note to the user who clicked", "error", err.Error())
	}
}

// postNote posts an ephemeral message with text to responseURL, which must
// be a Slack URL: https on a slack.com or slack-gov.com host, or a loopback
// host as in tests.
func (b *Buttons) postNote(ctx context.Context, responseURL, text string) error {
	if !slackURL(responseURL) {
		return errBadResponseURL
	}

	body, err := json.Marshal(map[string]any{"response_type": "ephemeral", "replace_original": false, "text": text})
	if err != nil {
		return fmt.Errorf("encode the note: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the note: %w", err)
	}

	defer func() {
		// The body is not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send the note: HTTP status %d", resp.StatusCode)
	}

	return nil
}

// ourAction returns the first of actions that is one of Sidekick's
// buttons.
func ourAction(actions []*slack.BlockAction) (*slack.BlockAction, bool) {
	for _, a := range actions {
		switch a.ActionID {
		case slackout.ActionVerdictCorrect, slackout.ActionVerdictWrong, slackout.ActionStop:
			return a, true
		}
	}

	return nil, false
}

// investigationID returns the investigation ID a button carries as its
// value, and false when the value is not one.
func investigationID(value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)

	return id, err == nil
}

// decodeInteraction reads the interaction in a form-encoded body.
func decodeInteraction(body []byte) (slack.InteractionCallback, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return slack.InteractionCallback{}, fmt.Errorf("%w: parse the form: %w", ErrInvalidPayload, err)
	}

	raw := form.Get(payloadField)
	if raw == "" {
		return slack.InteractionCallback{}, fmt.Errorf("%w: no payload field", ErrInvalidPayload)
	}

	var cb slack.InteractionCallback
	if err := json.Unmarshal([]byte(raw), &cb); err != nil {
		return slack.InteractionCallback{}, fmt.Errorf("%w: decode the payload: %w", ErrInvalidPayload, err)
	}

	return cb, nil
}

// slackURL reports whether raw is a URL Slack would send as a response
// URL.
func slackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}

	host := u.Hostname()
	if u.Scheme == "http" || u.Scheme == "https" {
		if httpclient.IsLoopbackHost(host) {
			return true
		}
	}

	if u.Scheme != "https" {
		return false
	}

	for _, domain := range []string{"slack.com", "slack-gov.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}

	return false
}

// responderRole returns the role lookup gives userID in teamID. A user
// below responder has their role read again when lookup can do that, so a
// user just given a higher role, such as one just added to a Slack user
// group, is not refused.
func responderRole(ctx context.Context, lookup roles.Lookup, teamID, userID string) (roles.Role, error) {
	role, err := lookup.Role(ctx, teamID, userID)
	if err != nil {
		return 0, fmt.Errorf("look up the role: %w", err)
	}

	r, ok := lookup.(refresher)
	if role.Has(roles.Responder) || !ok {
		return role, nil
	}

	role, err = r.Refresh(ctx, teamID, userID)
	if err != nil {
		return 0, fmt.Errorf("look up the role again: %w", err)
	}

	return role, nil
}

package teamsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/teams"
)

// MessagesPattern is the route the bot's messaging endpoint is served at,
// for http.ServeMux. The Azure Bot's messaging endpoint must be this path
// on a public https URL.
const MessagesPattern = "POST /teams/messages"

// MaxBodyBytes is the largest activity accepted: 1 MiB.
const MaxBodyBytes = 1 << 20

// rejectedIntake is the intake label of activities turned away before
// their event is known. Intake names cannot start with "_".
const rejectedIntake = "_teams"

// The messages of error responses. They never say which check failed.
const (
	msgTooLarge        = "the body is larger than 1 MiB"
	msgUnreadable      = "the body could not be read"
	msgUnauthenticated = "unauthenticated"
	msgForbidden       = "forbidden"
	msgInvalidPayload  = "invalid payload"
	msgInternal        = "internal error"
)

var (
	errOtherTenant    = errors.New("the activity is from another tenant")
	errOtherRecipient = errors.New("the activity is for another bot")
)

// ServeHTTP serves the messaging endpoint at MessagesPattern. Each
// activity's Bot Framework token is checked before its event is looked at
// (see teams.Verifier.Verify), and an activity must be for the bot and
// from the bot's tenant, so nothing from another tenant is ever stored. It
// answers 200 once the event is stored or when Sidekick ignores the
// activity, 413 for a body over MaxBodyBytes, 400 for a body that is not
// an activity, 401 when the token is missing or wrong, 403 for another
// tenant or bot, and 500 when the event could not be stored or the
// signing keys could not be read.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			h.reject(ctx, w, ingest.ReasonTooLarge, http.StatusRequestEntityTooLarge, msgTooLarge, err)

			return
		}

		h.reject(ctx, w, ingest.ReasonInvalidPayload, http.StatusBadRequest, msgUnreadable, err)

		return
	}

	authorization := r.Header.Get("Authorization")
	if authorization == "" {
		h.reject(ctx, w, ingest.ReasonUnauthenticated, http.StatusUnauthorized, msgUnauthenticated, fmt.Errorf("no Authorization header: %w", intake.ErrUnauthenticated))

		return
	}

	var a activity
	if err := json.Unmarshal(body, &a); err != nil {
		h.reject(ctx, w, ingest.ReasonInvalidPayload, http.StatusBadRequest, msgInvalidPayload, err)

		return
	}

	if err := h.verifier.Verify(ctx, authorization, a.ChannelID, a.ServiceURL); err != nil {
		if !errors.Is(err, intake.ErrUnauthenticated) {
			h.fail(ctx, w, "Teams activity not checked: the Bot Framework signing keys could not be read", err)

			return
		}

		h.reject(ctx, w, ingest.ReasonUnauthenticated, http.StatusUnauthorized, msgUnauthenticated, err)

		return
	}

	if err := h.checkAudience(a); err != nil {
		h.reject(ctx, w, ingest.ReasonUnauthenticated, http.StatusForbidden, msgForbidden, err)

		return
	}

	if err := h.handle(ctx, a, body); err != nil {
		h.fail(ctx, w, "Teams event not stored", err)

		return
	}

	w.WriteHeader(http.StatusOK)
}

// checkAudience checks that a verified activity is a Teams activity for
// this bot from the bot's tenant. Every tenant the activity states must be
// the bot's, and it must state one.
func (h *Handler) checkAudience(a activity) error {
	if a.ChannelID != teams.ChannelTeams {
		return fmt.Errorf("channel %q is not Teams: %w", a.ChannelID, errOtherRecipient)
	}

	if !strings.EqualFold(a.Recipient.ID, botIDPrefix+h.appID) {
		return errOtherRecipient
	}

	tenants := a.tenants()
	if len(tenants) == 0 {
		return fmt.Errorf("the activity states no tenant: %w", errOtherTenant)
	}

	for _, t := range tenants {
		if !strings.EqualFold(t, h.tenantID) {
			return errOtherTenant
		}
	}

	return nil
}

// reject answers an activity turned away for reason and counts it. err
// says why in the log; it never holds message text.
func (h *Handler) reject(ctx context.Context, w http.ResponseWriter, reason string, status int, msg string, err error) {
	h.metrics.Reject(rejectedIntake, reason)
	h.log.InfoContext(ctx, "Teams activity rejected", "reason", reason, "status", status, "error", err.Error())
	h.writeJSON(ctx, w, status, map[string]string{"error": msg})
}

// fail answers 500 for an activity that failed because of Sidekick
// itself.
func (h *Handler) fail(ctx context.Context, w http.ResponseWriter, logMsg string, err error) {
	h.log.ErrorContext(ctx, logMsg, "error", err.Error())
	h.writeJSON(ctx, w, http.StatusInternalServerError, map[string]string{"error": msgInternal})
}

// writeJSON answers with status and v, a map of strings, as JSON.
func (h *Handler) writeJSON(ctx context.Context, w http.ResponseWriter, status int, v map[string]string) {
	body, err := json.Marshal(v)
	if err != nil {
		h.log.ErrorContext(ctx, "encode the Teams response", "error", err.Error())
		http.Error(w, msgInternal, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(append(body, '\n')); err != nil {
		h.log.DebugContext(ctx, "write the Teams response", "error", err.Error())
	}
}

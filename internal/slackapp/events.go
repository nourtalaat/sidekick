package slackapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// EventsPattern is the route the Events API is served at, for
// http.ServeMux.
const EventsPattern = "POST /slack/events"

// MaxBodyBytes is the largest Events API request accepted: 1 MiB.
const MaxBodyBytes = 1 << 20

// The headers and scheme of Slack's request signatures.
const (
	headerTimestamp = "X-Slack-Request-Timestamp"
	headerSignature = "X-Slack-Signature"
	signatureScheme = "v0"
)

// SignatureTolerance is how far a request's timestamp may be from the time
// it arrives, so a captured request cannot be sent again later.
const SignatureTolerance = 5 * time.Minute

// rejectedIntake is the intake label of Events API requests turned away
// before their event is known. Intake names cannot start with "_".
const rejectedIntake = "_slack"

// The messages of error responses. They never say which check failed.
const (
	msgTooLarge        = "the body is larger than 1 MiB"
	msgUnreadable      = "the body could not be read"
	msgUnauthenticated = "unauthenticated"
	msgInvalidPayload  = "invalid payload"
	msgInternal        = "internal error"
)

// ServeHTTP serves the Events API at EventsPattern. It checks the request's
// signature with the signing secret before anything parses the body,
// answers Slack's URL check with its challenge, and otherwise answers 200
// once Handle has stored the event, so Slack sends again any event that
// could not be stored. It answers 413 for a body over MaxBodyBytes, 401
// when the signature or its timestamp is wrong, 400 for a payload that is
// not an Events API payload, and 500 when the event could not be stored.
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

	if err := h.Verify(ctx, r.Header, body); err != nil {
		if !errors.Is(err, intake.ErrUnauthenticated) {
			h.fail(ctx, w, "Slack request not checked: the signing secret could not be read", err)

			return
		}

		h.reject(ctx, w, ingest.ReasonUnauthenticated, http.StatusUnauthorized, msgUnauthenticated, err)

		return
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err == nil && env.Type == payloadURLVerification {
		h.writeJSON(ctx, w, http.StatusOK, map[string]string{"challenge": env.Challenge})

		return
	}

	switch err := h.Handle(ctx, body); {
	case errors.Is(err, ErrInvalidPayload):
		h.reject(ctx, w, ingest.ReasonInvalidPayload, http.StatusBadRequest, msgInvalidPayload, err)
	case err != nil:
		h.fail(ctx, w, "Slack event not stored; Slack sends it again", err)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// Verify checks Slack's signature on a request: X-Slack-Signature is "v0="
// and the hex HMAC-SHA256, keyed with the signing secret, of "v0:", the
// X-Slack-Request-Timestamp value, ":" and the body. The timestamp must be
// within SignatureTolerance of now. Every Slack HTTP route checks the raw
// body with it before parsing anything. An error that wraps
// intake.ErrUnauthenticated means the request must be refused with 401;
// any other error means the secret could not be read.
func (h *Handler) Verify(ctx context.Context, header http.Header, body []byte) error {
	secret, err := secrets.ResolveText(ctx, h.secrets, h.signingSecret)
	if err != nil {
		return fmt.Errorf("resolve the signing secret: %w", err)
	}

	if secret == "" {
		return fmt.Errorf("the signing secret is empty: %w", intake.ErrUnauthenticated)
	}

	ts := header.Get(headerTimestamp)

	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("parse timestamp: %w", intake.ErrUnauthenticated)
	}

	if age := h.now().Sub(time.Unix(sec, 0)); age > SignatureTolerance || age < -SignatureTolerance {
		return fmt.Errorf("timestamp outside tolerance: %w", intake.ErrUnauthenticated)
	}

	sig, ok := strings.CutPrefix(header.Get(headerSignature), signatureScheme+"=")
	if !ok {
		return fmt.Errorf("missing signature: %w", intake.ErrUnauthenticated)
	}

	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", intake.ErrUnauthenticated)
	}

	if !hmac.Equal(got, sign([]byte(secret), ts, body)) {
		return fmt.Errorf("wrong signature: %w", intake.ErrUnauthenticated)
	}

	return nil
}

// reject answers a request turned away for reason and counts it. err says
// why in the log; it never holds payload values.
func (h *Handler) reject(ctx context.Context, w http.ResponseWriter, reason string, status int, msg string, err error) {
	h.metrics.Reject(rejectedIntake, reason)
	h.log.InfoContext(ctx, "Slack request rejected", "reason", reason, "error", err.Error())
	h.writeJSON(ctx, w, status, map[string]string{"error": msg})
}

// fail answers 500 for a request that failed because of Sidekick itself.
func (h *Handler) fail(ctx context.Context, w http.ResponseWriter, logMsg string, err error) {
	h.log.ErrorContext(ctx, logMsg, "error", err.Error())
	h.writeJSON(ctx, w, http.StatusInternalServerError, map[string]string{"error": msgInternal})
}

// writeJSON answers with status and v, a map of strings, as JSON.
func (h *Handler) writeJSON(ctx context.Context, w http.ResponseWriter, status int, v map[string]string) {
	body, err := json.Marshal(v)
	if err != nil {
		h.log.ErrorContext(ctx, "encode the Slack response", "error", err.Error())
		http.Error(w, msgInternal, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(append(body, '\n')); err != nil {
		h.log.DebugContext(ctx, "write the Slack response", "error", err.Error())
	}
}

// sign computes Slack's raw signature of body sent at timestamp.
func sign(secret []byte, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signatureScheme + ":" + timestamp + ":"))
	mac.Write(body)

	return mac.Sum(nil)
}

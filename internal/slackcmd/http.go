package slackcmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/slackapp"
)

// CommandsPattern is the route slash commands are served at in Events API
// mode, for http.ServeMux.
const CommandsPattern = "POST /slack/commands"

// maxCommandBytes is the largest command request accepted. Slack's are
// well under 10 KiB.
const maxCommandBytes = 64 << 10

// The messages of error responses. They never say which check failed.
const (
	msgTooLarge        = "the body is too large"
	msgUnauthenticated = "unauthenticated"
	msgInvalid         = "invalid request"
	msgServerError     = "internal error"
)

// Verifier checks Slack's signature on a request. *slackapp.Handler is
// one.
type Verifier interface {
	Verify(ctx context.Context, header http.Header, body []byte) error
}

// HTTPHandler serves slash commands at CommandsPattern.
type HTTPHandler struct {
	commands *Handler
	verifier Verifier
	log      *slog.Logger
}

// NewHTTPHandler returns an http.Handler for commands sent by Slack in
// Events API mode, checked with verifier.
func NewHTTPHandler(commands *Handler, verifier Verifier) *HTTPHandler {
	return &HTTPHandler{commands: commands, verifier: verifier, log: commands.log}
}

// ServeHTTP checks the request's signature before it reads the form, then
// answers with the command's reply. It answers 413 for a body that is too
// large, 401 when the signature or its timestamp is wrong, 400 for a body
// that is not a form, and 500 when the signing secret cannot be read.
func (s *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommandBytes))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			s.writeError(ctx, w, http.StatusRequestEntityTooLarge, msgTooLarge)

			return
		}

		s.writeError(ctx, w, http.StatusBadRequest, msgInvalid)

		return
	}

	if err := s.verifier.Verify(ctx, r.Header, body); err != nil {
		if errors.Is(err, intake.ErrUnauthenticated) {
			s.log.InfoContext(ctx, "Slack command request rejected", "error", err.Error())
			s.writeError(ctx, w, http.StatusUnauthorized, msgUnauthenticated)

			return
		}

		s.log.ErrorContext(ctx, "Slack command request not checked: the signing secret could not be read", "error", err.Error())
		s.writeError(ctx, w, http.StatusInternalServerError, msgServerError)

		return
	}

	form, err := url.ParseQuery(string(body))
	if err != nil {
		s.writeError(ctx, w, http.StatusBadRequest, msgInvalid)

		return
	}

	reply, err := s.commands.HandleCommand(ctx, commandOf(form))
	if err != nil {
		s.log.ErrorContext(ctx, "Slack command not answered", "error", err.Error())
		s.writeError(ctx, w, http.StatusInternalServerError, msgServerError)

		return
	}

	s.write(ctx, w, http.StatusOK, reply)
}

func (s *HTTPHandler) writeError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	body, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		http.Error(w, msgServerError, http.StatusInternalServerError)

		return
	}

	s.write(ctx, w, status, body)
}

func (s *HTTPHandler) write(ctx context.Context, w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(body); err != nil {
		s.log.DebugContext(ctx, "write the Slack command response", "error", err.Error())
	}
}

// commandOf reads a slash command from its form, as Slack posts it.
func commandOf(form url.Values) slack.SlashCommand {
	return slack.SlashCommand{
		TeamID:      form.Get("team_id"),
		TeamDomain:  form.Get("team_domain"),
		ChannelID:   form.Get("channel_id"),
		ChannelName: form.Get("channel_name"),
		UserID:      form.Get("user_id"),
		UserName:    form.Get("user_name"),
		Command:     form.Get("command"),
		Text:        form.Get("text"),
		ResponseURL: form.Get("response_url"),
		TriggerID:   form.Get("trigger_id"),
		APIAppID:    form.Get("api_app_id"),
	}
}

var (
	_ Verifier     = (*slackapp.Handler)(nil)
	_ Investigator = (*slackapp.Handler)(nil)
)

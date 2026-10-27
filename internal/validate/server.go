package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	// ListenAddr is where the sidecar listens: loopback only, so only the
	// containers of its own pod reach it.
	ListenAddr = "127.0.0.1:8200"
	// busyRetryAfter is the Retry-After a busy answer suggests.
	busyRetryAfter = 5 * time.Second
	// MaxTerminationMessage is the most Kubernetes keeps of a container's
	// termination message.
	MaxTerminationMessage = 4096
)

// errorBody is the body of every answer that is not a result.
type errorBody struct {
	Error string `json:"error"`
}

// Handler serves POST /validate, which takes a Request and answers with a
// Result, and GET /healthz.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /validate", s.serveValidate)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		if _, err := io.WriteString(w, "ok\n"); err != nil {
			s.log.Debug("could not write an answer", "error", err)
		}
	})

	return mux
}

// serveValidate decodes a request, validates it and writes the result: 200
// with the result, 400 for a bad request, 503 when busy, 504 on timeout and
// 500 for anything else.
func (s *Service) serveValidate(w http.ResponseWriter, r *http.Request) {
	req, err := DecodeRequest(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, errorBody{Error: RedactTokens(err.Error())})

		return
	}

	start := time.Now()
	res, err := s.Validate(r.Context(), req)
	log := s.log.With("format", req.Format, "files", len(req.Files), "duration", time.Since(start).Round(time.Millisecond))

	if err == nil {
		log.Info("validated", "ok", res.OK, "errors", len(res.Errors), "not_checked", len(res.NotChecked))
		s.writeJSON(w, http.StatusOK, res)

		return
	}

	// Errors can quote tool output, so they are redacted like results.
	message := RedactTokens(err.Error())

	switch {
	case errors.Is(err, ErrBadRequest):
		s.writeJSON(w, http.StatusBadRequest, errorBody{Error: message})
	case errors.Is(err, ErrBusy):
		log.Warn("refused a validation", "error", message)
		w.Header().Set("Retry-After", strconv.Itoa(int(busyRetryAfter.Seconds())))
		s.writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: message})
	case errors.Is(err, ErrTimeout):
		log.Warn("validation timed out", "error", message)
		s.writeJSON(w, http.StatusGatewayTimeout, errorBody{Error: message})
	default:
		log.Error("validation failed", "error", message)
		s.writeJSON(w, http.StatusInternalServerError, errorBody{Error: message})
	}
}

// writeJSON writes v as the JSON body of an answer with the given status.
func (s *Service) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Debug("could not write an answer", "error", err)
	}
}

// DecodeRequest reads one JSON request and refuses unknown fields, so a
// misspelled field is not silently ignored.
func DecodeRequest(r io.Reader) (Request, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var req Request
	if err := dec.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("%w: decode the request: %w", ErrBadRequest, err)
	}

	return req, nil
}

// TerminationMessage returns res as JSON that fits Kubernetes's limit on a
// termination message. It drops the not-checked list, then errors from the
// end, then shortens the last error, and sets Truncated when it cut
// anything.
func TerminationMessage(res Result) ([]byte, error) {
	const quarter = MaxTerminationMessage / 4

	res.Errors = slices.Clone(res.Errors)

	for {
		raw, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("encode the result: %w", err)
		}

		if len(raw) <= MaxTerminationMessage {
			return raw, nil
		}

		res.Truncated = true

		switch {
		case len(res.NotChecked) > 0:
			res.NotChecked = nil
		case len(res.Errors) > 1:
			res.Errors = res.Errors[:len(res.Errors)-1]
		case len(res.Errors) == 1 && (len(res.Errors[0].Message) > quarter || len(res.Errors[0].Path) > quarter):
			res.Errors[0].Message = shorten(res.Errors[0].Message, quarter)
			res.Errors[0].Path = shorten(res.Errors[0].Path, quarter)
		default:
			return nil, fmt.Errorf("the result does not fit in %d bytes", MaxTerminationMessage)
		}
	}
}

// shorten cuts s to at most n bytes, on a rune boundary.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}

// Package validateclient calls the monitor validator, the sidekick-validate
// server that runs in a sidecar of the Sidekick pod (docs/validator.md). It
// has its own copy of the wire types, so the main binary does not link the
// validator's tools; a test checks that they match the server's.
package validateclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// Format names the format of the files in a request.
type Format string

// The formats the validator knows.
const (
	FormatPrometheusRules  Format = "prometheus_rules"
	FormatTerraform        Format = "terraform"
	FormatGrafanaAlertFile Format = "grafana_alert_file"
)

// Limits of the validator that the client keeps to, so a request that the
// validator would refuse is not sent.
const (
	// MaxFiles is the most files one request may hold.
	MaxFiles = 500
	// MaxRequestBytes bounds the size of one request body.
	MaxRequestBytes = 8 << 20
)

// maxAnswerBytes is the most of an answer the client reads.
const maxAnswerBytes = 4 << 20

// maxErrorChars is the most of an error body that is kept in an error.
const maxErrorChars = 500

// Errors that Validate wraps, so callers can tell them apart.
var (
	// ErrBadRequest means the validator refused the request itself.
	ErrBadRequest = errors.New("the validator refused the request")
	// ErrBusy means the validator was busy for as long as it waits.
	ErrBusy = errors.New("the validator is busy")
	// ErrTimeout means the validation took longer than the validator allows.
	ErrTimeout = errors.New("the validation timed out")
	// ErrUnavailable means the validator could not be reached, or answered
	// with something the client cannot use.
	ErrUnavailable = errors.New("the validator is not available")
	// ErrTooLarge means the request is larger than the validator takes.
	ErrTooLarge = errors.New("the files are too large for the validator")

	errAnswerTooLarge = errors.New("the validator answered with more than the client reads")
)

// File is one file to check. Path is relative and uses forward slashes.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Request asks for one validation. A terraform request holds one module
// directory: Dir names it within Files, and is "." when Files holds only
// the module itself. Other formats check each file on its own.
type Request struct {
	Format Format `json:"format"`
	Dir    string `json:"dir,omitempty"`
	Files  []File `json:"files"`
}

// Problem is one error in a file. Line is 0 when the tool did not say.
type Problem struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Result is the answer to a request. OK is true when no errors were found.
// NotChecked lists parts the validator could not check; they do not make OK
// false. Truncated is set when the result was cut down to fit a size limit.
type Result struct {
	OK         bool      `json:"ok"`
	Errors     []Problem `json:"errors"`
	NotChecked []Problem `json:"notChecked,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
}

// Client calls one validator. It is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the validator at baseURL, such as
// http://127.0.0.1:8200. A nil hc means httpclient.New().
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = httpclient.New()
	}

	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

// Validate sends the request and returns the validator's result. A result
// that lists errors is not an error here: the files failed, and OK is false.
// The error wraps ErrBadRequest, ErrBusy, ErrTimeout, ErrUnavailable or
// ErrTooLarge.
func (c *Client) Validate(ctx context.Context, req Request) (Result, error) {
	if len(req.Files) > MaxFiles {
		return Result{}, fmt.Errorf("%d files, the validator takes at most %d: %w", len(req.Files), MaxFiles, ErrTooLarge)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, fmt.Errorf("encode the request: %w", err)
	}

	if len(body) > MaxRequestBytes {
		return Result{}, fmt.Errorf("%d bytes, the validator takes at most %d: %w", len(body), MaxRequestBytes, ErrTooLarge)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/validate", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("make the request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("call the validator: %w", ctx.Err())
		}

		return Result{}, fmt.Errorf("call the validator: %w: %w", ErrUnavailable, err)
	}

	defer func() { _ = resp.Body.Close() }()

	return c.result(resp)
}

// result reads the answer to a validation request.
func (*Client) result(resp *http.Response) (Result, error) {
	answer, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxAnswerBytes, errAnswerTooLarge))
	if err != nil {
		return Result{}, fmt.Errorf("read the answer: %w: %w", ErrUnavailable, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var res Result
		if err := json.Unmarshal(answer, &res); err != nil {
			return Result{}, fmt.Errorf("decode the result: %w: %w", ErrUnavailable, err)
		}

		if res.Errors == nil {
			res.Errors = []Problem{}
		}

		return res, nil
	case http.StatusBadRequest:
		return Result{}, fmt.Errorf("%w: %s", ErrBadRequest, errorText(answer))
	case http.StatusServiceUnavailable:
		return Result{}, fmt.Errorf("%w: %s", ErrBusy, errorText(answer))
	case http.StatusGatewayTimeout:
		return Result{}, fmt.Errorf("%w: %s", ErrTimeout, errorText(answer))
	default:
		return Result{}, fmt.Errorf("%w: status %d: %s", ErrUnavailable, resp.StatusCode, errorText(answer))
	}
}

// errorText returns the message of an error body, or the start of the body
// when it is not the validator's JSON.
func errorText(answer []byte) string {
	var body struct {
		Error string `json:"error"`
	}

	text := string(answer)
	if json.Unmarshal(answer, &body) == nil && body.Error != "" {
		text = body.Error
	}

	runes := []rune(strings.TrimSpace(text))
	if len(runes) > maxErrorChars {
		return string(runes[:maxErrorChars-1]) + "…"
	}

	return string(runes)
}

package validate

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// The kinds of failure that job mode reports in JobError.Kind, so the
// caller can tell a bad request from a timeout without reading the text.
const (
	// KindBadRequest is a request that is wrong.
	KindBadRequest = "bad_request"
	// KindTimeout is a validation that took longer than Timeout.
	KindTimeout = "timeout"
	// KindBusy is a validator that had no free slot for SlotWait.
	KindBusy = "busy"
	// KindInternal is any other reason the validation could not run.
	KindInternal = "internal"
)

// gzipMagic starts every gzip stream.
var gzipMagic = []byte{0x1f, 0x8b}

// JobError is what job mode prints and writes when the validation could not
// run. Kind is one of the Kind constants.
type JobError struct {
	Error string `json:"error"`
	Kind  string `json:"kind,omitempty"`
}

// NewJobError describes err for job mode. The text can quote tool output,
// so tokens in it are redacted.
func NewJobError(err error) JobError {
	return JobError{Error: RedactTokens(err.Error()), Kind: ErrorKind(err)}
}

// ErrorKind returns the Kind constant that says why err stopped a
// validation.
func ErrorKind(err error) string {
	switch {
	case errors.Is(err, ErrBadRequest):
		return KindBadRequest
	case errors.Is(err, ErrTimeout):
		return KindTimeout
	case errors.Is(err, ErrBusy):
		return KindBusy
	default:
		return KindInternal
	}
}

// RequestReader returns a reader of the JSON request in r, which is plain
// JSON or gzipped JSON. The Kubernetes ConfigMap that carries a request
// holds at most 1 MiB, so Sidekick gzips it. At most MaxRequestBytes of the
// JSON are read, so a small gzip file cannot expand without limit.
func RequestReader(r io.Reader) (io.Reader, error) {
	buffered := bufio.NewReader(r)

	head, err := buffered.Peek(len(gzipMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read the request: %w", err)
	}

	if string(head) != string(gzipMagic) {
		return io.LimitReader(buffered, MaxRequestBytes), nil
	}

	zr, err := gzip.NewReader(buffered)
	if err != nil {
		return nil, fmt.Errorf("%w: read the gzipped request: %w", ErrBadRequest, err)
	}

	return io.LimitReader(zr, MaxRequestBytes), nil
}

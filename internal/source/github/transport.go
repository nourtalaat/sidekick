package github

import (
	"fmt"
	"io"
	"net/http"
)

// maxResponseSize bounds one response body. The largest answers are
// commits with many patches and files of up to a megabyte, which the
// contents API sends in base64; results are cut much smaller later.
const maxResponseSize = 16 << 20

// errResponseTooLarge is returned when a response body is larger than
// maxResponseSize.
var errResponseTooLarge = fmt.Errorf("GitHub sent a response larger than %d bytes", maxResponseSize)

// limitTransport fails the read of any response body larger than
// maxResponseSize, so a huge answer cannot make Sidekick read without end.
type limitTransport struct {
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	resp.Body = &limitedBody{ReadCloser: resp.Body, left: maxResponseSize}

	return resp, nil
}

// limitedBody fails a read once more than its limit has been read.
type limitedBody struct {
	io.ReadCloser

	left int64
}

// Read implements io.Reader.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errResponseTooLarge
	}

	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}

	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)

	if b.left < 0 {
		return n, errResponseTooLarge
	}

	return n, err //nolint:wrapcheck // io.Reader callers compare err with io.EOF, so it must stay unwrapped.
}

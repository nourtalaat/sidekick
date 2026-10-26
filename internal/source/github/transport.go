package github

import (
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/httpclient"
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

	resp.Body = httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	return resp, nil
}

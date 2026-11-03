package awsauth

import (
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// SessionName is the session name of every role Sidekick assumes for a
// plugin, which CloudTrail shows as the caller.
const SessionName = "sidekick"

// MaxResponseSize bounds one response body a plugin reads from AWS. The
// largest answers are Logs Insights results; results are cut much smaller
// later.
const MaxResponseSize = 16 << 20

// errResponseTooLarge is returned when a response body is larger than
// MaxResponseSize.
var errResponseTooLarge = fmt.Errorf("the response from AWS is larger than %d bytes", MaxResponseSize)

// HTTPClient returns the HTTP client for the AWS SDK of a plugin: base, or
// the client httpclient.New makes when base is nil, with request bodies the
// transport cannot take a failed write from, and a size limit on every
// response body, so a huge answer cannot make Sidekick read without end.
func HTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = httpclient.New()
	}

	base = httpclient.WithPlainBody(base)

	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	limited := *base
	limited.Transport = limitTransport{next: next}

	return &limited
}

// limitTransport fails the read of any response body larger than
// MaxResponseSize.
type limitTransport struct {
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	resp.Body = httpclient.LimitBody(resp.Body, MaxResponseSize, errResponseTooLarge)

	return resp, nil
}

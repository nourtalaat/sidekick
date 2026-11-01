package httpclient

import (
	"fmt"
	"io"
	"net/http"
)

// WithPlainBody returns a copy of c that sends every request body as a
// plainBody, so the HTTP transport cannot take a failed write from it. Use
// it for the client of the AWS SDK, which is the Bedrock connector and the
// AWS source.
//
// The AWS SDK sends a request body that also has WriteTo, and WriteTo of
// that body returns io.EOF once the SDK has closed it. The HTTP transport
// reads the body one last time after it has sent it, with WriteTo, and
// treats that io.EOF as a failed write: it closes the connection, even when
// the response is half read. A plain body has only Read and Close. Read of
// a closed body returns io.EOF too, which the transport takes as the end of
// the body. The same holds for a body from GetBody, which the transport
// asks for when it sends a request again.
//
// A client with no transport uses http.DefaultTransport.
func WithPlainBody(c *http.Client) *http.Client {
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	out := *c
	out.Transport = plainBodyTransport{next: next}

	return &out
}

// plainBodyTransport sends requests with a plain body.
type plainBodyTransport struct {
	next http.RoundTripper
}

// plainBody is a request body that shows only Read and Close.
type plainBody struct {
	body io.ReadCloser
}

// RoundTrip implements http.RoundTripper.
func (t plainBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(withPlainBody(req))
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

// Read implements io.Reader.
func (b plainBody) Read(p []byte) (int, error) {
	return b.body.Read(p) //nolint:wrapcheck // io.Reader callers compare err with io.EOF, so it must stay unwrapped.
}

// Close implements io.Closer.
func (b plainBody) Close() error {
	if err := b.body.Close(); err != nil {
		return fmt.Errorf("close the request body: %w", err)
	}

	return nil
}

// withPlainBody returns req with a plain body, as a copy, because a
// RoundTripper must not change the request it is given. Its GetBody gives a
// plain body too. A request without a body is returned as it is.
func withPlainBody(req *http.Request) *http.Request {
	if (req.Body == nil || req.Body == http.NoBody) && req.GetBody == nil {
		return req
	}

	out := req.WithContext(req.Context())
	out.Body = plainOf(req.Body)

	if getBody := req.GetBody; getBody != nil {
		out.GetBody = func() (io.ReadCloser, error) {
			body, err := getBody()
			if err != nil {
				return nil, fmt.Errorf("get the request body again: %w", err)
			}

			return plainOf(body), nil
		}
	}

	return out
}

// plainOf returns body as a plainBody. A missing or empty body is returned
// as it is, because the transport treats http.NoBody in a special way.
func plainOf(body io.ReadCloser) io.ReadCloser {
	if body == nil || body == http.NoBody {
		return body
	}

	return plainBody{body: body}
}

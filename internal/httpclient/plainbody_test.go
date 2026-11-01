package httpclient

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// largeAnswerSize is the size of the answer the fake server sends, large
// enough that it is still being read when a connection is closed under it.
const largeAnswerSize = 512 << 10

// errGetBody is the error a failing GetBody returns.
var errGetBody = errors.New("no body")

func TestWithPlainBodySurvivesAClosedRequestBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// closeOnEnd closes the body as soon as it has been read to its
		// end, as the AWS SDK does once the request is sent.
		closeOnEnd bool
	}{
		{name: "a body that stays open", closeOnEnd: false},
		{name: "a body that is closed once it is sent", closeOnEnd: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := fakehttp.New(t)
			srv.Handle(http.MethodPost, "/", fakehttp.Response{Body: []byte(strings.Repeat("x", largeAnswerSize))})

			payload := []byte(`{"queryId":"q-big"}`)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL(), http.NoBody)
			require.NoError(t, err)

			req.Body = &sdkBody{reader: bytes.NewReader(payload), closeOnEnd: tt.closeOnEnd}
			req.ContentLength = int64(len(payload))

			resp, err := WithPlainBody(srv.Client()).Do(req)
			require.NoError(t, err)

			defer func() { require.NoError(t, resp.Body.Close()) }()

			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Len(t, got, largeAnswerSize)
			require.Equal(t, payload, srv.Requests()[0].Body)
		})
	}
}

func TestWithPlainBodyRequest(t *testing.T) {
	t.Parallel()

	newSDKBody := func() io.ReadCloser { return &sdkBody{reader: bytes.NewReader([]byte("abc"))} }

	tests := []struct {
		name    string
		body    io.ReadCloser
		getBody func() (io.ReadCloser, error)
		// wantSame says the request is returned as it is.
		wantSame bool
	}{
		{name: "a request without a body is not changed", wantSame: true},
		{name: "a request with an empty body is not changed", body: http.NoBody, wantSame: true},
		{name: "a body shows only Read and Close", body: newSDKBody()},
		{
			name:    "a body from GetBody shows only Read and Close",
			body:    newSDKBody(),
			getBody: func() (io.ReadCloser, error) { return newSDKBody(), nil },
		},
		{
			name:    "an empty body from GetBody is not changed",
			body:    http.NoBody,
			getBody: func() (io.ReadCloser, error) { return http.NoBody, nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1/", nil)
			require.NoError(t, err)

			req.Body = tt.body
			req.GetBody = tt.getBody

			got := withPlainBody(req)

			if tt.wantSame {
				require.Same(t, req, got)

				return
			}

			require.NotSame(t, req, got)
			require.Equal(t, tt.body, req.Body, "the request it was given is not changed")

			if tt.getBody != nil && tt.body != http.NoBody {
				orig, err := req.GetBody()
				require.NoError(t, err)
				require.IsType(t, &sdkBody{}, orig, "GetBody of the request it was given is not changed")
			}

			if tt.body != http.NoBody {
				requirePlain(t, got.Body)
				requireBodyRead(t, got.Body)
			}

			if tt.getBody == nil {
				require.Nil(t, got.GetBody)

				return
			}

			again, err := got.GetBody()
			require.NoError(t, err)

			if tt.body == http.NoBody {
				require.Equal(t, http.NoBody, again)

				return
			}

			requirePlain(t, again)
			requireBodyRead(t, again)
		})
	}
}

func TestWithPlainBodyGetBodyError(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1/", strings.NewReader("abc"))
	require.NoError(t, err)

	req.GetBody = func() (io.ReadCloser, error) { return nil, errGetBody }

	_, err = withPlainBody(req).GetBody()
	require.ErrorIs(t, err, errGetBody)
}

func TestWithPlainBodyKeepsTheClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport http.RoundTripper
	}{
		{name: "a client with a transport", transport: &http.Transport{}},
		{name: "a client without a transport"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orig := &http.Client{Transport: tt.transport, Timeout: time.Minute}

			got := WithPlainBody(orig)

			require.NotSame(t, orig, got)
			require.Equal(t, time.Minute, got.Timeout)
			require.Equal(t, tt.transport, orig.Transport, "the client it was given is not changed")

			wrapped, ok := got.Transport.(plainBodyTransport)
			require.True(t, ok)

			if tt.transport == nil {
				require.Equal(t, http.DefaultTransport, wrapped.next)
			} else {
				require.Equal(t, tt.transport, wrapped.next)
			}
		})
	}
}

// requirePlain fails the test when body has more than Read and Close.
func requirePlain(t *testing.T, body io.ReadCloser) {
	t.Helper()

	_, hasWriteTo := body.(io.WriterTo)
	require.False(t, hasWriteTo)
}

// requireBodyRead reads body to its end and closes it, and checks that it
// held "abc".
func requireBodyRead(t *testing.T, body io.ReadCloser) {
	t.Helper()

	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "abc", string(data))
	require.NoError(t, body.Close())
}

// sdkBody is a request body like the one the AWS SDK sends. It has WriteTo,
// and once it is closed, Read and WriteTo return io.EOF.
type sdkBody struct {
	reader     *bytes.Reader
	closeOnEnd bool

	mu     sync.Mutex
	closed bool
}

// Read implements io.Reader.
func (b *sdkBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return 0, io.EOF
	}

	n, err := b.reader.Read(p)
	if b.closeOnEnd && b.reader.Len() == 0 {
		b.closed = true
	}

	return n, err //nolint:wrapcheck // the test body acts as an io.Reader.
}

// WriteTo implements io.WriterTo.
func (b *sdkBody) WriteTo(w io.Writer) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return 0, io.EOF
	}

	return b.reader.WriteTo(w) //nolint:wrapcheck // the test body acts as an io.WriterTo.
}

// Close implements io.Closer.
func (b *sdkBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true

	return nil
}

package aws

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// largeAnswerSize is the size of the answer the fake server sends, large
// enough that it is still being read when a connection is closed under it.
const largeAnswerSize = 512 << 10

func TestLimitTransportSurvivesAClosedRequestBody(t *testing.T) {
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

			s, apis := newTestSource(t, nil)
			apis.logs.Handle(http.MethodPost, "/", fakehttp.Response{Body: []byte(strings.Repeat("x", largeAnswerSize))})

			payload := []byte(`{"queryId":"q-big"}`)
			body := &sdkBody{reader: bytes.NewReader(payload), closeOnEnd: tt.closeOnEnd}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, apis.logs.URL(), http.NoBody)
			require.NoError(t, err)

			req.Body = body
			req.ContentLength = int64(len(payload))

			resp, err := s.base.HTTPClient.Do(req)
			require.NoError(t, err)

			defer func() { require.NoError(t, resp.Body.Close()) }()

			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Len(t, got, largeAnswerSize)
			require.Equal(t, payload, apis.logs.Requests()[0].Body)
		})
	}
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

package bedrock

import (
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// TestConnectorSendsAPlainRequestBody checks what reaches the HTTP
// transport. The AWS SDK sends a body with WriteTo, and the transport takes
// the io.EOF that WriteTo returns once the SDK has closed the body as a
// failed write, so it closes the connection under a half read answer. The
// transport must see a body with only Read and Close.
func TestConnectorSendsAPlainRequestBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modelID string
		edit    func(*Config)
		serve   func(t *testing.T) *fakehttp.Server
		call    func(t *testing.T, c *Connector)
	}{
		{
			name:    "a streamed turn",
			modelID: opus55Geo,
			serve: func(t *testing.T) *fakehttp.Server {
				t.Helper()

				return serve(t, opus55Geo, recorded{op: opStream, status: http.StatusOK, file: "text.jsonl"})
			},
			call: func(t *testing.T, c *Connector) {
				t.Helper()

				_, err := c.Generate(t.Context(), userText("Why did checkout fail?"))
				require.NoError(t, err)
			},
		},
		{
			name:    "an embedding call",
			modelID: titanV2,
			edit:    embedEdit,
			serve: func(t *testing.T) *fakehttp.Server {
				t.Helper()

				return serveTitan(t, map[string]titanAnswer{
					"first alert":  {status: http.StatusOK, file: "embed_titan_a.json"},
					"second alert": {status: http.StatusOK, file: "embed_titan_b.json"},
				})
			},
			call: func(t *testing.T, c *Connector) {
				t.Helper()

				_, err := c.Embed(t.Context(), embedTexts)
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := tt.serve(t)
			seen := &bodyRecorder{next: api.Client().Transport}

			c := newTestConnectorWith(t, api, &http.Client{Transport: seen}, tt.modelID, tt.edit)

			tt.call(t, c)

			bodies := seen.bodies()
			require.NotEmpty(t, bodies, "the test must send at least one request with a body")

			for _, body := range bodies {
				_, hasWriteTo := body.(io.WriterTo)
				require.False(t, hasWriteTo, "the transport got a request body with WriteTo: %T", body)
			}
		})
	}
}

// bodyRecorder is a transport that records the request bodies it is given.
type bodyRecorder struct {
	next http.RoundTripper

	mu   sync.Mutex
	seen []io.ReadCloser
}

// RoundTrip implements http.RoundTripper.
func (r *bodyRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody {
		r.mu.Lock()
		r.seen = append(r.seen, req.Body)
		r.mu.Unlock()
	}

	return r.next.RoundTrip(req) //nolint:wrapcheck // the test transport passes the result on as it is.
}

// bodies returns the bodies recorded so far.
func (r *bodyRecorder) bodies() []io.ReadCloser {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]io.ReadCloser(nil), r.seen...)
}

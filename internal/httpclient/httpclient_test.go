package httpclient

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	// testHeaderTimeout is the response header timeout of the test client.
	testHeaderTimeout = 100 * time.Millisecond
	// failWithin is how soon a call to a silent server must fail.
	failWithin = 5 * time.Second
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(t *testing.T, client *http.Client, transport *http.Transport)
	}{
		{
			name: "no limit on a whole call",
			check: func(t *testing.T, client *http.Client, _ *http.Transport) {
				t.Helper()
				require.Zero(t, client.Timeout)
			},
		},
		{
			name: "deadlines before the response starts",
			check: func(t *testing.T, _ *http.Client, transport *http.Transport) {
				t.Helper()
				require.Equal(t, TLSHandshakeTimeout, transport.TLSHandshakeTimeout)
				require.Equal(t, ResponseHeaderTimeout, transport.ResponseHeaderTimeout)
				require.NotNil(t, transport.DialContext)
			},
		},
		{
			name: "idle connection limits and proxies",
			check: func(t *testing.T, _ *http.Client, transport *http.Transport) {
				t.Helper()
				require.Equal(t, maxIdleConns, transport.MaxIdleConns)
				require.Equal(t, maxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
				require.Equal(t, idleConnTimeout, transport.IdleConnTimeout)
				require.NotNil(t, transport.Proxy)
			},
		},
		{
			name: "a client of its own",
			check: func(t *testing.T, client *http.Client, transport *http.Transport) {
				t.Helper()
				require.NotSame(t, http.DefaultClient, client)
				require.NotSame(t, http.DefaultTransport, transport)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := New()

			transport, ok := client.Transport.(*http.Transport)
			require.True(t, ok)

			tt.check(t, client, transport)
		})
	}
}

func TestResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// silent makes the server never send its response headers.
		silent      bool
		wantTimeout bool
	}{
		{name: "a server that answers", silent: false},
		{name: "a server that never sends headers", silent: true, wantTimeout: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.silent {
					<-release
				}

				w.WriteHeader(http.StatusNoContent)
			}))
			// Cleanups run last in first out: release the handler, then close.
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(release) })

			client := newClient(timeouts{dial: DialTimeout, tlsHandshake: TLSHandshakeTimeout, responseHeader: testHeaderTimeout})

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
			require.NoError(t, err)

			start := time.Now()
			resp, err := client.Do(req)

			if !tt.wantTimeout {
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, http.StatusNoContent, resp.StatusCode)

				return
			}

			require.Error(t, err)
			require.Less(t, time.Since(start), failWithin)

			var netErr net.Error
			require.True(t, errors.As(err, &netErr) && netErr.Timeout(), "want a timeout, got %v", err)
		})
	}
}

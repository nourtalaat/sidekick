package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// watchCloses makes client report, on stderr, the stack of the close of
// any connection that is read after it was closed. Temporary, for SK-92.
func watchCloses(client *http.Client) {
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		return
	}

	dial := tr.DialContext

	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		return &watchedConn{Conn: conn}, nil
	}

	client.Transport = bodyWatcher{next: tr}
}

// bodyWatcher reports errors from reading and closing request bodies.
type bodyWatcher struct {
	next http.RoundTripper
}

func (w bodyWatcher) RoundTrip(req *http.Request) (*http.Response, error) {
	var first atomic.Bool

	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { first.Store(true) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				fmt.Fprintf(os.Stderr, "SK-92: wrote request with error (response started: %v): %v\n", first.Load(), info.Err)
			}
		},
	}

	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	return w.next.RoundTrip(req) //nolint:wrapcheck // temporary diagnostic.
}

// CloseIdleConnections lets the test servers clean up the transport.
func (w bodyWatcher) CloseIdleConnections() {
	if c, ok := w.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

type watchedConn struct {
	net.Conn

	mu    sync.Mutex
	stack []byte
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)

	if errors.Is(err, net.ErrClosed) {
		c.mu.Lock()
		stack := c.stack
		c.mu.Unlock()

		fmt.Fprintf(os.Stderr, "SK-92: read of %s after its close; it was closed by\n%s\nread by\n%s\n", c.LocalAddr(), stack, debug.Stack())
	}

	return n, err
}

func (c *watchedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SK-92: write of %d bytes on %s failed after %d: %v\n", len(p), c.LocalAddr(), n, err)
	}

	return n, err
}

func (c *watchedConn) Close() error {
	c.mu.Lock()
	if c.stack == nil {
		c.stack = debug.Stack()
	}
	c.mu.Unlock()

	return c.Conn.Close()
}

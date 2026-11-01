package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
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
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &watchedBody{ReadCloser: req.Body, length: req.ContentLength}
	}

	resp, err := w.next.RoundTrip(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SK-92: round trip error: %v\n", err)
	}

	return resp, err
}

type watchedBody struct {
	io.ReadCloser

	length int64
	read   int64
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)

	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(os.Stderr, "SK-92: request body read error after %d of %d: %v\n", b.read, b.length, err)
	}

	return n, err
}

func (b *watchedBody) Close() error {
	err := b.ReadCloser.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "SK-92: request body close error: %v\n", err)
	}

	return err
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

package aws

import (
	"context"
	"errors"
	"fmt"
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

func (c *watchedConn) Close() error {
	c.mu.Lock()
	if c.stack == nil {
		c.stack = debug.Stack()
	}
	c.mu.Unlock()

	return c.Conn.Close()
}

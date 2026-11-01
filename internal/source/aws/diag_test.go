package aws

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"sync/atomic"
)

// watchCloses makes client report, on stderr, every close of a connection
// that happens while a read on it is in progress. Temporary, for SK-92.
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

	reading atomic.Int32
}

func (c *watchedConn) Read(p []byte) (int, error) {
	c.reading.Add(1)
	defer c.reading.Add(-1)

	return c.Conn.Read(p)
}

func (c *watchedConn) Close() error {
	if c.reading.Load() > 0 {
		fmt.Fprintf(os.Stderr, "SK-92: close of %s during a read\n%s\n", c.LocalAddr(), debug.Stack())
	}

	return c.Conn.Close()
}

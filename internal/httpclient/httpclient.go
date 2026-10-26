// Package httpclient makes the HTTP client that plugins get in
// registry.Deps. Every step before a response starts has a deadline, so a
// server that accepts a connection and then stays silent cannot hold a call
// forever.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

// Timeouts of the client New returns. There is no limit on a whole call,
// because model responses stream for minutes; callers bound each call with
// its context.
const (
	// DialTimeout bounds opening a TCP connection.
	DialTimeout = 10 * time.Second
	// TLSHandshakeTimeout bounds the TLS handshake.
	TLSHandshakeTimeout = 10 * time.Second
	// ResponseHeaderTimeout bounds the wait for the response headers
	// after the request is sent.
	ResponseHeaderTimeout = 60 * time.Second
)

// Connection reuse settings of the client New returns.
const (
	keepAlive           = 30 * time.Second
	idleConnTimeout     = 90 * time.Second
	maxIdleConns        = 100
	maxIdleConnsPerHost = 10
)

// timeouts are the deadlines newClient sets. Tests shorten them.
type timeouts struct {
	dial           time.Duration
	tlsHandshake   time.Duration
	responseHeader time.Duration
}

// New returns an HTTP client for plugins: the standard transport, with
// proxies from the environment, plus DialTimeout, TLSHandshakeTimeout,
// ResponseHeaderTimeout and limits on idle connections.
func New() *http.Client {
	return newClient(timeouts{dial: DialTimeout, tlsHandshake: TLSHandshakeTimeout, responseHeader: ResponseHeaderTimeout})
}

func newClient(t timeouts) *http.Client {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}

	transport.DialContext = (&net.Dialer{Timeout: t.dial, KeepAlive: keepAlive}).DialContext
	transport.TLSHandshakeTimeout = t.tlsHandshake
	transport.ResponseHeaderTimeout = t.responseHeader
	transport.IdleConnTimeout = idleConnTimeout
	transport.MaxIdleConns = maxIdleConns
	transport.MaxIdleConnsPerHost = maxIdleConnsPerHost

	return &http.Client{Transport: transport}
}

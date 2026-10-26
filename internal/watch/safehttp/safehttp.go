// Package safehttp is the HTTP client of watcher HTTP checks. A model can
// ask for these checks, so the client only does what a health probe needs
// and can reach only what an admin allowed:
//
//   - GET only, http and https only, TLS verification on, no cookies, and
//     no proxy from the environment.
//   - The host must match watch.http.allowHosts: exact names or IP
//     addresses, or "*." and a suffix for every name below it.
//   - Private and special address ranges are blocked unless
//     watch.http.allowPrivate lists them: loopback, private, carrier-grade
//     NAT, link-local (with the cloud metadata address 169.254.169.254),
//     unique local, multicast, unspecified and the other special-purpose
//     ranges. IPv4-mapped and NAT64 IPv6 addresses count as the IPv4
//     address they carry.
//   - The host is resolved before the request and every address is
//     checked. The dialer resolves it again and checks the address it
//     actually connects to in its Control hook, so a DNS answer that
//     changes in between cannot reach a blocked address.
//   - Redirects are followed only to the same host, at most MaxRedirects.
//     Any other redirect is not followed and is the response.
//   - Bodies are read up to MaxBody bytes.
package safehttp

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// Limits of a request.
const (
	// MaxBody is how much of a response body the client reads, in bytes.
	MaxBody = 64 << 10
	// MaxRedirects is how many redirects to the same host a request
	// follows.
	MaxRedirects = 3
)

// Connection reuse settings.
const (
	keepAlive           = 30 * time.Second
	idleConnTimeout     = 90 * time.Second
	maxIdleConns        = 100
	maxIdleConnsPerHost = 2
)

// Reason says why a request was blocked. It is the reason label of
// MetricBlocked.
type Reason string

// Reasons.
const (
	// ReasonHost: the host is not in allowHosts, or the URL is not one the
	// client takes.
	ReasonHost Reason = "host"
	// ReasonPrivateIP: the host resolves to a blocked address.
	ReasonPrivateIP Reason = "private_ip"
	// ReasonRedirect: a redirect was not followed.
	ReasonRedirect Reason = "redirect"
)

// BlockedError is returned, wrapped, for a request the rules refuse.
type BlockedError struct {
	Reason Reason
	// Message says what was refused. It names hosts and addresses, never
	// header values.
	Message string
}

// Error returns the message.
func (e *BlockedError) Error() string {
	return e.Message
}

// Options configure a Client.
type Options struct {
	// AllowHosts are the hosts requests may go to, from
	// watch.http.allowHosts. Empty allows none.
	AllowHosts []string
	// AllowPrivate are the private or special address ranges requests may
	// reach anyway, from watch.http.allowPrivate.
	AllowPrivate []string
	// Metrics counts blocked requests. Nil counts nothing.
	Metrics *Metrics
}

// Response is the answer to a Get.
type Response struct {
	Status int
	// Body is the response body, up to MaxBody bytes. It is the target's
	// own text: untrusted, and not redacted.
	Body []byte
	// Truncated is set when the body was longer than MaxBody.
	Truncated bool
	// NotFollowed says why a redirect was not followed, in which case the
	// redirect is the response. It is empty otherwise.
	NotFollowed string
}

// resolver looks up the addresses of a host. *net.Resolver is one.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Client makes the requests of HTTP checks. Build one at start-up and share
// it: it is safe for concurrent use.
type Client struct {
	hosts     hostList
	addresses addressPolicy
	metrics   *Metrics
	resolver  resolver
	dialer    *net.Dialer
	transport *http.Transport
}

// Check reports whether the client would send a request to rawURL, without
// resolving or sending anything: a URL that CheckURL accepts, whose host
// allowHosts allows. A refused URL returns a BlockedError.
func (c *Client) Check(rawURL string) error {
	_, err := c.checkURL(rawURL)

	return err
}

// AllowsHost reports whether allowHosts allows host, a host name or IP
// address without a port.
func (c *Client) AllowsHost(host string) bool {
	return c.hosts.allows(host)
}

// Get sends a GET request with header to rawURL under the rules of the
// package. A request the rules refuse returns an error that wraps a
// BlockedError, and is counted in MetricBlocked; a redirect that is not
// followed is counted there too, but is a Response. Cookie and Host
// headers in header are dropped.
func (c *Client) Get(ctx context.Context, rawURL string, header http.Header) (*Response, error) {
	resp, err := c.get(ctx, rawURL, header)
	if blocked, ok := errors.AsType[*BlockedError](err); ok {
		c.metrics.recordBlocked(blocked.Reason)
	}

	return resp, err
}

// get is Get without the metric.
func (c *Client) get(ctx context.Context, rawURL string, header http.Header) (*Response, error) {
	u, err := c.checkURL(rawURL)
	if err != nil {
		return nil, err
	}

	if _, err := c.lookup(ctx, u.Hostname()); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}

	req.Header = header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}

	req.Header.Del("Cookie")
	req.Header.Del("Host")

	var notFollowed string

	client := &http.Client{
		Transport: c.transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			notFollowed = redirectRefusal(u, next.URL, len(via))
			if notFollowed != "" {
				c.metrics.recordBlocked(ReasonRedirect)

				return http.ErrUseLastResponse
			}

			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send the request: %w", err)
	}

	body, truncated, err := readLimited(resp.Body, MaxBody)
	if err = errors.Join(err, resp.Body.Close()); err != nil {
		return nil, fmt.Errorf("read the body of %s: %w", u.Redacted(), err)
	}

	return &Response{Status: resp.StatusCode, Body: body, Truncated: truncated, NotFollowed: notFollowed}, nil
}

// checkURL parses rawURL and checks that the client takes it.
func (c *Client) checkURL(rawURL string) (*url.URL, error) {
	u, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}

	if !c.hosts.allows(u.Hostname()) {
		return nil, &BlockedError{Reason: ReasonHost, Message: fmt.Sprintf("the host %s is not in watch.http.allowHosts", u.Hostname())}
	}

	return u, nil
}

// lookup returns the addresses of host, an IP address or a name to resolve,
// and fails with a BlockedError when any of them is blocked.
func (c *Client) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr

	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		addrs, err = c.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}

	for _, a := range addrs {
		if c.addresses.blocked(a) {
			return nil, blockedAddress(host, a)
		}
	}

	return addrs, nil
}

// dial connects to addr, a host and port. It resolves the host again, so
// it connects to what DNS says now, and the dialer's Control hook checks
// each address before connecting. It stops at the first blocked address.
func (c *Client) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	var addrs []netip.Addr

	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else if addrs, err = c.resolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}

	var firstErr error

	for _, a := range addrs {
		conn, err := c.dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}

		if _, ok := errors.AsType[*BlockedError](err); ok {
			return nil, fmt.Errorf("dial %s: %w", host, err)
		}

		firstErr = cmp.Or(firstErr, err)
	}

	if firstErr == nil {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}

	return nil, fmt.Errorf("dial %s: %w", host, firstErr)
}

// control is the dialer's Control hook. It runs just before each connect,
// with the address the socket connects to, and refuses a blocked one.
func (c *Client) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &BlockedError{Reason: ReasonPrivateIP, Message: fmt.Sprintf("cannot check the address %s", address)}
	}

	if c.addresses.blocked(ap.Addr()) {
		return &BlockedError{
			Reason:  ReasonPrivateIP,
			Message: fmt.Sprintf("the connection to %s was refused: a private or special address that watch.http.allowPrivate does not list", ap.Addr()),
		}
	}

	return nil
}

// New builds a client with the rules in opts. It fails on an allowHosts or
// allowPrivate entry that does not parse.
func New(opts Options) (*Client, error) {
	hosts, err := parseHosts(opts.AllowHosts)
	if err != nil {
		return nil, fmt.Errorf("new HTTP check client: %w", err)
	}

	allowed, err := parsePrefixes(opts.AllowPrivate)
	if err != nil {
		return nil, fmt.Errorf("new HTTP check client: %w", err)
	}

	c := &Client{
		hosts:     hosts,
		addresses: addressPolicy{allowed: allowed},
		metrics:   opts.Metrics,
		resolver:  net.DefaultResolver,
	}

	c.dialer = &net.Dialer{Timeout: httpclient.DialTimeout, KeepAlive: keepAlive, Control: c.control}
	c.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           c.dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   httpclient.TLSHandshakeTimeout,
		ResponseHeaderTimeout: httpclient.ResponseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       idleConnTimeout,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
	}

	return c, nil
}

// CheckURL checks the form of a URL without any allowlist: an absolute
// http or https URL with a host and without user info. A refused URL
// returns a BlockedError.
func CheckURL(rawURL string) error {
	_, err := parseURL(rawURL)

	return err
}

// redirectRefusal says why a redirect from the original URL to next, after
// done redirects, is not followed, or returns "" to follow it.
func redirectRefusal(original, next *url.URL, done int) string {
	switch {
	case next.Scheme != "http" && next.Scheme != "https":
		return fmt.Sprintf("a redirect to the scheme %q was not followed", next.Scheme)
	case !strings.EqualFold(strings.TrimSuffix(next.Hostname(), "."), strings.TrimSuffix(original.Hostname(), ".")):
		return fmt.Sprintf("a redirect to another host (%s) was not followed", next.Hostname())
	case done > MaxRedirects:
		return fmt.Sprintf("more than %d redirects; the last was not followed", MaxRedirects)
	default:
		return ""
	}
}

// parseURL parses rawURL and checks its form.
func parseURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, &BlockedError{Reason: ReasonHost, Message: "the URL does not parse"}
	}

	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, &BlockedError{Reason: ReasonHost, Message: fmt.Sprintf("the scheme %q is not http or https", u.Scheme)}
	case u.User != nil:
		return nil, &BlockedError{Reason: ReasonHost, Message: "the URL holds user info; send credentials in headers from secrets"}
	case u.Hostname() == "":
		return nil, &BlockedError{Reason: ReasonHost, Message: "the URL has no host"}
	}

	return u, nil
}

// blockedAddress returns the error for a blocked address of host.
func blockedAddress(host string, a netip.Addr) *BlockedError {
	return &BlockedError{
		Reason:  ReasonPrivateIP,
		Message: fmt.Sprintf("%s resolves to %s, a private or special address that watch.http.allowPrivate does not list", host, a),
	}
}

// readLimited reads r up to limit bytes, and reports whether there was
// more.
func readLimited(r io.Reader, limit int) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, false, fmt.Errorf("read: %w", err)
	}

	if len(body) > limit {
		return body[:limit], true, nil
	}

	return body, false, nil
}

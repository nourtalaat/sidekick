package safehttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// statusHost is the host name the tests resolve to the fake server.
const statusHost = "status.example.com"

// publicAddr is a public address the fake resolver can answer with. The
// tests never connect to it.
const publicAddr = "93.184.215.14"

func TestGetBlocked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		url         string
		allowHosts  []string
		answers     [][]string
		wantReason  Reason
		wantMessage string
		wantLookups int
	}{
		{name: "a host not in allowHosts", url: "http://other.example.com/", wantReason: ReasonHost, wantMessage: "the host other.example.com is not in watch.http.allowHosts"},
		{name: "a name below an exact host", url: "http://x.status.example.com/", wantReason: ReasonHost, wantMessage: "not in watch.http.allowHosts"},
		{name: "a scheme that is not http", url: "ftp://status.example.com/", wantReason: ReasonHost, wantMessage: `the scheme "ftp" is not http or https`},
		{name: "a file URL", url: "file:///etc/passwd", wantReason: ReasonHost, wantMessage: `the scheme "file"`},
		{name: "user info", url: "http://admin:hunter2@status.example.com/", wantReason: ReasonHost, wantMessage: "the URL holds user info"}, //nolint:gosec // A fake password, to check it never shows in the error.
		{name: "no host", url: "http:///health", wantReason: ReasonHost, wantMessage: "the URL has no host"},
		{name: "a URL that does not parse", url: "http://status.example.com/%zz", wantReason: ReasonHost, wantMessage: "the URL does not parse"},
		{name: "IPv4 loopback", answers: [][]string{{"127.0.0.1"}}, wantReason: ReasonPrivateIP, wantMessage: "resolves to 127.0.0.1", wantLookups: 1},
		{name: "IPv6 loopback", answers: [][]string{{"::1"}}, wantReason: ReasonPrivateIP, wantMessage: "resolves to ::1", wantLookups: 1},
		{name: "private 10/8", answers: [][]string{{"10.0.0.1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "private 172.16/12", answers: [][]string{{"172.20.0.1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "private 192.168/16", answers: [][]string{{"192.168.0.10"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "carrier-grade NAT", answers: [][]string{{"100.64.0.1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "cloud metadata address", answers: [][]string{{"169.254.169.254"}}, wantReason: ReasonPrivateIP, wantMessage: "resolves to 169.254.169.254", wantLookups: 1},
		{name: "IPv6 link-local", answers: [][]string{{"fe80::1"}}, wantReason: ReasonPrivateIP, wantMessage: "resolves to fe80::1", wantLookups: 1},
		{name: "IPv6 unique local", answers: [][]string{{"fd00::1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "IPv4 multicast", answers: [][]string{{"224.0.0.1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "IPv6 multicast", answers: [][]string{{"ff02::1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "IPv4 unspecified", answers: [][]string{{"0.0.0.0"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "IPv6 unspecified", answers: [][]string{{"::"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "IPv4-mapped IPv6 loopback", answers: [][]string{{"::ffff:127.0.0.1"}}, wantReason: ReasonPrivateIP, wantLookups: 1},
		{name: "one blocked address among public ones", answers: [][]string{{publicAddr, "10.0.0.1"}}, wantReason: ReasonPrivateIP, wantMessage: "resolves to 10.0.0.1", wantLookups: 1},
		{
			name:       "an IP address host is checked without DNS",
			url:        "http://169.254.169.254/latest/meta-data/",
			allowHosts: []string{"169.254.169.254"},
			wantReason: ReasonPrivateIP, wantMessage: "169.254.169.254 resolves to 169.254.169.254",
		},
		{
			name:       "an IPv6 link-local host",
			url:        "http://[fe80::1%25eth0]:8080/",
			allowHosts: []string{"fe80::1"},
			wantReason: ReasonPrivateIP, wantMessage: "fe80::1%eth0",
		},
		{
			name:        "a DNS answer that changes between the check and the connection",
			answers:     [][]string{{publicAddr}, {"127.0.0.1"}},
			wantReason:  ReasonPrivateIP,
			wantMessage: "the connection to 127.0.0.1 was refused",
			wantLookups: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			port := serverPort(t, api)

			res := &fakeResolver{answers: map[string][][]string{statusHost: tt.answers}}

			reg := prometheus.NewPedanticRegistry()
			c := newClient(t, reg, Options{AllowHosts: append([]string{statusHost}, tt.allowHosts...)}, res)

			rawURL := tt.url
			if rawURL == "" {
				rawURL = fmt.Sprintf("http://%s:%s/health", statusHost, port)
			}

			resp, err := c.Get(t.Context(), rawURL, nil)
			require.Nil(t, resp)

			blocked, ok := errors.AsType[*BlockedError](err)
			require.True(t, ok, "want a BlockedError, got %v", err)
			require.Equal(t, tt.wantReason, blocked.Reason)
			require.Contains(t, blocked.Message, tt.wantMessage)
			require.NotContains(t, err.Error(), "hunter2")
			require.Equal(t, tt.wantLookups, res.lookups(statusHost))
			require.Empty(t, api.Requests(), "a blocked request never reaches the server")
			require.InDelta(t, 1, testutil.ToFloat64(c.metrics.blocked.WithLabelValues(string(tt.wantReason))), 0)
		})
	}
}

func TestGet(t *testing.T) {
	t.Parallel()

	big := bytes.Repeat([]byte("a"), MaxBody+100)
	exact := bytes.Repeat([]byte("b"), MaxBody)

	tests := []struct {
		name          string
		allowPrivate  []string
		status        int
		body          []byte
		wantBody      []byte
		wantTruncated bool
	}{
		{name: "an allowed private address", allowPrivate: []string{"127.0.0.1"}, status: http.StatusOK, body: []byte("ok"), wantBody: []byte("ok")},
		{name: "an allowed private range", allowPrivate: []string{"127.0.0.0/8"}, status: http.StatusServiceUnavailable, body: []byte("down"), wantBody: []byte("down")},
		{name: "a body over the limit is cut", allowPrivate: []string{"127.0.0.1"}, status: http.StatusOK, body: big, wantBody: big[:MaxBody], wantTruncated: true},
		{name: "a body of exactly the limit is whole", allowPrivate: []string{"127.0.0.1"}, status: http.StatusOK, body: exact, wantBody: exact},
		{name: "an empty body", allowPrivate: []string{"127.0.0.1"}, status: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/health", fakehttp.Response{Status: tt.status, Body: tt.body})
			api.ExpectAllCalled()

			res := &fakeResolver{answers: map[string][][]string{statusHost: {{"127.0.0.1"}}}}
			c := newClient(t, prometheus.NewPedanticRegistry(), Options{AllowHosts: []string{statusHost}, AllowPrivate: tt.allowPrivate}, res)

			header := http.Header{"Authorization": {"Bearer token"}, "Cookie": {"session=1"}, "Host": {"evil.example.com"}}

			resp, err := c.Get(t.Context(), fmt.Sprintf("http://%s:%s/health", statusHost, serverPort(t, api)), header)
			require.NoError(t, err)
			require.Equal(t, tt.status, resp.Status)
			require.Len(t, resp.Body, len(tt.wantBody))
			require.Equal(t, string(tt.wantBody), string(resp.Body))
			require.Equal(t, tt.wantTruncated, resp.Truncated)
			require.Empty(t, resp.NotFollowed)
			require.Equal(t, 2, res.lookups(statusHost), "the host is resolved before the request and again by the dialer")

			reqs := api.Requests()
			require.Len(t, reqs, 1)
			require.Equal(t, http.MethodGet, reqs[0].Method)
			require.Equal(t, "Bearer token", reqs[0].Header.Get("Authorization"))
			require.Empty(t, reqs[0].Header.Get("Cookie"), "the client sends no cookies")
			require.Equal(t, "Bearer token", header.Get("Authorization"), "the caller's header is not changed")
			require.Equal(t, "session=1", header.Get("Cookie"), "the caller's header is not changed")
		})
	}
}

func TestGetErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		res     *fakeResolver
		wantErr string
	}{
		{name: "DNS fails", res: &fakeResolver{fail: true}, wantErr: "resolve status.example.com: lookup status.example.com: no such host"},
		{name: "DNS has no answer", res: &fakeResolver{answers: map[string][][]string{statusHost: {{}}}}, wantErr: "resolve status.example.com: no addresses"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newClient(t, prometheus.NewPedanticRegistry(), Options{AllowHosts: []string{statusHost}}, tt.res)

			_, err := c.Get(t.Context(), "http://status.example.com/health", nil)
			require.ErrorContains(t, err, tt.wantErr)

			_, blocked := errors.AsType[*BlockedError](err)
			require.False(t, blocked)
		})
	}
}

func TestGetRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		hops            int
		location        func(base string, hop int) string
		wantStatus      int
		wantRequests    int
		wantNotFollowed string
	}{
		{name: "same host, relative", hops: 1, location: relative, wantStatus: http.StatusOK, wantRequests: 2},
		{name: "same host, absolute", hops: 2, location: absolute, wantStatus: http.StatusOK, wantRequests: 3},
		{name: "same host in another case", hops: 1, location: upperCase, wantStatus: http.StatusOK, wantRequests: 2},
		{name: "the most redirects", hops: MaxRedirects, location: relative, wantStatus: http.StatusOK, wantRequests: MaxRedirects + 1},
		{
			name: "one redirect too many", hops: MaxRedirects + 1, location: relative,
			wantStatus: http.StatusFound, wantRequests: MaxRedirects + 1, wantNotFollowed: "more than 3 redirects",
		},
		{
			name: "another host", hops: 1, location: func(string, int) string { return "http://metadata.example.com/latest" },
			wantStatus: http.StatusFound, wantRequests: 1, wantNotFollowed: "a redirect to another host (metadata.example.com) was not followed",
		},
		{
			name: "an IP address of the same server", hops: 1, location: func(base string, _ int) string { return strings.Replace(base, statusHost, "127.0.0.1", 1) + "/0" },
			wantStatus: http.StatusFound, wantRequests: 1, wantNotFollowed: "another host (127.0.0.1)",
		},
		{
			name: "another scheme", hops: 1, location: func(string, int) string { return "ftp://status.example.com/x" },
			wantStatus: http.StatusFound, wantRequests: 1, wantNotFollowed: `the scheme "ftp"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			base := fmt.Sprintf("http://%s:%s", statusHost, serverPort(t, api))

			for hop := range tt.hops + 1 {
				path := fmt.Sprintf("/%d", hop)
				if hop == 0 {
					path = "/health"
				}

				api.HandleFunc(http.MethodGet, path, func(w http.ResponseWriter, _ *http.Request) {
					if hop == tt.hops {
						w.WriteHeader(http.StatusOK)

						return
					}

					http.SetCookie(w, &http.Cookie{Name: "session", Value: "from-the-target"}) //nolint:gosec // A plain cookie over http, which a client with cookies would send back.
					w.Header().Set("Location", tt.location(base, hop+1))
					w.WriteHeader(http.StatusFound)
				})
			}

			res := &fakeResolver{answers: map[string][][]string{statusHost: {{"127.0.0.1"}}}}
			c := newClient(t, prometheus.NewPedanticRegistry(), Options{AllowHosts: []string{statusHost}, AllowPrivate: []string{"127.0.0.1"}}, res)

			resp, err := c.Get(t.Context(), base+"/health", http.Header{"Authorization": {"Bearer token"}})
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, resp.Status)
			require.Contains(t, resp.NotFollowed, tt.wantNotFollowed)

			reqs := api.Requests()
			require.Len(t, reqs, tt.wantRequests)

			require.Equal(t, "Bearer token", reqs[0].Header.Get("Authorization"))

			for _, r := range reqs {
				require.Empty(t, r.Header.Get("Cookie"), "cookies from a redirect are not sent back")
			}

			wantRedirects := 0.0
			if tt.wantNotFollowed != "" {
				wantRedirects = 1
			}

			require.InDelta(t, wantRedirects, testutil.ToFloat64(c.metrics.blocked.WithLabelValues(string(ReasonRedirect))), 0)
		})
	}
}

func TestGetTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		trustCert bool
		wantErr   string
	}{
		{name: "a trusted certificate", trustCert: true},
		{name: "an untrusted certificate is refused", wantErr: "certificate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t, fakehttp.WithTLS())
			if tt.wantErr == "" {
				api.Handle(http.MethodGet, "/health", fakehttp.Response{Body: []byte("ok")})
			}

			// The test server's certificate is for example.com.
			res := &fakeResolver{answers: map[string][][]string{"example.com": {{"127.0.0.1"}}}}
			c := newClient(t, prometheus.NewPedanticRegistry(), Options{AllowHosts: []string{"example.com"}, AllowPrivate: []string{"127.0.0.1"}}, res)

			if tt.trustCert {
				transport, ok := api.Client().Transport.(*http.Transport)
				require.True(t, ok)

				c.transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs
			}

			resp, err := c.Get(t.Context(), fmt.Sprintf("https://example.com:%s/health", serverPort(t, api)), nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.Status)
		})
	}
}

func TestClientSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(t *testing.T, c *Client)
	}{
		{name: "no proxy from the environment", check: func(t *testing.T, c *Client) {
			t.Helper()
			require.Nil(t, c.transport.Proxy)
		}},
		{name: "TLS verification is on", check: func(t *testing.T, c *Client) {
			t.Helper()
			require.False(t, c.transport.TLSClientConfig.InsecureSkipVerify)
		}},
		{name: "the dialer checks addresses", check: func(t *testing.T, c *Client) {
			t.Helper()
			require.NotNil(t, c.dialer.Control)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(Options{AllowHosts: []string{statusHost}})
			require.NoError(t, err)

			tt.check(t, c)
		})
	}
}

func TestControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		allowed []string
		wantErr string
	}{
		{name: "a public address", address: publicAddr + ":443"},
		{name: "IPv4 loopback", address: "127.0.0.1:80", wantErr: "the connection to 127.0.0.1 was refused"},
		{name: "the metadata address", address: "169.254.169.254:80", wantErr: "the connection to 169.254.169.254 was refused"},
		{name: "IPv6 link-local with a zone", address: "[fe80::1%eth0]:80", wantErr: "the connection to fe80::1%eth0 was refused"},
		{name: "IPv4-mapped loopback", address: "[::ffff:127.0.0.1]:80", wantErr: "was refused"},
		{name: "an allowed private address", address: "10.20.0.5:80", allowed: []string{"10.20.0.0/16"}},
		{name: "an address that does not parse", address: "nowhere", wantErr: "cannot check the address nowhere"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(Options{AllowPrivate: tt.allowed})
			require.NoError(t, err)

			err = c.control("tcp", tt.address, nil)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			blocked, ok := errors.AsType[*BlockedError](err)
			require.True(t, ok)
			require.Equal(t, ReasonPrivateIP, blocked.Reason)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		url         string
		wantErr     string
		wantFormErr string
		wantHost    string
		wantAllow   bool
	}{
		{name: "an allowed URL", url: "https://status.example.com/health?x=1", wantHost: statusHost, wantAllow: true},
		{name: "a host not allowed", url: "https://example.com/", wantErr: "not in watch.http.allowHosts", wantHost: "example.com"},
		{name: "a relative URL", url: "/health", wantErr: `the scheme ""`, wantFormErr: `the scheme ""`},
		{name: "user info", url: "https://u@status.example.com/", wantErr: "user info", wantFormErr: "user info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(Options{AllowHosts: []string{statusHost}})
			require.NoError(t, err)

			err = c.Check(tt.url)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}

			if tt.wantFormErr == "" {
				require.NoError(t, CheckURL(tt.url), "the form of the URL is fine")
			} else {
				require.ErrorContains(t, CheckURL(tt.url), tt.wantFormErr)
			}

			if tt.wantHost != "" {
				require.Equal(t, tt.wantAllow, c.AllowsHost(tt.wantHost))
			}
		})
	}
}

func TestMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "every reason starts at zero, and a second registration fails"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewPedanticRegistry()
			_, err := NewMetrics(reg)
			require.NoError(t, err)

			want := "# HELP sidekick_watch_http_blocked_total Watcher HTTP check requests the safety rules refused, by reason: host, private_ip or redirect.\n" +
				"# TYPE sidekick_watch_http_blocked_total counter\n" +
				`sidekick_watch_http_blocked_total{reason="host"} 0` + "\n" +
				`sidekick_watch_http_blocked_total{reason="private_ip"} 0` + "\n" +
				`sidekick_watch_http_blocked_total{reason="redirect"} 0` + "\n"
			require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), MetricBlocked))

			_, err = NewMetrics(reg)
			require.Error(t, err, tt.name)

			var m *Metrics

			require.NotPanics(t, func() { m.recordBlocked(ReasonHost) })
		})
	}
}

// newClient builds a client with metrics on reg and the fake resolver.
func newClient(t *testing.T, reg prometheus.Registerer, opts Options, res *fakeResolver) *Client {
	t.Helper()

	metrics, err := NewMetrics(reg)
	require.NoError(t, err)

	opts.Metrics = metrics

	c, err := New(opts)
	require.NoError(t, err)

	c.resolver = res

	return c
}

// serverPort returns the port the fake server listens on.
func serverPort(t *testing.T, api *fakehttp.Server) string {
	t.Helper()

	u, err := url.Parse(api.URL())
	require.NoError(t, err)

	return u.Port()
}

// relative returns a relative redirect to the next hop.
func relative(_ string, hop int) string {
	return fmt.Sprintf("/%d", hop)
}

// absolute returns an absolute redirect to the next hop on the same host.
func absolute(base string, hop int) string {
	return fmt.Sprintf("%s/%d", base, hop)
}

// upperCase returns a redirect to the next hop with the host in upper case.
func upperCase(base string, hop int) string {
	return fmt.Sprintf("%s/%d", strings.Replace(base, statusHost, strings.ToUpper(statusHost), 1), hop)
}

// fakeResolver answers lookups from a list per host, one answer per call;
// the last answer repeats. It is safe for concurrent use.
type fakeResolver struct {
	answers map[string][][]string
	fail    bool

	mu    sync.Mutex
	calls map[string]int
}

// LookupNetIP returns the next answer for host, which matches without
// regard to case, as in DNS.
func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	host = strings.ToLower(host)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.calls == nil {
		r.calls = map[string]int{}
	}

	n := r.calls[host]
	r.calls[host]++

	answers := r.answers[host]
	if r.fail || len(answers) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	answer := answers[min(n, len(answers)-1)]

	out := make([]netip.Addr, len(answer))
	for i, s := range answer {
		out[i] = netip.MustParseAddr(s)
	}

	return out, nil
}

// lookups returns how many times host was looked up.
func (r *fakeResolver) lookups(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls[host]
}

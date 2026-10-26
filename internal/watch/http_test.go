package watch

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// tokenRef is the secret of the Authorization header in the tests.
var tokenRef = registry.SecretRef{Name: "status-page", Key: "token"}

func TestHTTPCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		check        HTTPCheck
		path         string
		response     *fakehttp.Response
		allowPrivate []string
		setupMocks   func(s *registry.MockSecrets)
		noSecrets    bool
		wantResult   Result
		wantDetail   string
		wantNot      string
		wantHeader   string
	}{
		{name: "200 passes", response: &fakehttp.Response{Body: []byte("ok")}, wantResult: Pass, wantDetail: "HTTP 200"},
		{name: "503 fails with the body", response: &fakehttp.Response{Status: http.StatusServiceUnavailable, Body: []byte("down for\nmaintenance")}, wantResult: Fail, wantDetail: "HTTP 503: down for maintenance"},
		{name: "404 fails", response: &fakehttp.Response{Status: http.StatusNotFound}, wantResult: Fail, wantDetail: "HTTP 404"},
		{
			name: "an expected status passes", check: HTTPCheck{Expect: Expect{Status: []int{200, 204}}},
			response: &fakehttp.Response{Status: http.StatusNoContent}, wantResult: Pass, wantDetail: "HTTP 204",
		},
		{
			name: "200 fails when only other statuses are expected", check: HTTPCheck{Expect: Expect{Status: []int{204}}},
			response: &fakehttp.Response{Body: []byte("ok")}, wantResult: Fail, wantDetail: "HTTP 200: ok",
		},
		{
			name: "a matching body passes", check: HTTPCheck{Expect: Expect{BodyRegex: `"status":\s*"up"`}},
			response: &fakehttp.Response{Body: []byte(`{"status": "up"}`)}, wantResult: Pass, wantDetail: "HTTP 200",
		},
		{
			name: "a body that does not match fails", check: HTTPCheck{Expect: Expect{BodyRegex: `"status":\s*"up"`}},
			response: &fakehttp.Response{Body: []byte(`{"status": "degraded"}`)}, wantResult: Fail,
			wantDetail: `HTTP 200, the body does not match expect.bodyRegex: {"status": "degraded"}`,
		},
		{
			name: "the body in a detail is redacted", response: &fakehttp.Response{Status: http.StatusInternalServerError, Body: []byte("db login password=hunter2 failed")},
			wantResult: Fail, wantDetail: "[REDACTED:", wantNot: "hunter2",
		},
		{
			name: "a long body is cut in the detail", response: &fakehttp.Response{Status: http.StatusBadGateway, Body: bytes.Repeat([]byte("x"), 1000)},
			wantResult: Fail, wantDetail: "HTTP 502: " + strings.Repeat("x", maxBodyExcerpt) + "...",
		},
		{
			name: "headers come from secrets", check: HTTPCheck{Headers: map[string]registry.SecretRef{"Authorization": tokenRef}},
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), tokenRef).Return([]byte("Bearer s3cret\n"), nil)
			},
			response: &fakehttp.Response{Body: []byte("ok")}, wantResult: Pass, wantHeader: "Bearer s3cret",
		},
		{
			name: "a secret that cannot be read", check: HTTPCheck{Headers: map[string]registry.SecretRef{"Authorization": tokenRef}},
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), tokenRef).Return(nil, errors.New("file does not exist"))
			},
			wantResult: Error, wantDetail: "cannot read the secret status-page/token of header Authorization",
		},
		{
			name: "headers without secrets", check: HTTPCheck{Headers: map[string]registry.SecretRef{"Authorization": tokenRef}}, noSecrets: true,
			wantResult: Error, wantDetail: "HTTP check headers need secrets",
		},
		{
			name: "a timeout", check: HTTPCheck{Timeout: 50 * time.Millisecond},
			response: &fakehttp.Response{Delay: time.Second}, wantResult: Error, wantDetail: "timeout: the target did not answer within 50ms",
		},
		{
			name: "a host not allowed is blocked", check: HTTPCheck{URL: "http://metadata.example.com/latest"},
			wantResult: Error, wantDetail: "blocked: the host metadata.example.com is not in watch.http.allowHosts",
		},
		{
			name: "a private address not listed is blocked", allowPrivate: []string{"10.0.0.0/8"},
			wantResult: Error, wantDetail: "blocked: 127.0.0.1 resolves to 127.0.0.1, a private or special address",
		},
		{
			name: "a redirect to another host is the response", check: HTTPCheck{Expect: Expect{Status: []int{200}}},
			response:   &fakehttp.Response{Status: http.StatusFound, Headers: map[string]string{"Location": "http://other.example.com/"}},
			wantResult: Fail, wantDetail: "HTTP 302, a redirect to another host (other.example.com) was not followed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			s := registry.NewMockSecrets(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(s)
			}

			api := fakehttp.New(t)
			if tt.response != nil {
				api.Handle(http.MethodGet, "/health", *tt.response)
			}

			check := tt.check
			if check.URL == "" {
				check.URL = "http://" + serverHost(t, api) + "/health"
			}

			allowPrivate := tt.allowPrivate
			if allowPrivate == nil {
				allowPrivate = []string{loopback}
			}

			opts := Options{Secrets: s}
			if tt.noSecrets {
				opts.Secrets = nil
			}

			c := newChecker(t, testDeps{allowHosts: []string{loopback}, allowPrivate: allowPrivate, opts: opts})

			o := c.Run(t.Context(), Target{}, check)
			require.Equal(t, tt.wantResult, o.Result, o.Detail)
			require.Contains(t, o.Detail, tt.wantDetail)
			require.NotContains(t, o.Detail, "s3cret")

			if tt.wantNot != "" {
				require.NotContains(t, o.Detail, tt.wantNot)
			}

			if tt.wantHeader != "" {
				reqs := api.Requests()
				require.Len(t, reqs, 1)
				require.Equal(t, tt.wantHeader, reqs[0].Header.Get("Authorization"))
			}
		})
	}
}

func TestHTTPCheckConnectionRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "nothing listens on the port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", loopback+":0")
			require.NoError(t, err)

			addr := l.Addr().String()
			require.NoError(t, l.Close())

			c := newChecker(t, testDeps{allowHosts: []string{loopback}, allowPrivate: []string{loopback}})

			o := c.Run(t.Context(), Target{}, HTTPCheck{URL: "http://" + addr + "/health"})
			require.Equal(t, Error, o.Result, tt.name)
			require.Contains(t, o.Detail, `failed: HTTP check: send the request: Get "http://`+addr+`/health"`)
			require.Contains(t, o.Detail, "connection refused")
		})
	}
}

func TestHTTPCheckPerHostLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		checks int
	}{
		{name: "at most 2 checks at once on one host", checks: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var active, peak atomic.Int64

			api := fakehttp.New(t)
			api.HandleFunc(http.MethodGet, "/health", func(w http.ResponseWriter, _ *http.Request) {
				n := active.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}

				time.Sleep(30 * time.Millisecond)
				active.Add(-1)
				w.WriteHeader(http.StatusOK)
			})

			c := newChecker(t, testDeps{allowHosts: []string{loopback}, allowPrivate: []string{loopback}})
			check := HTTPCheck{URL: "http://" + serverHost(t, api) + "/health"}

			var wg sync.WaitGroup
			for range tt.checks {
				wg.Go(func() {
					if o := c.Run(t.Context(), Target{}, check); o.Result != Pass {
						t.Errorf("check ended %s: %s", o.Result, o.Detail)
					}
				})
			}

			wg.Wait()
			require.Equal(t, int64(httpConcurrency), peak.Load())
		})
	}
}

func TestHTTPCheckValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		check   HTTPCheck
		wantErr []string
	}{
		{name: "a full check", check: HTTPCheck{
			URL:     "https://status.example.com/health",
			Expect:  Expect{Status: []int{200, 204}, BodyRegex: "ok"},
			Headers: map[string]registry.SecretRef{"Authorization": tokenRef},
			Timeout: time.Second,
		}},
		{name: "no URL", check: HTTPCheck{}, wantErr: []string{"url: required"}},
		{name: "another scheme", check: HTTPCheck{URL: "gopher://status.example.com/"}, wantErr: []string{`url: the scheme "gopher" is not http or https`}},
		{name: "user info", check: HTTPCheck{URL: "https://u:p@status.example.com/"}, wantErr: []string{"url: the URL holds user info"}}, //nolint:gosec // A fake password in a URL the check refuses.
		{name: "a bad status", check: HTTPCheck{URL: "https://s.example.com/", Expect: Expect{Status: []int{200, 99, 600}}}, wantErr: []string{
			"expect.status[1]: 99 is not an HTTP status", "expect.status[2]: 600 is not an HTTP status",
		}},
		{name: "a bad regex", check: HTTPCheck{URL: "https://s.example.com/", Expect: Expect{BodyRegex: "("}}, wantErr: []string{"expect.bodyRegex: error parsing regexp"}},
		{name: "a bad header name", check: HTTPCheck{URL: "https://s.example.com/", Headers: map[string]registry.SecretRef{"Bad Name": tokenRef}}, wantErr: []string{"headers.Bad Name: not a valid header name"}},
		{name: "a cookie header", check: HTTPCheck{URL: "https://s.example.com/", Headers: map[string]registry.SecretRef{"cookie": tokenRef}}, wantErr: []string{"headers.cookie: HTTP checks do not send this header"}},
		{name: "a host header", check: HTTPCheck{URL: "https://s.example.com/", Headers: map[string]registry.SecretRef{"Host": tokenRef}}, wantErr: []string{"headers.Host: HTTP checks do not send this header"}},
		{name: "a negative timeout", check: HTTPCheck{URL: "https://s.example.com/", Timeout: -time.Second}, wantErr: []string{"timeout: must not be negative"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.check.Validate()
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Len(t, strings.Split(err.Error(), "\n"), len(tt.wantErr))

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

package fakehttp_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// helloFile is a body file in testdata, and helloText its content.
const (
	helloFile = "testdata/hello.txt"
	helloText = "hello from a file\n"
)

// call is one request a test sends, and the answer it expects.
type call struct {
	method     string
	path       string
	wantStatus int
	wantBody   string
	wantHeader map[string]string
}

func TestHandle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []fakehttp.Response
		calls     []call
	}{
		{
			name:      "status, headers and body",
			responses: []fakehttp.Response{{Status: http.StatusCreated, Headers: map[string]string{"Request-Id": "req_1"}, Body: []byte("made")}},
			calls:     []call{{method: http.MethodPost, path: "/v1/things", wantStatus: http.StatusCreated, wantBody: "made", wantHeader: map[string]string{"Request-Id": "req_1"}}},
		},
		{
			name:      "no status means 200",
			responses: []fakehttp.Response{{Body: []byte("ok")}},
			calls:     []call{{method: http.MethodPost, path: "/v1/things", wantStatus: http.StatusOK, wantBody: "ok"}},
		},
		{
			name:      "body from a file",
			responses: []fakehttp.Response{{BodyFile: helloFile}},
			calls:     []call{{method: http.MethodPost, path: "/v1/things", wantStatus: http.StatusOK, wantBody: helloText}},
		},
		{
			name: "responses are served in order",
			responses: []fakehttp.Response{
				{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "1"}},
				{Body: []byte("second")},
			},
			calls: []call{
				{method: http.MethodPost, path: "/v1/things", wantStatus: http.StatusTooManyRequests, wantHeader: map[string]string{"Retry-After": "1"}},
				{method: http.MethodPost, path: "/v1/things", wantStatus: http.StatusOK, wantBody: "second"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := fakehttp.New(t)
			s.Handle(http.MethodPost, "/v1/things", tt.responses...)
			s.ExpectAllCalled()

			for _, c := range tt.calls {
				status, header, body := send(t, s.Client(), c.method, s.URL()+c.path, "")
				require.Equal(t, c.wantStatus, status)
				require.Equal(t, c.wantBody, body)

				for k, v := range c.wantHeader {
					require.Equal(t, v, header.Get(k))
				}
			}
		})
	}
}

func TestUnexpectedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		path    string
		wantErr string
	}{
		{name: "a path with no route", method: http.MethodPost, path: "/v1/other", wantErr: "unexpected request POST /v1/other"},
		{name: "a method with no route", method: http.MethodGet, path: "/v1/things", wantErr: "unexpected request GET /v1/things"},
		{name: "a call after the last response", method: http.MethodPost, path: "/v1/things", wantErr: "unexpected request POST /v1/things"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newRecorder(t)
			s := fakehttp.New(rec)
			s.Handle(http.MethodPost, "/v1/things", fakehttp.Response{})

			_, _, _ = send(t, s.Client(), http.MethodPost, s.URL()+"/v1/things", "")
			require.Empty(t, rec.errors())

			status, _, _ := send(t, s.Client(), tt.method, s.URL()+tt.path, "")
			require.Equal(t, http.StatusNotImplemented, status)
			require.Equal(t, []string{tt.wantErr}, rec.errors())
			require.Len(t, s.Requests(), 2)
		})
	}
}

func TestRequests(t *testing.T) {
	t.Parallel()

	s := fakehttp.New(t)
	s.Handle(http.MethodPost, "/v1/messages", fakehttp.Response{})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL()+"/v1/messages?beta=true", strings.NewReader(`{"a":1}`))
	require.NoError(t, err)
	req.Header.Set("X-Api-Key", "key")

	resp, err := s.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	got := s.Requests()
	require.Len(t, got, 1)
	require.Equal(t, http.MethodPost, got[0].Method)
	require.Equal(t, "/v1/messages", got[0].Path)
	require.Equal(t, url.Values{"beta": {"true"}}, got[0].Query)
	require.Equal(t, "key", got[0].Header.Get("X-Api-Key"))
	require.Equal(t, []byte(`{"a":1}`), got[0].Body)
}

func TestHandleFunc(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	s := fakehttp.New(t)
	s.HandleFunc(http.MethodGet, "/count", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, calls.Add(1))
	})
	s.ExpectAllCalled()

	for _, want := range []string{"1", "2", "3"} {
		status, _, body := send(t, s.Client(), http.MethodGet, s.URL()+"/count", "")
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, want, body)
	}
}

func TestExpectAllCalled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(s *fakehttp.Server)
		calls   int
		wantErr []string
	}{
		{
			name:  "every response served",
			setup: func(s *fakehttp.Server) { s.Handle(http.MethodGet, "/a", fakehttp.Response{}, fakehttp.Response{}) },
			calls: 2,
		},
		{
			name:    "a response left over",
			setup:   func(s *fakehttp.Server) { s.Handle(http.MethodGet, "/a", fakehttp.Response{}, fakehttp.Response{}) },
			calls:   1,
			wantErr: []string{"GET /a served 1 of its 2 responses"},
		},
		{
			name: "a handler function never called",
			setup: func(s *fakehttp.Server) {
				s.HandleFunc(http.MethodGet, "/a", func(http.ResponseWriter, *http.Request) {})
			},
			wantErr: []string{"GET /a was never called"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newRecorder(t)
			s := fakehttp.New(rec)
			tt.setup(s)
			s.ExpectAllCalled()

			for range tt.calls {
				_, _, _ = send(t, s.Client(), http.MethodGet, s.URL()+"/a", "")
			}

			rec.runCleanups()
			require.Equal(t, tt.wantErr, rec.errors())
		})
	}
}

func TestHandleMisuse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(s *fakehttp.Server)
		wantErr string
	}{
		{
			name: "both Body and BodyFile",
			setup: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/a", fakehttp.Response{Body: []byte("x"), BodyFile: helloFile})
			},
			wantErr: "both Body and BodyFile",
		},
		{
			name: "a body file that does not exist",
			setup: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/a", fakehttp.Response{BodyFile: "testdata/missing.txt"})
			},
			wantErr: "read body file",
		},
		{
			name: "responses for a route with a handler function",
			setup: func(s *fakehttp.Server) {
				s.HandleFunc(http.MethodGet, "/a", func(http.ResponseWriter, *http.Request) {})
				s.Handle(http.MethodGet, "/a", fakehttp.Response{})
			},
			wantErr: "already has a handler function",
		},
		{
			name: "a handler function for a route with responses",
			setup: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/a", fakehttp.Response{})
				s.HandleFunc(http.MethodGet, "/a", func(http.ResponseWriter, *http.Request) {})
			},
			wantErr: "already has responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := newRecorder(t)
			s := fakehttp.New(rec)

			rec.inGoroutine(func() { tt.setup(s) })

			errs := rec.errors()
			require.Len(t, errs, 1)
			require.Contains(t, errs[0], tt.wantErr)
		})
	}
}

func TestDelay(t *testing.T) {
	t.Parallel()

	const delay = 200 * time.Millisecond

	tests := []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{name: "the client waits out the delay", timeout: 10 * delay},
		{name: "the client gives up first", timeout: delay / 10, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := fakehttp.New(t)
			s.Handle(http.MethodGet, "/slow", fakehttp.Response{Body: []byte("late"), Delay: delay})

			ctx, cancel := context.WithTimeout(t.Context(), tt.timeout)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL()+"/slow", nil)
			require.NoError(t, err)

			start := time.Now()
			resp, err := s.Client().Do(req)

			if tt.wantErr {
				require.ErrorIs(t, err, context.DeadlineExceeded)

				return
			}

			require.NoError(t, err)
			require.GreaterOrEqual(t, time.Since(start), delay)
			require.NoError(t, resp.Body.Close())
		})
	}
}

func TestWithTLS(t *testing.T) {
	t.Parallel()

	s := fakehttp.New(t, fakehttp.WithTLS())
	s.Handle(http.MethodGet, "/a", fakehttp.Response{Body: []byte("secure")})

	require.True(t, strings.HasPrefix(s.URL(), "https://"))

	status, _, body := send(t, s.Client(), http.MethodGet, s.URL()+"/a", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "secure", body)
}

// send sends one request and returns the status, headers and body of the
// answer.
func send(t *testing.T, client *http.Client, method, target, body string) (int, http.Header, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp.StatusCode, resp.Header, string(data)
}

// recorder is a testing.TB that keeps the failures the server reports, so a
// test can check them, and runs cleanups when the test asks.
type recorder struct {
	testing.TB

	mu       sync.Mutex
	failures []string
	cleanups []func()
}

// newRecorder returns a recorder for t. Its cleanups also run when t ends.
func newRecorder(t *testing.T) *recorder {
	t.Helper()

	r := &recorder{TB: t}
	t.Cleanup(r.runCleanups)

	return r
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failures = append(r.failures, strings.TrimPrefix(fmt.Sprintf(format, args...), "fakehttp: "))
}

// Fatalf records the failure and stops the goroutine, as testing.T does.
// Call code that may reach it through inGoroutine.
func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Cleanup(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanups = append(r.cleanups, f)
}

// errors returns the failures recorded so far.
func (r *recorder) errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.failures) == 0 {
		return nil
	}

	return append([]string(nil), r.failures...)
}

// runCleanups runs the cleanups not yet run, last added first.
func (r *recorder) runCleanups() {
	for {
		r.mu.Lock()

		if len(r.cleanups) == 0 {
			r.mu.Unlock()

			return
		}

		f := r.cleanups[len(r.cleanups)-1]
		r.cleanups = r.cleanups[:len(r.cleanups)-1]
		r.mu.Unlock()

		f()
	}
}

// inGoroutine runs f on a goroutine of its own and waits for it, so a
// Fatalf inside f stops only that goroutine.
func (r *recorder) inGoroutine(f func()) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		f()
	}()

	<-done
}

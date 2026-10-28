// Package fakehttp is a fake HTTP server for tests of code that calls a third
// party. Each test or subtest starts its own Server, so tests that use it can
// run in parallel: nothing is shared between servers.
//
// A Server answers only the routes a test registers. Any other request fails
// the test and gets a 501, so a missing mock never passes silently.
package fakehttp

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

// Response is one canned answer to a request.
type Response struct {
	// Status is the HTTP status. Zero means 200.
	Status int
	// Headers are set on the response.
	Headers map[string]string
	// Body is the response body. Leave it empty when BodyFile is set.
	Body []byte
	// BodyFile is a file whose content is the response body, relative to
	// the test's package directory, for example "fixture/text.sse".
	BodyFile string
	// Delay is how long to wait before answering. The wait ends early when
	// the client gives up on the request.
	Delay time.Duration
}

// Request is one request the server received.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
	// ClientCertificate is the certificate the client sent, on a server
	// made WithClientCertificates. It is nil when the client sent none.
	ClientCertificate *x509.Certificate
}

// Option changes how New starts a server.
type Option func(*options)

// Server is a fake HTTP server that lives for one test.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[routeKey]*route
	requests []Request
}

type options struct {
	tls         bool
	clientCerts bool
}

type routeKey struct {
	method string
	path   string
}

// route is what a server does for one method and path: either it serves
// responses in order, or it calls handler.
type route struct {
	responses []Response
	served    int
	handler   http.HandlerFunc
	calls     int
}

// WithTLS makes the server use HTTPS. The client from Server.Client trusts
// its certificate.
func WithTLS() Option {
	return func(o *options) { o.tls = true }
}

// WithClientCertificates makes the server use HTTPS, as WithTLS does, and
// ask each client for a certificate without requiring one. The certificate
// a client sent is in its Request.ClientCertificate.
func WithClientCertificates() Option {
	return func(o *options) {
		o.tls = true
		o.clientCerts = true
	}
}

// New starts a server for t and closes it when t ends.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()

	var o options
	for _, opt := range opts {
		opt(&o)
	}

	s := &Server{t: t, routes: map[routeKey]*route{}}

	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serveHTTP))

	switch {
	case o.clientCerts:
		s.srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
		s.srv.StartTLS()
	case o.tls:
		s.srv.StartTLS()
	default:
		s.srv.Start()
	}

	t.Cleanup(s.srv.Close)

	return s
}

// URL returns the server's base URL, such as http://127.0.0.1:51234.
func (s *Server) URL() string {
	return s.srv.URL
}

// Client returns an HTTP client for the server. For a TLS server it trusts
// the server's certificate.
func (s *Server) Client() *http.Client {
	return s.srv.Client()
}

// CertificatePEM returns the certificate of a TLS server in PEM form, for
// tests of clients that are told which CA to trust. It is nil for a server
// without TLS.
func (s *Server) CertificatePEM() []byte {
	cert := s.srv.Certificate()
	if cert == nil {
		return nil
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// Handle answers requests to method and path with the responses, one each,
// in order: the first request gets the first response, and so on. A request
// after the last response fails the test. Calling Handle again for the same
// route adds responses to the end. Call it from the test's own goroutine.
func (s *Server) Handle(method, path string, responses ...Response) {
	s.t.Helper()

	loaded := make([]Response, 0, len(responses))

	for _, r := range responses {
		loaded = append(loaded, s.load(r))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rt := s.route(method, path)
	if rt.handler != nil {
		s.t.Fatalf("fakehttp: %s %s already has a handler function", method, path)
	}

	rt.responses = append(rt.responses, loaded...)
}

// HandleFunc answers every request to method and path with h. Use it when
// canned responses are not enough. h can read the request body, although
// the server has already recorded it. Call it from the test's own goroutine.
func (s *Server) HandleFunc(method, path string, h http.HandlerFunc) {
	s.t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	rt := s.route(method, path)
	if len(rt.responses) > 0 {
		s.t.Fatalf("fakehttp: %s %s already has responses", method, path)
	}

	rt.handler = h
}

// Requests returns every request the server received so far, in order,
// including the ones no route matched. It is safe to call at any time.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

// ExpectAllCalled makes the test fail at its end unless every response given
// to Handle was served and every HandleFunc handler was called at least once.
func (s *Server) ExpectAllCalled() {
	s.t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		for key, rt := range s.routes {
			switch {
			case rt.handler != nil && rt.calls == 0:
				s.t.Errorf("fakehttp: %s %s was never called", key.method, key.path)
			case rt.handler == nil && rt.served < len(rt.responses):
				s.t.Errorf("fakehttp: %s %s served %d of its %d responses", key.method, key.path, rt.served, len(rt.responses))
			}
		}
	})
}

// serveHTTP records the request and answers it from its route.
func (s *Server) serveHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		s.t.Errorf("fakehttp: read the body of %s %s: %v", req.Method, req.URL.Path, err)
		http.Error(w, "fakehttp: cannot read the request body", http.StatusBadRequest)

		return
	}

	handler, resp, ok := s.next(req, body)
	if !ok {
		s.t.Errorf("fakehttp: unexpected request %s %s", req.Method, req.URL.Path)
		http.Error(w, "fakehttp: no response for this request", http.StatusNotImplemented)

		return
	}

	if handler != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		handler(w, req)

		return
	}

	write(w, req, resp)
}

// next records the request and returns what should answer it: a handler
// function or a response. ok is false when nothing should.
func (s *Server) next(req *http.Request, body []byte) (handler http.HandlerFunc, resp Response, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	recorded := Request{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  req.URL.Query(),
		Header: req.Header.Clone(),
		Body:   body,
	}

	if req.TLS != nil && len(req.TLS.PeerCertificates) > 0 {
		recorded.ClientCertificate = req.TLS.PeerCertificates[0]
	}

	s.requests = append(s.requests, recorded)

	rt, found := s.routes[routeKey{method: req.Method, path: req.URL.Path}]

	switch {
	case !found:
		return nil, Response{}, false
	case rt.handler != nil:
		rt.calls++

		return rt.handler, Response{}, true
	case rt.served < len(rt.responses):
		rt.served++

		return nil, rt.responses[rt.served-1], true
	default:
		return nil, Response{}, false
	}
}

// route returns the route for method and path, adding it when it is new.
// The caller holds s.mu.
func (s *Server) route(method, path string) *route {
	key := routeKey{method: method, path: path}

	rt, ok := s.routes[key]
	if !ok {
		rt = &route{}
		s.routes[key] = rt
	}

	return rt
}

// load reads the body file of r, if it has one, into r.Body.
func (s *Server) load(r Response) Response {
	s.t.Helper()

	if r.BodyFile == "" {
		return r
	}

	if len(r.Body) > 0 {
		s.t.Fatalf("fakehttp: a response has both Body and BodyFile %s", r.BodyFile)
	}

	data, err := os.ReadFile(r.BodyFile)
	if err != nil {
		s.t.Fatalf("fakehttp: read body file: %v", err)
	}

	r.Body = data

	return r
}

// write sends resp, after its delay.
func write(w http.ResponseWriter, req *http.Request, resp Response) {
	if resp.Delay > 0 {
		timer := time.NewTimer(resp.Delay)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-req.Context().Done():
			return
		}
	}

	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}

	w.WriteHeader(status)
	_, _ = w.Write(resp.Body)
}

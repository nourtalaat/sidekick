package prometheus

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// errNoCertificates is returned when the CA secret holds no PEM
// certificate.
var errNoCertificates = errors.New("the CA secret holds no PEM certificate")

// tlsTransport sends requests with the TLS setup of the config. The client
// certificate is read from its secrets for each new connection, and the CA
// for each request: when the CA changes, a new transport with the new CA
// takes over, so a rotated secret needs no restart.
type tlsTransport struct {
	base    *http.Transport
	cfg     TLS
	secrets registry.Secrets

	mu sync.Mutex
	// ca is the CA the current transport trusts.
	ca []byte
	// current is the transport for ca; nil until the first request.
	current *http.Transport
}

// newTLSTransport returns a transport that sends requests with base and
// the TLS setup of cfg.
func newTLSTransport(base *http.Transport, cfg TLS, secrets registry.Secrets) *tlsTransport {
	return &tlsTransport{base: base, cfg: cfg, secrets: secrets}
}

// RoundTrip implements http.RoundTripper.
func (t *tlsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next, err := t.transport(req.Context())
	if err != nil {
		return nil, fmt.Errorf("set up TLS: %w", err)
	}

	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

// transport returns the transport for the CA the secret holds now.
func (t *tlsTransport) transport(ctx context.Context) (*http.Transport, error) {
	var ca []byte

	if t.cfg.CASecret != nil {
		raw, err := t.secrets.Resolve(ctx, *t.cfg.CASecret)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %s/%s: %w", t.cfg.CASecret.Name, t.cfg.CASecret.Key, err)
		}

		ca = raw
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current != nil && bytes.Equal(ca, t.ca) {
		return t.current, nil
	}

	next := t.base.Clone()

	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if next.TLSClientConfig != nil {
		config = next.TLSClientConfig.Clone()
	}

	if ca != nil {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errNoCertificates
		}

		config.RootCAs = pool
	}

	if t.cfg.CertSecret != nil {
		config.GetClientCertificate = t.clientCertificate
	}

	next.TLSClientConfig = config

	if t.current != nil {
		t.current.CloseIdleConnections()
	}

	t.current, t.ca = next, ca

	return next, nil
}

// clientCertificate reads the client certificate and its key from their
// secrets, for one TLS handshake.
func (t *tlsTransport) clientCertificate(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	ctx := info.Context()

	certPEM, err := t.secrets.Resolve(ctx, *t.cfg.CertSecret)
	if err != nil {
		return nil, fmt.Errorf("resolve secret %s/%s: %w", t.cfg.CertSecret.Name, t.cfg.CertSecret.Key, err)
	}

	keyPEM, err := t.secrets.Resolve(ctx, *t.cfg.KeySecret)
	if err != nil {
		return nil, fmt.Errorf("resolve secret %s/%s: %w", t.cfg.KeySecret.Name, t.cfg.KeySecret.Key, err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("read the client certificate and key: %w", err)
	}

	return &cert, nil
}

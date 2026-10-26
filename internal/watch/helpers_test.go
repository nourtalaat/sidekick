package watch

import (
	"log/slog"
	"net/url"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// loopback is the host of the fake servers. The tests allow it, and allow
// it as a private address unless a case says otherwise.
const loopback = "127.0.0.1"

// testDeps are the parts of a checker a test sets.
type testDeps struct {
	sources      []tools.Source
	allowHosts   []string
	allowPrivate []string
	opts         Options
	reg          prometheus.Registerer
}

// newChecker builds a checker on a real guard and safe HTTP client.
func newChecker(t *testing.T, d testDeps) *Checker {
	t.Helper()

	r := newRedactor(t)

	guard, err := tools.New(d.sources, tools.Options{Redactor: r, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	client, err := safehttp.New(safehttp.Options{AllowHosts: d.allowHosts, AllowPrivate: d.allowPrivate})
	require.NoError(t, err)

	opts := d.opts
	opts.Guard = guard
	opts.HTTP = client
	opts.Redactor = r

	if d.reg != nil {
		opts.Metrics, err = NewMetrics(d.reg)
		require.NoError(t, err)
	}

	c, err := New(opts)
	require.NoError(t, err)

	return c
}

// newRedactor returns a redactor with the default rules and a fixed key.
func newRedactor(t *testing.T) *redact.Redactor {
	t.Helper()

	r, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	return r
}

// serverHost returns host:port of a fake server.
func serverHost(t *testing.T, api *fakehttp.Server) string {
	t.Helper()

	u, err := url.Parse(api.URL())
	require.NoError(t, err)

	return u.Host
}

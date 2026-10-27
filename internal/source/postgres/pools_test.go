package postgres

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// testPassword is the password in the test URLs. No error or log line may
// hold it.
const testPassword = "s3cret-pw"

func TestPoolConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       error
		wantWarning   bool
	}{
		{name: "verify-full", url: "postgres://monitor:" + testPassword + "@db.example.com:5432/orders?sslmode=verify-full"},
		{name: "verify-ca", url: "postgresql://monitor:" + testPassword + "@db.example.com/orders?sslmode=verify-ca"},
		{name: "a loopback address without TLS", url: "postgres://monitor:" + testPassword + "@127.0.0.1:5432/orders?sslmode=disable"},
		{name: "localhost without sslmode", url: "postgres://monitor:" + testPassword + "@localhost/orders"},
		{name: "a Unix socket", url: "postgres://monitor@/orders?host=/var/run/postgresql"},
		{name: "require does not verify the server", url: "postgres://monitor:" + testPassword + "@db.example.com/orders?sslmode=require", wantErr: errInsecure},
		{name: "no sslmode means prefer", url: "postgres://monitor:" + testPassword + "@db.example.com/orders", wantErr: errInsecure},
		{
			name:    "one host of several is on the network",
			url:     "postgres://monitor:" + testPassword + "@localhost:5432,db.example.com:5432/orders?sslmode=disable",
			wantErr: errInsecure,
		},
		{
			name:          "allowInsecure lets an unverified URL through with a warning",
			url:           "postgres://monitor:" + testPassword + "@db.example.com/orders?sslmode=require",
			allowInsecure: true,
			wantWarning:   true,
		},
		{name: "keyword and value form", url: "host=db.example.com user=monitor password=" + testPassword + " sslmode=verify-full", wantErr: errNotURL},
		{name: "another scheme", url: "mysql://monitor:" + testPassword + "@db.example.com/orders", wantErr: errNotURL},
		{name: "a URL pgx cannot parse", url: "postgres://monitor:" + testPassword + "@db.example.com/orders?sslmode=verify-full&connect_timeout=soon", wantErr: errNotURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			p := &pools{logger: slog.New(slog.NewTextHandler(&logs, nil)), maxConns: 3}
			db := Database{Name: "orders", URLSecret: testURLRef, AllowInsecure: tt.allowInsecure}

			cfg, err := p.poolConfig(tt.url, db)
			require.NotContains(t, logs.String(), testPassword)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), testPassword)

				return
			}

			require.NoError(t, err)
			require.Equal(t, int32(3), cfg.MaxConns)
			require.Equal(t, applicationName, cfg.ConnConfig.RuntimeParams["application_name"])
			require.Equal(t, connectTimeout, cfg.ConnConfig.ConnectTimeout)
			require.NotNil(t, cfg.AfterConnect)

			if tt.wantWarning {
				require.Contains(t, logs.String(), "does not verify the server's TLS certificate")
				require.Contains(t, logs.String(), "sslmode=require")
			} else {
				require.Empty(t, logs.String())
			}
		})
	}
}

func TestPoolsGet(t *testing.T) {
	t.Parallel()

	first := "postgres://monitor:" + testPassword + "@localhost/orders"
	rotated := "postgres://monitor:new-" + testPassword + "@localhost/orders"

	tests := []struct {
		name string
		// urls are what the secret holds on each call, with a trailing line
		// break where a file would have one.
		urls     []string
		wantSame []bool
	}{
		{name: "the same URL keeps its pool", urls: []string{first, first + "\n"}, wantSame: []bool{true}},
		{name: "a rotated URL gets a new pool", urls: []string{first, rotated, rotated}, wantSame: []bool{false, true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			// The last call comes after close, which reads the secret too.
			for _, u := range append(tt.urls, tt.urls[len(tt.urls)-1]) {
				secrets.EXPECT().Resolve(gomock.Any(), testURLRef).Return([]byte(u), nil)
			}

			p := &pools{secrets: secrets, logger: slog.New(slog.DiscardHandler), maxConns: 1, open: map[string]openPool{}}
			db := Database{Name: "orders", URLSecret: testURLRef}

			prev, err := p.get(t.Context(), db)
			require.NoError(t, err)

			for i, same := range tt.wantSame {
				got, err := p.get(t.Context(), db)
				require.NoError(t, err)
				require.Equal(t, same, got == prev, "call %d", i+2)

				prev = got
			}

			p.close()

			_, err = p.get(t.Context(), db)
			require.ErrorIs(t, err, errClosed)
		})
	}
}

func TestPoolsGetErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr error
		wantMsg string
	}{
		{
			name:    "an unverified URL names the database and the secret, not the URL",
			url:     "postgres://monitor:" + testPassword + "@db.example.com/orders?sslmode=prefer",
			wantErr: errInsecure,
			wantMsg: "database orders, URL secret orders-db/url",
		},
		{name: "a secret that is not a URL", url: testPassword, wantErr: errNotURL, wantMsg: "must hold a postgres:// URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), testURLRef).Return([]byte(tt.url), nil)

			p := &pools{secrets: secrets, logger: slog.New(slog.DiscardHandler), maxConns: 1, open: map[string]openPool{}}

			_, err := p.get(t.Context(), Database{Name: "orders", URLSecret: testURLRef})
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorContains(t, err, tt.wantMsg)
			require.NotContains(t, err.Error(), testPassword)
		})
	}
}

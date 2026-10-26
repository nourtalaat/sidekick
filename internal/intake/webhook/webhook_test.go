package webhook

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var errTest = errors.New("test failure")

var bearerAuth = config.WebhookAuth{Bearer: &registry.SecretRef{Name: "hooks", Key: "token"}}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		webhooks map[string]config.Webhook
		adapters map[string]intake.Adapter
		wantErr  error
	}{
		{
			name:     "every endpoint has an adapter and authentication",
			webhooks: map[string]config.Webhook{"am": {Auth: bearerAuth}},
			adapters: map[string]intake.Adapter{"am": nil},
		},
		{
			name:     "an endpoint without an adapter",
			webhooks: map[string]config.Webhook{"am": {Auth: bearerAuth}},
			wantErr:  errNoAdapter,
		},
		{
			name:     "an endpoint without authentication",
			webhooks: map[string]config.Webhook{"am": {}},
			adapters: map[string]intake.Adapter{"am": nil},
			wantErr:  errNoAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(Options{Webhooks: tt.webhooks, Adapters: tt.adapters})
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		events  []event.Event
		err     error
		wantErr error
	}{
		{name: "events with keys and known kinds", events: []event.Event{{Key: "a", Kind: event.KindAlert}, {Key: "b", Kind: event.KindResolved}}},
		{name: "the adapter fails", err: errTest, wantErr: errTest},
		{name: "an event without a key", events: []event.Event{{Key: "a", Kind: event.KindAlert}, {Kind: event.KindAlert}}, wantErr: errEmptyKey},
		{name: "an event of an unknown kind", events: []event.Event{{Key: "a", Kind: "page"}}, wantErr: errBadKind},
		{name: "an event without a kind", events: []event.Event{{Key: "a"}}, wantErr: errBadKind},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := intake.NewMockAdapter(gomock.NewController(t))
			adapter.EXPECT().Parse(gomock.Any(), gomock.Any()).Return(tt.events, tt.err)

			events, err := parse(t.Context(), endpoint{name: "am", adapter: adapter}, intake.Request{})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.events, events)
		})
	}
}

// verifyingAdapter is an adapter with its own signature scheme.
type verifyingAdapter struct {
	*intake.MockAdapter
	*intake.MockSignatureVerifier
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// verifier is the error of the adapter's own check, when the
		// adapter has one.
		verifier   *error
		secret     string
		resolveErr error
		header     string
		wantUnauth bool
		wantErr    bool
	}{
		{name: "the configured bearer check", secret: "token\n", header: "Bearer token"},
		{name: "the configured bearer check fails", secret: "token", header: "Bearer other", wantUnauth: true},
		{name: "the adapter's own check passes", verifier: new(error), secret: "token", header: "anything"},
		{name: "the adapter's own check fails", verifier: ptr(errTest), secret: "token", wantUnauth: true},
		{name: "the adapter's own check fails as unauthenticated", verifier: ptr(intake.ErrUnauthenticated), secret: "token", wantUnauth: true},
		{name: "an empty secret fails before any check", verifier: ptr(error(nil)), secret: "\n", wantUnauth: true},
		{name: "the secret cannot be read", resolveErr: errTest, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			secrets := registry.NewMockSecrets(ctrl)
			secrets.EXPECT().Resolve(gomock.Any(), *bearerAuth.Bearer).Return([]byte(tt.secret), tt.resolveErr)

			var adapter intake.Adapter = intake.NewMockAdapter(ctrl)

			if tt.verifier != nil {
				verifier := intake.NewMockSignatureVerifier(ctrl)
				if strings.TrimSpace(tt.secret) != "" {
					verifier.EXPECT().VerifySignature(gomock.Any(), []byte(strings.TrimSpace(tt.secret))).Return(*tt.verifier)
				}

				adapter = verifyingAdapter{MockAdapter: intake.NewMockAdapter(ctrl), MockSignatureVerifier: verifier}
			}

			h := &Handler{secrets: secrets, log: slog.New(slog.DiscardHandler)}
			req := intake.Request{Header: http.Header{"Authorization": {tt.header}}}

			err := h.authenticate(t.Context(), endpoint{name: "am", cfg: config.Webhook{Auth: bearerAuth}, adapter: adapter}, req)

			switch {
			case tt.wantUnauth:
				require.ErrorIs(t, err, intake.ErrUnauthenticated)
			case tt.wantErr:
				require.Error(t, err)
				require.NotErrorIs(t, err, intake.ErrUnauthenticated)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestStoredKey(t *testing.T) {
	t.Parallel()

	redactor, err := redact.New(redact.Options{HashKey: []byte("key"), Custom: []redact.Rule{{Name: "seeded", Pattern: "SEEDED-[0-9]+"}}})
	require.NoError(t, err)

	h := &Handler{redactor: redactor}

	tests := []struct {
		name     string
		key      string
		wantKept bool
	}{
		{name: "a plain key is kept", key: "build-4711", wantKept: true},
		{name: "a key at the length limit is kept", key: strings.Repeat("k", maxKeyBytes), wantKept: true},
		{name: "a longer key is hashed", key: strings.Repeat("k", maxKeyBytes+1)},
		{name: "a key the redactor changes is hashed", key: "deploy-SEEDED-1"},
		{name: "a key with a NUL character is hashed", key: "a\x00b"},
		{name: "a key that is not valid UTF-8 is hashed", key: "a\xffb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := h.storedKey(tt.key)
			if tt.wantKept {
				require.Equal(t, tt.key, got)

				return
			}

			require.True(t, strings.HasPrefix(got, hashedKeyPrefix))
			require.Len(t, got, len(hashedKeyPrefix)+64)
			require.Equal(t, got, h.storedKey(tt.key), "the same key gives the same hash")
		})
	}
}

func TestDBText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "disk full", want: "disk full"},
		{name: "NUL characters", in: "a\x00b\x00", want: "a�b�"},
		{name: "invalid UTF-8", in: "a\xff\xfeb", want: "a�b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, dbText(tt.in))
			require.True(t, validText(dbText(tt.in)))
		})
	}
}

func TestDBJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no NUL escape", in: `{"a":"b\n"}`, want: `{"a":"b\n"}`},
		{name: "NUL escape in a value", in: `{"a":"x\u0000y"}`, want: `{"a":"x�y"}`},
		{name: "NUL escape in a key", in: `{"\u0000":1}`, want: `{"�":1}`},
		{name: "escaped backslash before u0000 is text", in: `{"a":"\\u0000"}`, want: `{"a":"\\u0000"}`},
		{name: "escaped backslash then a NUL escape", in: `["\\\u0000"]`, want: `["\\�"]`},
		{name: "several escapes", in: `["\u0000\"\u0000"]`, want: `["�\"�"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, string(dbJSON([]byte(tt.in))))
		})
	}
}

func ptr[T any](v T) *T {
	return &v
}

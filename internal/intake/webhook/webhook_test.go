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

func TestRedactedLinks(t *testing.T) {
	t.Parallel()

	redactor, err := redact.New(redact.Options{HashKey: []byte("key"), Custom: []redact.Rule{{Name: "seeded", Pattern: "SEEDED-[0-9]+"}}})
	require.NoError(t, err)

	h := &Handler{redactor: redactor}

	tests := []struct {
		name  string
		links []event.Link
		want  string
	}{
		{name: "no links", want: ""},
		{
			name:  "links are kept with their titles",
			links: []event.Link{{Title: "Runbook", URL: "https://runbooks.example.com/disk"}, {URL: "https://grafana.example.com/d/1"}},
			want:  `[{"title": "Runbook", "url": "https://runbooks.example.com/disk"}, {"title": "", "url": "https://grafana.example.com/d/1"}]`,
		},
		{
			name:  "a title is redacted",
			links: []event.Link{{Title: "Dashboard SEEDED-1", URL: "https://grafana.example.com/d/1"}},
			want:  `[{"title": "Dashboard ` + redactor.Text("SEEDED-1").Text + `", "url": "https://grafana.example.com/d/1"}]`,
		},
		{
			name: "a link whose URL redaction changes is dropped",
			links: []event.Link{
				{Title: "Leaky", URL: "https://example.com/?token=SEEDED-2"},
				{Title: "Kept", URL: "https://example.com/ok"},
			},
			want: `[{"title": "Kept", "url": "https://example.com/ok"}]`,
		},
		{
			name:  "links without a usable URL are dropped",
			links: []event.Link{{Title: "Empty"}, {Title: "NUL", URL: "https://example.com/\x00"}},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := h.redactedLinks(tt.links)
			require.NoError(t, err)

			if tt.want == "" {
				require.Nil(t, got)

				return
			}

			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func ptr[T any](v T) *T {
	return &v
}

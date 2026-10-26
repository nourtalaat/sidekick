package config

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestRedactionRedactOptions(t *testing.T) {
	t.Parallel()

	keyRef := registry.SecretRef{Name: "sidekick-redaction", Key: "key"}
	errBroken := errors.New("resolver broken")

	tests := []struct {
		name       string
		yaml       string
		setupMocks func(s *registry.MockSecrets)
		want       redact.Options
		wantErr    error
	}{
		{
			name: "no redaction section uses the defaults",
			want: redact.Options{},
		},
		{
			name: "empty built-in list turns built-in rules off",
			yaml: "redaction:\n  builtIn: []\n",
			want: redact.Options{BuiltIn: []string{}},
		},
		{
			name: "rules and hash key",
			yaml: `redaction:
  builtIn: [emails, ip_addresses]
  custom:
    - name: customer_id
      pattern: "cus_[A-Za-z0-9]{14}"
  hashKeySecret: { name: sidekick-redaction, key: key }
`,
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key bytes\n"), nil)
			},
			want: redact.Options{
				BuiltIn: []string{redact.Emails, redact.IPAddresses},
				Custom:  []redact.Rule{{Name: "customer_id", Pattern: "cus_[A-Za-z0-9]{14}"}},
				HashKey: []byte("key bytes\n"),
			},
		},
		{
			name: "unreadable hash key",
			yaml: "redaction:\n  hashKeySecret: { name: sidekick-redaction, key: key }\n",
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return(nil, errBroken)
			},
			wantErr: errBroken,
		},
		{
			name: "empty hash key",
			yaml: "redaction:\n  hashKeySecret: { name: sidekick-redaction, key: key }\n",
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte{}, nil)
			},
			wantErr: ErrEmptyHashKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := registry.NewMockSecrets(gomock.NewController(t))
			if tt.setupMocks != nil {
				tt.setupMocks(s)
			}

			cfg, err := Parse(t.Context(), []byte(minimalYAML(t)+tt.yaml), testOptions())
			require.NoError(t, err)

			got, err := cfg.Redaction.RedactOptions(t.Context(), s)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			got.Logger = slog.New(slog.DiscardHandler)
			_, err = redact.New(got)
			require.NoError(t, err, "the options build a redactor")
		})
	}
}

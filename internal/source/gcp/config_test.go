package gcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "one project", cfg: Config{Projects: []string{"shop-prod"}}},
		{name: "a domain-scoped project", cfg: Config{Projects: []string{"example.com:shop-prod", "shop-staging"}}},
		{name: "no projects", cfg: Config{}, wantErr: []string{"projects: list at least one project ID"}},
		{
			name:    "projects that are not project IDs",
			cfg:     Config{Projects: []string{"Shop Prod", "abc", "shop-prod-"}},
			wantErr: []string{`projects[0]: "Shop Prod" is not a project ID`, `projects[1]: "abc"`, `projects[2]: "shop-prod-"`},
		},
		{name: "a project listed twice", cfg: Config{Projects: []string{"shop-prod", "shop-prod"}}, wantErr: []string{`projects[1]: "shop-prod" is listed twice`}},
		{
			name:    "a timeout longer than the HTTP client waits",
			cfg:     Config{Projects: []string{"shop-prod"}, Settings: source.Settings{Timeout: 2 * time.Minute}},
			wantErr: []string{"timeout: 2m0s is above"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

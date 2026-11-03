package gitlabrevert

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testToken is the secret of the token in the tests.
var testToken = registry.SecretRef{Name: "gitlab-revert", Key: "token"}

// validLimits returns limits that pass the config check.
func validLimits() Limits {
	return Limits{
		Projects: []string{"acme/platform/deploy"},
		Bounds:   gitchange.Bounds{MaxFiles: 5, MaxChangedLines: 100, BaseBranch: "main"},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "valid"},
		{name: "every limit at its top", change: func(c *Config) {
			c.Limits.MaxFiles, c.Limits.MaxChangedLines, c.Limits.MaxAge = 25, 1000, 30*24*time.Hour
			c.Limits.RefusedPaths = []string{"infra/**"}
		}},
		{name: "every limit at its bottom", change: func(c *Config) {
			c.Limits.MaxFiles, c.Limits.MaxChangedLines, c.Limits.MaxAge = 1, 1, time.Nanosecond
		}},
		{name: "maxFiles just over", change: func(c *Config) { c.Limits.MaxFiles = 26 }, wantErr: "limits.maxFiles"},
		{name: "maxFiles zero", change: func(c *Config) { c.Limits.MaxFiles = 0 }, wantErr: "limits.maxFiles"},
		{name: "maxChangedLines just over", change: func(c *Config) { c.Limits.MaxChangedLines = 1001 }, wantErr: "limits.maxChangedLines"},
		{name: "maxChangedLines zero", change: func(c *Config) { c.Limits.MaxChangedLines = 0 }, wantErr: "limits.maxChangedLines"},
		{name: "maxAge just over", change: func(c *Config) { c.Limits.MaxAge = 30*24*time.Hour + time.Nanosecond }, wantErr: "limits.maxAge"},
		{name: "maxAge negative", change: func(c *Config) { c.Limits.MaxAge = -time.Hour }, wantErr: "limits.maxAge"},
		{name: "no base branch", change: func(c *Config) { c.Limits.BaseBranch = "" }, wantErr: "limits.baseBranch"},
		{name: "no project", change: func(c *Config) { c.Limits.Projects = nil }, wantErr: "limits.projects"},
		{name: "a wildcard project", change: func(c *Config) { c.Limits.Projects = []string{"acme/*"} }, wantErr: "limits.projects[0]"},
		{name: "a bad refused path glob", change: func(c *Config) { c.Limits.RefusedPaths = []string{"a/[b"} }, wantErr: "limits.refusedPaths[0]"},
		{name: "no token secret", change: func(c *Config) { c.TokenSecret = registry.SecretRef{} }, wantErr: "tokenSecret: required"},
		{name: "a token secret without a key", change: func(c *Config) { c.TokenSecret.Key = "" }, wantErr: "tokenSecret: required"},
		{name: "an http base URL", change: func(c *Config) { c.BaseURL = "http://gitlab.example.com" }, wantErr: "must use https"},
		{name: "an http loopback base URL", change: func(c *Config) { c.BaseURL = "http://127.0.0.1:8080" }},
		{name: "an https base URL", change: func(c *Config) { c.BaseURL = "https://gitlab.example.com" }},
		{name: "the API root as the base URL", change: func(c *Config) { c.BaseURL = "https://gitlab.example.com/api/v4" }, wantErr: "without /api/v4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{TokenSecret: testToken, Limits: validLimits()}
			if tt.change != nil {
				tt.change(&cfg)
			}

			err := cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestLimitsMaxAge(t *testing.T) {
	t.Parallel()

	require.Equal(t, 72*time.Hour, (&Limits{}).maxAge(), "empty means the default")
	require.Equal(t, time.Hour, (&Limits{MaxAge: time.Hour}).maxAge())
}

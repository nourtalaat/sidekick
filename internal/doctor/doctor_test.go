package doctor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		openErr    func(url string) error
		wantChecks []string
		wantFailed bool
	}{
		{
			name: "an error that holds the database URL",
			openErr: func(url string) error {
				return fmt.Errorf("failed to connect to %s", url)
			},
			wantChecks: []string{"database", "slack", "model claude", "prices", "sources"},
			wantFailed: true,
		},
		{
			name: "an error that holds the password alone",
			openErr: func(string) error {
				return fmt.Errorf("password %q was rejected", testDBPassword)
			},
			wantChecks: []string{"database", "slack", "model claude", "prices", "sources"},
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/v1/models", fakehttp.Response{BodyFile: filepath.Join("fixture", "anthropic-models.json"), Headers: jsonHeaders})
			api.ExpectAllCalled()

			cfg := parseConfig(t, baseConfig, defaultSecrets, config.Registries{}, "ANTHROPIC_URL", api.URL())

			results := Run(t.Context(), Options{
				Config: cfg,
				HTTP:   api.Client(),
				OpenDatabase: func(_ context.Context, url string) (Database, error) {
					return nil, tt.openErr(url)
				},
			})

			require.Equal(t, tt.wantChecks, checks(results))
			require.Equal(t, tt.wantFailed, Failed(results))

			var out bytes.Buffer
			require.NoError(t, Write(&out, results))
			require.NotContains(t, out.String(), testDBPassword)
			require.NotContains(t, out.String(), testAPIKey)
			require.Contains(t, results[0].Message, scrubbed)
		})
	}
}

func TestWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []Result
		want    string
	}{
		{
			name: "every status, with details and a fix",
			results: []Result{
				{Check: "database", Status: StatusOK, Message: "connected"},
				{Check: "slack", Status: StatusSkip, Message: "no slack section"},
				{Check: "source logs", Status: StatusWarn, Message: "connected; 0 of 1 tools exposed", Details: []string{"a is not exposed: writes"}, Fix: "allow a tool"},
				{Check: "model claude", Status: StatusFail, Message: "rejected", Fix: "put a valid key"},
			},
			want: strings.Join([]string{
				"ok    database: connected",
				"skip  slack: no slack section",
				"warn  source logs: connected; 0 of 1 tools exposed",
				"      - a is not exposed: writes",
				"      fix: allow a tool",
				"FAIL  model claude: rejected",
				"      fix: put a valid key",
				"",
			}, "\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, Write(&out, tt.results))
			require.Equal(t, tt.want, out.String())
		})
	}
}

func TestFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []Result
		want    bool
	}{
		{name: "nothing", want: false},
		{name: "warnings only", results: []Result{{Status: StatusOK}, {Status: StatusWarn}, {Status: StatusSkip}}, want: false},
		{name: "one failure", results: []Result{{Status: StatusOK}, {Status: StatusFail}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, Failed(tt.results))
		})
	}
}

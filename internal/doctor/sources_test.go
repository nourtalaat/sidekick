package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testSourceWait is how long the tests wait for a source to connect.
const testSourceWait = 100 * time.Millisecond

var (
	errDialRefused = errors.New("dial tcp 10.0.0.9:8101: connection refused")
	errBadToken    = errors.New("401 Unauthorized")
	errNoServer    = errors.New("url: no such host")
)

// fakeSourceConfig is the config of the fake source type.
type fakeSourceConfig struct {
	source.Settings `yaml:",inline"`
}

var objectSchema = json.RawMessage(`{"type":"object"}`)

func TestCheckSources(t *testing.T) {
	t.Parallel()

	search := source.Tool{Name: "search_logs", InputSchema: objectSchema, ReadOnly: true}
	deleteTool := source.Tool{Name: "delete_index", InputSchema: objectSchema}

	tests := []struct {
		name     string
		section  string
		buildErr error
		logError error
		// wait is how long the source has to connect; zero means
		// testSourceWait.
		wait       time.Duration
		setupMocks func(src *source.MockSource)
		want       Result
	}{
		{
			name:    "connected, with a writing tool left out",
			section: "{ type: fake }",
			setupMocks: func(src *source.MockSource) {
				src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{search, deleteTool}, nil)
			},
			want: Result{
				Check: "source logs", Status: StatusOK,
				Message: "connected; 1 of 2 tools exposed",
				Details: []string{"delete_index is not exposed: writes, the source marks it as a tool that can change something"},
			},
		},
		{
			name:    "connected after a wait",
			section: "{ type: fake }",
			wait:    time.Minute,
			setupMocks: func(src *source.MockSource) {
				gomock.InOrder(
					src.EXPECT().Tools(gomock.Any()).Return(nil, fmt.Errorf("list tools of logs: %w", source.ErrUnavailable)),
					src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{search}, nil),
				)
			},
			want: Result{Check: "source logs", Status: StatusOK, Message: "connected; 1 of 1 tools exposed", Details: []string{}},
		},
		{
			name:    "no tool passes the allowlist",
			section: "{ type: fake, tools: { allow: [query_*] } }",
			setupMocks: func(src *source.MockSource) {
				src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{search}, nil)
			},
			want: Result{
				Check: "source logs", Status: StatusWarn,
				Message: "connected; 0 of 1 tools exposed",
				Details: []string{"search_logs is not exposed: not_allowed, no tools.allow glob matches it"},
				Fix:     "the model can call none of its tools; check tools.allow and tools.deny, or set tools.trustAnnotations: false if the server marks read-only tools as writing",
			},
		},
		{
			name:     "never connects",
			section:  "{ type: fake }",
			logError: errDialRefused,
			setupMocks: func(src *source.MockSource) {
				src.EXPECT().Tools(gomock.Any()).Return(nil, fmt.Errorf("list tools of logs: %w", source.ErrUnavailable)).MinTimes(1)
			},
			want: Result{
				Check: "source logs", Status: StatusFail,
				Message: "cannot list its tools: not connected after 100ms: list tools of logs: source unavailable (last error: " + errDialRefused.Error() + ")",
				Fix:     "check the source's url, that the server is running and that Sidekick's pod can reach it; for an MCP sidecar, check its logs with `kubectl logs <pod> -c <sidecar>`",
			},
		},
		{
			name:    "listing fails",
			section: "{ type: fake }",
			setupMocks: func(src *source.MockSource) {
				src.EXPECT().Tools(gomock.Any()).Return(nil, errBadToken)
			},
			want: Result{
				Check: "source logs", Status: StatusFail,
				Message: "cannot list its tools: list tools: " + errBadToken.Error(),
				Fix:     "check the source's url, that the server is running and that Sidekick's pod can reach it; for an MCP sidecar, check its logs with `kubectl logs <pod> -c <sidecar>`",
			},
		},
		{
			name:     "the source cannot be built",
			section:  "{ type: fake }",
			buildErr: errNoServer,
			want: Result{
				Check: "source logs", Status: StatusFail,
				Message: `cannot set up the source: build source "logs": construct: ` + errNoServer.Error(),
				Fix:     "mount every secret the source names; `sidekick config validate --check-secrets -f <file>` points at the field",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			src := source.NewMockSource(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(src)
				src.EXPECT().Close().Return(nil)
			}

			sources := registry.New[source.Source]("source")
			sources.Register("fake", registry.Typed(func(_ context.Context, _ string, _ fakeSourceConfig, deps registry.Deps) (source.Source, error) {
				if tt.buildErr != nil {
					return nil, tt.buildErr
				}

				if tt.logError != nil {
					deps.Logger.Warn("cannot connect; will retry", "error", tt.logError)
				}

				return src, nil
			}))

			regs := config.Registries{Sources: sources}
			cfg := parseConfig(t, baseConfig+"sources:\n  logs: "+tt.section+"\n", defaultSecrets, regs)

			wait := testSourceWait
			if tt.wait > 0 {
				wait = tt.wait
			}

			got := newDoctor(Options{Config: cfg, Registries: regs, SourceWait: wait}).checkSources(t.Context())

			require.Equal(t, []Result{tt.want}, got)
		})
	}
}

func TestCheckSourcesWithoutSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want []Result
	}{
		{name: "no sources", want: []Result{{Check: "sources", Status: StatusSkip, Message: "no sources"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := parseConfig(t, strings.TrimSpace(baseConfig), defaultSecrets, config.Registries{})

			require.Equal(t, tt.want, newDoctor(Options{Config: cfg}).checkSources(t.Context()))
		})
	}
}

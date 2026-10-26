package kubernetes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cfg           Config
		wantNames     []string
		wantNamespace []any
	}{
		{
			name:      "logs off",
			wantNames: []string{toolListEvents, toolGetWorkload, toolListPodProblems, toolGetRolloutHistory, toolListNodes},
		},
		{
			name:          "logs on, in two namespaces",
			cfg:           Config{Logs: true, Namespaces: []string{"prod", "staging"}},
			wantNames:     []string{toolListEvents, toolGetWorkload, toolListPodProblems, toolGetRolloutHistory, toolGetPodLogs, toolListNodes},
			wantNamespace: []any{"prod", "staging"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, nil)

			list, err := s.Tools(t.Context())
			require.NoError(t, err)

			names := make([]string, 0, len(list))

			for _, tool := range list {
				names = append(names, tool.Name)

				require.True(t, tool.ReadOnly, tool.Name)
				require.NotEmpty(t, tool.Description, tool.Name)

				var schema struct {
					Type       string `json:"type"`
					Properties map[string]struct {
						Enum []any `json:"enum"`
					} `json:"properties"`
				}

				require.NoError(t, json.Unmarshal(tool.InputSchema, &schema), tool.Name)
				require.Equal(t, "object", schema.Type)

				if ns, ok := schema.Properties["namespace"]; ok {
					require.Equal(t, tt.wantNamespace, ns.Enum, tool.Name)
				}
			}

			require.Equal(t, tt.wantNames, names)
		})
	}
}

// TestThroughTheGuard shows what the model sees of a call, after the
// guard's checks.
func TestThroughTheGuard(t *testing.T) {
	t.Parallel()

	crashing := pod("prod", "checkout-crash", corev1.PodRunning, nil, corev1.ContainerStatus{Name: "app", RestartCount: 3, State: running()})
	crashing.CreationTimestamp = metav1.NewTime(testNow.Add(-time.Hour))

	tests := []struct {
		name        string
		cfg         Config
		call        model.ToolCall
		wantIsError bool
		wantText    string
		wantNoData  bool
	}{
		{
			name:     "a call that finds a problem",
			call:     model.ToolCall{ID: "call-1", Name: "k8s__list_pod_problems", Input: json.RawMessage(`{"namespace":"prod"}`)},
			wantText: "app: 3 restarts",
		},
		{
			name:       "a call that finds nothing",
			call:       model.ToolCall{ID: "call-1", Name: "k8s__list_events", Input: json.RawMessage(`{"namespace":"prod"}`)},
			wantNoData: true,
		},
		{
			name:        "a namespace outside the config never reaches the source",
			cfg:         Config{Namespaces: []string{"staging"}},
			call:        model.ToolCall{ID: "call-1", Name: "k8s__list_pod_problems", Input: json.RawMessage(`{"namespace":"prod"}`)},
			wantIsError: true,
		},
		{
			name:        "logs are off",
			call:        model.ToolCall{ID: "call-1", Name: "k8s__get_pod_logs", Input: json.RawMessage(`{"namespace":"prod","pod":"x"}`)},
			wantIsError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, []runtime.Object{crashing})

			r, err := redact.New(redact.Options{HashKey: []byte("test")})
			require.NoError(t, err)

			g, err := tools.New([]tools.Source{{Name: "k8s", Source: s, Settings: tt.cfg.Settings}}, tools.Options{Redactor: r})
			require.NoError(t, err)

			res := g.Start(t.Context(), "inv-1", nil).Call(t.Context(), tt.call)
			require.Equal(t, tt.wantIsError, res.ToolResult.IsError)
			require.Equal(t, tt.wantNoData, res.NoData)
			require.Contains(t, res.ToolResult.Content, tt.wantText)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		deps    registry.Deps
		wantErr string
	}{
		{name: "outside a cluster without a kubeconfig", cfg: Config{}, wantErr: "use the pod's service account (set kubeconfigSecret to read another cluster)"},
		{
			name:    "a kubeconfig without a secrets resolver",
			cfg:     Config{KubeconfigSecret: &registry.SecretRef{Name: "other", Key: "kubeconfig"}},
			wantErr: errNoSecrets.Error(),
		},
		{
			name:    "bad shared settings",
			cfg:     Config{Settings: source.Settings{Concurrency: -1}},
			wantErr: "concurrency: must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(t.Context(), "k8s", tt.cfg, tt.deps)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCallUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool the source does not have", tool: "delete_pod"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, Config{}, nil)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

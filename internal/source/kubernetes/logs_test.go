package kubernetes

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestGetPodLogs(t *testing.T) {
	t.Parallel()

	const lines = "2026-10-26T11:59:00Z starting\n2026-10-26T11:59:01Z pq: timeout acquiring connection from pool\n"

	long := strings.Repeat("2026-10-26T11:59:00Z a line of logs that repeats\n", maxResultSize/40)

	tests := []struct {
		name          string
		cfg           Config
		logs          string
		failWith      error
		input         string
		wantText      string
		wantOptions   *corev1.PodLogOptions
		wantEvidence  source.Evidence
		wantNoData    bool
		wantTruncated bool
		wantTool      []string
		wantErr       error
	}{
		{
			name:         "the last lines by default",
			logs:         lines,
			input:        `{"namespace":"prod","pod":"checkout-1"}`,
			wantText:     lines,
			wantOptions:  &corev1.PodLogOptions{TailLines: ptr(int64(defaultLogLines))},
			wantEvidence: source.Evidence{Kind: source.EvidenceLogQuery, Title: "kubectl logs checkout-1 -n prod --tail=200 --timestamps"},
		},
		{
			name:        "the previous run of one container since a time",
			logs:        lines,
			input:       `{"namespace":"prod","pod":"checkout-1","container":"app","tailLines":50,"previous":true,"since":"30m"}`,
			wantText:    lines,
			wantOptions: &corev1.PodLogOptions{Container: "app", TailLines: ptr(int64(50)), Previous: true, SinceTime: ptr(at(30 * time.Minute))},
			wantEvidence: source.Evidence{
				Kind: source.EvidenceLogQuery, Title: "kubectl logs checkout-1 -n prod -c app --tail=50 --timestamps --previous --since=30m",
			},
		},
		{
			name:         "at most the maximum lines, with a console link",
			cfg:          Config{ConsoleURL: testConsoleURL},
			logs:         lines,
			input:        `{"namespace":"prod","pod":"checkout-1","tailLines":5000}`,
			wantText:     lines,
			wantOptions:  &corev1.PodLogOptions{TailLines: ptr(int64(maxLogLines))},
			wantEvidence: source.Evidence{Kind: source.EvidenceLogQuery, Title: "Pod prod/checkout-1", URL: "https://console.example.com/pods/prod/checkout-1"},
		},
		{
			name:          "long logs keep the newest lines",
			logs:          long + "the last line\n",
			input:         `{"namespace":"prod","pod":"checkout-1"}`,
			wantTruncated: true,
			wantEvidence:  source.Evidence{Kind: source.EvidenceLogQuery, Title: "kubectl logs checkout-1 -n prod --tail=200 --timestamps"},
		},
		{
			name:         "no logs",
			logs:         "\n",
			input:        `{"namespace":"prod","pod":"checkout-1"}`,
			wantNoData:   true,
			wantEvidence: source.Evidence{Kind: source.EvidenceLogQuery, Title: "kubectl logs checkout-1 -n prod --tail=200 --timestamps"},
		},
		{
			name:     "a pod with several containers and none named",
			failWith: apierrors.NewBadRequest("a container name must be specified for pod checkout-1, choose one of: [app proxy]"),
			input:    `{"namespace":"prod","pod":"checkout-1"}`,
			wantTool: []string{"a container name must be specified"},
		},
		{
			name:     "a missing permission",
			failWith: forbidden("", "pods/log"),
			input:    `{"namespace":"prod","pod":"checkout-1"}`,
			wantTool: []string{`may not get pods/log in namespace prod`, `the "get" verb on "pods/log" in the core API group`},
		},
		{
			name:     "a namespace the config does not list",
			cfg:      Config{Namespaces: []string{"staging"}},
			input:    `{"namespace":"prod","pod":"checkout-1"}`,
			wantTool: []string{`namespace "prod" is not one Sidekick may read`},
		},
		{name: "an API server that cannot be reached", failWith: errConnection, input: `{"namespace":"prod","pod":"checkout-1"}`, wantErr: errConnection},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got *corev1.PodLogOptions

			logs := reactor{verb: "get", resource: "pods", react: func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "log" {
					return false, nil, nil
				}

				if g, ok := action.(k8stesting.GenericAction); ok {
					got, _ = g.GetValue().(*corev1.PodLogOptions)
				}

				if tt.failWith != nil {
					return true, nil, tt.failWith
				}

				return true, &runtime.Unknown{Raw: []byte(tt.logs)}, nil
			}}

			cfg := tt.cfg
			cfg.Logs = true
			s := newTestSource(t, cfg, nil, logs)

			res, err := call(t, s, toolGetPodLogs, tt.input)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)

				return
			case tt.wantTool != nil:
				requireToolError(t, err, tt.wantTool...)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantNoData, res.NoData)
			require.Equal(t, tt.wantTruncated, res.Truncated)
			require.Equal(t, []source.Evidence{tt.wantEvidence}, res.Evidence)

			switch {
			case tt.wantTruncated:
				require.LessOrEqual(t, len(res.Text), maxResultSize)
				require.True(t, strings.HasPrefix(res.Text, "2026-10-26T11:59:00Z a line"))
				require.True(t, strings.HasSuffix(res.Text, "the last line\n"))
			case !tt.wantNoData:
				require.Equal(t, tt.wantText, res.Text)
			}

			if tt.wantOptions != nil {
				want := *tt.wantOptions
				want.Timestamps = true
				want.LimitBytes = ptr(int64(maxLogRead))
				require.Equal(t, &want, got)
			}
		})
	}
}

func TestPodLogsNeedLogs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		logs bool
	}{
		{name: "logs off", logs: false},
		{name: "logs on", logs: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, Config{Logs: tt.logs}, nil)

			tools, err := s.Tools(t.Context())
			require.NoError(t, err)

			listed := false

			for _, tool := range tools {
				listed = listed || tool.Name == toolGetPodLogs
			}

			require.Equal(t, tt.logs, listed)

			_, err = call(t, s, toolGetPodLogs, `{"namespace":"prod","pod":"checkout-1"}`)
			if tt.logs {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errUnknownTool)
			}
		})
	}
}

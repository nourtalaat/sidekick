//go:build integration

package investigator

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// inconclusiveReport is a report that needs no evidence.
const inconclusiveReport = `{"verdict": "inconclusive", "confidence": "low", "reason": "Too little data.", "summary": "Not sure yet.", "findings": []}`

func TestStartWatcherTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		// wantStarted is the name of the watcher that starts, or "".
		wantStarted string
		wantResult  string
	}{
		{
			name:        "a watcher inside the route's limits starts",
			input:       `{"name": "cleared", "type": "alert", "interval": "1m"}`,
			wantStarted: "cleared",
			wantResult:  `Started watcher "cleared"`,
		},
		{
			name:       "a type the route does not allow gets an error naming the limit",
			input:      `{"name": "logs", "type": "tool", "tool": "signoz__search_logs"}`,
			wantResult: `type: "tool" checks are not allowed; routes[0].watcherLimits.types allows [http alert]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{turns: []modeltest.Turn{
				turn(modeltest.Call("w1", watch.StartWatcherTool, tt.input)),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, inconclusiveReport)),
			}})
			h.handler.opts.Watchers = newModelStarter(t, h)

			require.NoError(t, h.run(t))

			steps := h.steps(t)
			require.Contains(t, promptTools(t, steps[0]), watch.StartWatcherTool, "the prompt offers start_watcher")

			var result db.Step

			for _, s := range steps {
				if s.ToolCallID != nil && *s.ToolCallID == "w1" {
					result = s
				}
			}

			parts, err := decodeParts(result.Parts)
			require.NoError(t, err)
			require.Len(t, parts, 1)

			content, ok := parts[0].(model.ToolResult)
			require.True(t, ok)
			require.Contains(t, content.Content, tt.wantResult)

			watchers, err := h.pool.Query(t.Context(), "SELECT name, origin, started_by_seq FROM watchers")
			require.NoError(t, err)

			var names []string

			for watchers.Next() {
				var (
					name, origin string
					seq          *int32
				)

				require.NoError(t, watchers.Scan(&name, &origin, &seq))
				require.Equal(t, string(watch.OriginModel), origin)
				require.Equal(t, new(int32(1)), seq, "the watcher records the model turn that started it")

				names = append(names, name)
			}

			require.NoError(t, watchers.Err())

			if tt.wantStarted == "" {
				require.Empty(t, names)
				require.Equal(t, statusRejected, result.Status)

				return
			}

			require.Equal(t, []string{tt.wantStarted}, names)
			require.Equal(t, statusOK, result.Status)
		})
	}
}

// newModelStarter returns a start_watcher starter that allows HTTP and
// alert checks on the payments route.
func newModelStarter(t *testing.T, h *harness) *watch.ModelStarter {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New(nil, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	client, err := safehttp.New(safehttp.Options{AllowHosts: []string{"status.example.com"}})
	require.NoError(t, err)

	checker, err := watch.New(watch.Options{Guard: guard, HTTP: client, Redactor: redactor})
	require.NoError(t, err)

	starter, err := watch.NewModelStarter(watch.ModelStarterOptions{
		Pool:    h.pool,
		Checker: checker,
		Policy: watch.Policy{
			MaxPerGroup: watch.DefaultMaxPerGroup,
			MaxTotal:    watch.DefaultMaxTotal,
			Defaults:    watch.Level{Path: "watch.defaults", Settings: watch.Settings{Interval: 30 * time.Second, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second}},
			Bounds:      watch.Bounds{Path: "watch", MinInterval: 10 * time.Second, MaxInterval: 15 * time.Minute, MaxTimeout: 4 * time.Hour},
			Routes: map[string]watch.RoutePolicy{"payments": {
				Path:       "routes[0]",
				AllowModel: true,
				Types:      []watch.Type{watch.TypeHTTP, watch.TypeAlert},
				Hosts:      []string{"status.example.com"},
			}},
		},
	})
	require.NoError(t, err)

	return starter
}

// promptTools returns the names of the tools a prompt step offers.
func promptTools(t *testing.T, prompt db.Step) []string {
	t.Helper()

	var p struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}

	require.NoError(t, json.Unmarshal(prompt.Parts, &p))

	names := make([]string, len(p.Tools))
	for i, tool := range p.Tools {
		names[i] = tool.Name
	}

	return names
}

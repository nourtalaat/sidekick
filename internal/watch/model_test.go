package watch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// modelPolicy allows HTTP checks to status.example.com and one tool on the
// payments route, with route bounds inside the global ones.
func modelPolicy() Policy {
	return Policy{
		MaxPerGroup: DefaultMaxPerGroup,
		MaxTotal:    DefaultMaxTotal,
		Defaults:    Level{Path: "watch.defaults", Settings: Settings{Interval: 30 * time.Second, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second}},
		Bounds:      Bounds{Path: "watch", MinInterval: 10 * time.Second, MaxInterval: 15 * time.Minute, MaxTimeout: 4 * time.Hour},
		Routes: map[string]RoutePolicy{
			"payments": {
				Path:       "routes[0]",
				AllowModel: true,
				Types:      []Type{TypeHTTP, TypeTool},
				Tools:      []string{"signoz__query_metrics"},
				Hosts:      []string{"status.example.com"},
				Defaults:   Level{Path: "routes[0].watcherDefaults", Settings: Settings{Interval: 20 * time.Second}},
				Bounds:     Bounds{Path: "routes[0].watcherLimits", MinInterval: 15 * time.Second, MaxTimeout: 2 * time.Hour},
			},
			"quiet": {Path: "routes[1]", Types: []Type{TypeHTTP}},
		},
	}
}

func TestStartWatcherLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		route string
		input string
		// allowHosts is watch.http.allowHosts; empty means the route's
		// host.
		allowHosts  []string
		wantRefusal string
		want        func(t *testing.T, w NewWatcher)
	}{
		{
			name:  "an HTTP check inside every limit",
			route: "payments",
			input: `{"name": "status-page", "type": "http", "url": "https://status.example.com/api", "expect": {"status": [200]}}`,
			want: func(t *testing.T, w NewWatcher) {
				t.Helper()
				require.Equal(t, HTTPCheck{URL: "https://status.example.com/api", Expect: Expect{Status: []int{200}}}, w.Check)
				require.Equal(t, Settings{Interval: 20 * time.Second, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second}, w.Settings)
				require.Equal(t, OriginModel, w.Origin)
				require.Equal(t, int32(7), *w.StartedBySeq)
			},
		},
		{
			name:  "a tool check names the source's own tool",
			route: "payments",
			input: `{"name": "error-rate", "type": "tool", "tool": "signoz__query_metrics", "args": {"q": "rate"}, "condition": {"jsonPath": "$.last", "op": "lt", "value": 0.01}, "interval": "1m", "passesToRecover": 3}`,
			want: func(t *testing.T, w NewWatcher) {
				t.Helper()

				c, ok := w.Check.(ToolCheck)
				require.True(t, ok)
				require.Equal(t, "signoz", c.Source)
				require.Equal(t, "query_metrics", c.Tool)
				require.Equal(t, time.Minute, w.Settings.Interval)
				require.Equal(t, 3, w.Settings.PassesToRecover)
			},
		},
		{
			name:        "a type outside watcherLimits.types",
			route:       "payments",
			input:       `{"name": "cleared", "type": "alert"}`,
			wantRefusal: `type: "alert" checks are not allowed; routes[0].watcherLimits.types allows [http tool]`,
		},
		{
			name:        "a host outside watcherLimits.hosts",
			route:       "payments",
			input:       `{"name": "probe", "type": "http", "url": "https://internal.example.com/"}`,
			wantRefusal: `url: host "internal.example.com" is not allowed; routes[0].watcherLimits.hosts allows [status.example.com]`,
		},
		{
			name:        "a host in watcherLimits.hosts but not in watch.http.allowHosts",
			route:       "payments",
			input:       `{"name": "probe", "type": "http", "url": "https://status.example.com/"}`,
			allowHosts:  []string{"other.example.com"},
			wantRefusal: "url: ",
		},
		{
			name:        "a tool outside watcherLimits.tools",
			route:       "payments",
			input:       `{"name": "pods", "type": "tool", "tool": "k8s__get_workload", "condition": {"textRegex": "Running"}}`,
			wantRefusal: `tool: "k8s__get_workload" is not allowed; routes[0].watcherLimits.tools allows [signoz__query_metrics]`,
		},
		{
			name:        "an interval below the route's minimum",
			route:       "payments",
			input:       `{"name": "status-page", "type": "http", "url": "https://status.example.com/", "interval": "5s"}`,
			wantRefusal: "interval: 5s is below routes[0].watcherLimits.minInterval 15s; interval: 5s is below watch.minInterval 10s",
		},
		{
			name:        "a timeout above the route's maximum",
			route:       "payments",
			input:       `{"name": "status-page", "type": "http", "url": "https://status.example.com/", "timeout": "3h"}`,
			wantRefusal: "timeout: 3h0m0s is above routes[0].watcherLimits.maxTimeout 2h0m0s",
		},
		{
			name:        "an interval that is not a duration",
			route:       "payments",
			input:       `{"name": "status-page", "type": "http", "url": "https://status.example.com/", "interval": "often"}`,
			wantRefusal: `interval: "often" is not a positive duration such as 30s or 10m`,
		},
		{
			name:        "a name that does not fit",
			route:       "payments",
			input:       `{"name": "Status Page", "type": "http", "url": "https://status.example.com/"}`,
			wantRefusal: `name: "Status Page" ` + NameRule,
		},
		{
			name:        "an unknown field",
			route:       "payments",
			input:       `{"name": "status-page", "type": "http", "url": "https://status.example.com/", "method": "POST"}`,
			wantRefusal: `the input does not decode: json: unknown field "method"`,
		},
		{
			name:        "a route that does not offer start_watcher",
			route:       "quiet",
			input:       `{"name": "status-page", "type": "http", "url": "https://status.example.com/"}`,
			wantRefusal: `start_watcher is not allowed on route "quiet"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			allowHosts := tt.allowHosts
			if allowHosts == nil {
				allowHosts = []string{"status.example.com"}
			}

			m := &ModelStarter{opts: ModelStarterOptions{Checker: newChecker(t, testDeps{allowHosts: allowHosts}), Policy: modelPolicy()}}

			w, refusal := m.prepare(ModelRequest{
				Route:           tt.route,
				GroupID:         uuid.New(),
				InvestigationID: uuid.New(),
				StartedBySeq:    7,
				Input:           json.RawMessage(tt.input),
				SourceTool: func(qualified string) (string, string, bool) {
					if qualified == "signoz__query_metrics" {
						return "signoz", "query_metrics", true
					}

					return "", "", false
				},
			})

			if tt.wantRefusal != "" {
				require.Contains(t, refusal, tt.wantRefusal)

				return
			}

			require.Empty(t, refusal)
			tt.want(t, w)
		})
	}
}

func TestStartWatcherCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inGroup int64
		running int64
		want    string
	}{
		{name: "under both limits", inGroup: 2, running: 99},
		{name: "the group is full", inGroup: 3, running: 10, want: "watch.maxPerGroup is 3, and the group has 3 watchers"},
		{name: "the deployment is full", inGroup: 0, running: 100, want: "watch.maxTotal is 100, and 100 watchers are running"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limit := countsProblem(modelPolicy(), tt.inGroup, tt.running)
			if tt.want == "" {
				require.Nil(t, limit)

				return
			}

			require.EqualError(t, limit, tt.want)
		})
	}
}

func TestStartWatcherSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		route     string
		exposed   []string
		wantOK    bool
		wantTypes []string
		wantTools []string
	}{
		{name: "the route's types, and its tools the run exposes", route: "payments", exposed: []string{"signoz__query_metrics", "signoz__search_logs"}, wantOK: true, wantTypes: []string{"http", "tool"}, wantTools: []string{"signoz__query_metrics"}},
		{name: "no tool checks when the run exposes none of the route's tools", route: "payments", exposed: []string{"signoz__search_logs"}, wantOK: true, wantTypes: []string{"http"}},
		{name: "a route that does not offer it", route: "quiet"},
		{name: "a route that is not configured", route: "gone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := &ModelStarter{opts: ModelStarterOptions{Policy: modelPolicy()}}

			spec, ok := m.Spec(tt.route, tt.exposed)
			require.Equal(t, tt.wantOK, ok)

			if !ok {
				return
			}

			require.Equal(t, StartWatcherTool, spec.Name)

			var schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			}

			require.NoError(t, json.Unmarshal(spec.InputSchema, &schema))
			require.Equal(t, tt.wantTypes, schema.Properties["type"].Enum)

			tool, hasTool := schema.Properties["tool"]
			require.Equal(t, tt.wantTools != nil, hasTool)
			require.Equal(t, tt.wantTools, tool.Enum)
		})
	}

	var none *ModelStarter

	_, ok := none.Spec("payments", nil)
	require.False(t, ok, "a nil starter offers the tool nowhere")
}

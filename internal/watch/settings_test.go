package watch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	global := Level{Path: "watch.defaults", Settings: Settings{Interval: 30 * time.Second, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second}}
	globalBounds := Bounds{Path: "watch", MinInterval: 10 * time.Second, MaxInterval: 15 * time.Minute, MaxTimeout: 4 * time.Hour}
	routeBounds := Bounds{Path: "routes[0].watcherLimits", MinInterval: 15 * time.Second, MaxTimeout: 2 * time.Hour}

	tests := []struct {
		name         string
		watcher      Settings
		strategy     Settings
		route        Settings
		want         Settings
		wantProblems []string
	}{
		{
			name: "global defaults fill everything",
			want: global.Settings,
		},
		{
			name:  "the route's defaults win over the global ones",
			route: Settings{Interval: 20 * time.Second, Timeout: 90 * time.Minute},
			want:  Settings{Interval: 20 * time.Second, Timeout: 90 * time.Minute, PassesToRecover: 2, CheckTimeout: 10 * time.Second},
		},
		{
			name:     "the strategy's defaults win over the route's",
			strategy: Settings{Interval: 25 * time.Second, PassesToRecover: 4},
			route:    Settings{Interval: 20 * time.Second, Timeout: 90 * time.Minute},
			want:     Settings{Interval: 25 * time.Second, Timeout: 90 * time.Minute, PassesToRecover: 4, CheckTimeout: 10 * time.Second},
		},
		{
			name:     "the watcher's own values win over every default",
			watcher:  Settings{Interval: 16 * time.Second, Timeout: 30 * time.Minute, PassesToRecover: 3, CheckTimeout: 5 * time.Second},
			strategy: Settings{Interval: 25 * time.Second, PassesToRecover: 4},
			route:    Settings{Interval: 20 * time.Second},
			want:     Settings{Interval: 16 * time.Second, Timeout: 30 * time.Minute, PassesToRecover: 3, CheckTimeout: 5 * time.Second},
		},
		{
			name:         "an interval below the route's minimum names the route bound",
			watcher:      Settings{Interval: 12 * time.Second},
			want:         Settings{Interval: 12 * time.Second, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second},
			wantProblems: []string{"routes[0].watchers[0].interval: 12s is below routes[0].watcherLimits.minInterval 15s"},
		},
		{
			name:    "an interval below both minimums names both bounds",
			watcher: Settings{Interval: 5 * time.Second},
			want:    Settings{Interval: 5 * time.Second, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second},
			wantProblems: []string{
				"routes[0].watchers[0].interval: 5s is below routes[0].watcherLimits.minInterval 15s",
				"routes[0].watchers[0].interval: 5s is below watch.minInterval 10s",
			},
		},
		{
			name:         "an interval above the global maximum",
			watcher:      Settings{Interval: 20 * time.Minute},
			want:         Settings{Interval: 20 * time.Minute, Timeout: time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second},
			wantProblems: []string{"routes[0].watchers[0].interval: 20m0s is above watch.maxInterval 15m0s"},
		},
		{
			name:         "a timeout from the strategy above the route's maximum names the strategy",
			strategy:     Settings{Timeout: 3 * time.Hour},
			want:         Settings{Interval: 30 * time.Second, Timeout: 3 * time.Hour, PassesToRecover: 2, CheckTimeout: 10 * time.Second},
			wantProblems: []string{"strategy.watcherDefaults.timeout: 3h0m0s is above routes[0].watcherLimits.maxTimeout 2h0m0s"},
		},
		{
			name:         "a negative passesToRecover",
			watcher:      Settings{PassesToRecover: -1},
			want:         Settings{Interval: 30 * time.Second, Timeout: time.Hour, PassesToRecover: -1, CheckTimeout: 10 * time.Second},
			wantProblems: []string{"routes[0].watchers[0].passesToRecover: must be at least 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			levels := []Level{
				{Path: "routes[0].watchers[0]", Settings: tt.watcher},
				{Path: "strategy.watcherDefaults", Settings: tt.strategy},
				{Path: "routes[0].watcherDefaults", Settings: tt.route},
				global,
			}

			got, problems := Resolve(levels, []Bounds{routeBounds, globalBounds})
			require.Equal(t, tt.want, got)

			var messages []string

			for _, p := range problems {
				messages = append(messages, p.String())
			}

			require.Equal(t, tt.wantProblems, messages)
		})
	}
}

func TestResolveForModelInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		own          Settings
		wantProblems []string
	}{
		{name: "inside the bounds", own: Settings{Interval: time.Minute}},
		{
			name:         "a field alone in the message",
			own:          Settings{Interval: time.Second},
			wantProblems: []string{"interval: 1s is below watch.minInterval 10s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, problems := Resolve([]Level{{Settings: tt.own}}, []Bounds{{Path: "watch", MinInterval: 10 * time.Second}})

			var messages []string

			for _, p := range problems {
				messages = append(messages, p.String())
			}

			require.Equal(t, tt.wantProblems, messages)
		})
	}
}

func TestWithin(t *testing.T) {
	t.Parallel()

	global := Bounds{Path: "watch", MinInterval: 10 * time.Second, MaxInterval: 15 * time.Minute, MaxTimeout: 4 * time.Hour}

	tests := []struct {
		name  string
		inner Bounds
		want  []string
	}{
		{name: "no route bounds", inner: Bounds{Path: "routes[0].watcherLimits"}},
		{name: "inside the global bounds", inner: Bounds{Path: "routes[0].watcherLimits", MinInterval: 15 * time.Second, MaxInterval: 5 * time.Minute, MaxTimeout: 2 * time.Hour}},
		{
			name:  "a minimum below the global one",
			inner: Bounds{Path: "routes[0].watcherLimits", MinInterval: 5 * time.Second},
			want:  []string{"routes[0].watcherLimits.minInterval: 5s is below watch.minInterval 10s"},
		},
		{
			name:  "a maximum timeout above the global one",
			inner: Bounds{Path: "routes[0].watcherLimits", MaxTimeout: 5 * time.Hour},
			want:  []string{"routes[0].watcherLimits.maxTimeout: 5h0m0s is above watch.maxTimeout 4h0m0s"},
		},
		{
			name:  "a minimum above the maximum",
			inner: Bounds{Path: "routes[0].watcherLimits", MinInterval: 2 * time.Minute, MaxInterval: time.Minute},
			want:  []string{"routes[0].watcherLimits.minInterval: 2m0s is above the maximum interval 1m0s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string

			for _, p := range Within(tt.inner, global) {
				got = append(got, p.String())
			}

			require.Equal(t, tt.want, got)
		})
	}
}

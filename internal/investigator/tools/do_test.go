package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestGuardDo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		timeout     time.Duration
		cancel      bool
		fn          func(ctx context.Context) error
		wantKind    ErrorKind
		wantMessage string
		wantNot     string
	}{
		{name: "success", timeout: time.Second, fn: func(context.Context) error { return nil }},
		{
			name:        "an error is redacted",
			timeout:     time.Second,
			fn:          func(context.Context) error { return errors.New("dial ops@example.com: refused") },
			wantKind:    KindFailed,
			wantMessage: "[REDACTED:",
			wantNot:     "ops@example.com",
		},
		{
			name:        "a panic",
			timeout:     time.Second,
			fn:          func(context.Context) error { panic("password=hunter2") },
			wantKind:    KindFailed,
			wantMessage: "the call failed unexpectedly",
			wantNot:     "hunter2",
		},
		{
			name:    "a timeout",
			timeout: 10 * time.Millisecond,
			fn: func(ctx context.Context) error {
				<-ctx.Done()

				return ctx.Err()
			},
			wantKind:    KindTimeout,
			wantMessage: "the target did not answer within 10ms",
		},
		{
			name:    "the caller's context ends",
			timeout: time.Minute,
			cancel:  true,
			fn: func(ctx context.Context) error {
				<-ctx.Done()

				return ctx.Err()
			},
			wantKind:    KindFailed,
			wantMessage: "the call was cancelled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if tt.cancel {
				time.AfterFunc(10*time.Millisecond, cancel)
			}

			terr := newGuard(t).Do(ctx, "http:status.example.com", 2, tt.timeout, tt.fn)
			if tt.wantKind == "" {
				require.Nil(t, terr)

				return
			}

			require.NotNil(t, terr)
			require.Equal(t, tt.wantKind, terr.Kind)
			require.Equal(t, "http:status.example.com", terr.Source)
			require.Contains(t, terr.Message, tt.wantMessage)

			if tt.wantNot != "" {
				require.NotContains(t, terr.Message, tt.wantNot)
			}
		})
	}
}

func TestGuardDoConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		keys  []string
		calls int
		want  int64
	}{
		{name: "one key keeps its limit", limit: 2, keys: []string{"a"}, calls: 6, want: 2},
		{name: "keys have separate limits", limit: 2, keys: []string{"a", "b"}, calls: 8, want: 4},
		{name: "a limit below one means one", limit: 0, keys: []string{"a"}, calls: 3, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := newGuard(t)

			var active, peak atomic.Int64

			fn := func(context.Context) error {
				n := active.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}

				time.Sleep(20 * time.Millisecond)
				active.Add(-1)

				return nil
			}

			var wg sync.WaitGroup
			for i := range tt.calls {
				wg.Go(func() {
					if terr := g.Do(t.Context(), tt.keys[i%len(tt.keys)], tt.limit, time.Minute, fn); terr != nil {
						t.Errorf("call failed: %v", terr)
					}
				})
			}

			wg.Wait()
			require.Equal(t, tt.want, peak.Load())
		})
	}
}

func TestGuardDoCircuitBreaker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		trialErr error
		wantKind ErrorKind
	}{
		{name: "a good trial closes the circuit", wantKind: ""},
		{name: "a failed trial keeps it open", trialErr: errors.New("refused"), wantKind: KindUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := newGuard(t)

			var now atomic.Int64

			now.Store(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC).UnixNano())
			g.now = func() time.Time { return time.Unix(0, now.Load()) }

			var calls atomic.Int64

			failing := func(context.Context) error {
				calls.Add(1)

				return errors.New("refused")
			}

			for range breakerFailures {
				require.Equal(t, KindFailed, g.Do(t.Context(), "k", 2, time.Second, failing).Kind)
			}

			open := g.Do(t.Context(), "k", 2, time.Second, failing)
			require.Equal(t, KindUnavailable, open.Kind)
			require.Contains(t, open.Message, "failed 5 times in a row")
			require.Equal(t, int64(breakerFailures), calls.Load(), "the open circuit does not call fn")

			now.Add(int64(breakerOpenFor))

			trial := g.Do(t.Context(), "k", 2, time.Second, func(context.Context) error { return tt.trialErr })
			if tt.trialErr == nil {
				require.Nil(t, trial)
			} else {
				require.Equal(t, KindFailed, trial.Kind)
			}

			after := g.Do(t.Context(), "k", 2, time.Second, func(context.Context) error { return nil })
			if tt.wantKind == "" {
				require.Nil(t, after)
			} else {
				require.Equal(t, tt.wantKind, after.Kind)
			}
		})
	}
}

func TestGuardDoKeyCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// busy keys have a call in flight when the new key arrives.
		busy []string
		// idle keys finished their calls.
		idle []string
		// broken keys have an open circuit.
		broken   []string
		wantFull bool
		wantKeys []string
	}{
		{name: "idle keys are dropped for a new one", idle: []string{"a", "b"}, wantKeys: []string{"new"}},
		{name: "busy keys are kept and idle ones dropped", busy: []string{"a"}, idle: []string{"b"}, wantKeys: []string{"a", "new"}},
		{name: "keys with an open circuit are kept", broken: []string{"a"}, idle: []string{"b"}, wantKeys: []string{"a", "new"}},
		{name: "a full guard refuses the new key", busy: []string{"a"}, broken: []string{"b"}, wantFull: true, wantKeys: []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewPedanticRegistry()
			metrics, err := NewMetrics(reg)
			require.NoError(t, err)

			g, err := New(nil, Options{Redactor: newRedactor(t), Metrics: metrics})
			require.NoError(t, err)

			g.maxKeys = 2

			for _, key := range tt.idle {
				require.Nil(t, g.Do(t.Context(), key, 2, time.Second, func(context.Context) error { return nil }))
			}

			for _, key := range tt.broken {
				for range breakerFailures {
					require.NotNil(t, g.Do(t.Context(), key, 2, time.Second, func(context.Context) error { return errors.New("refused") }))
				}
			}

			release := make(chan struct{})
			started := make(chan struct{}, len(tt.busy))

			var wg sync.WaitGroup
			for _, key := range tt.busy {
				wg.Go(func() {
					if terr := g.Do(t.Context(), key, 2, time.Minute, func(context.Context) error {
						started <- struct{}{}
						<-release

						return nil
					}); terr != nil {
						t.Errorf("busy call failed: %v", terr)
					}
				})
			}

			for range tt.busy {
				<-started
			}

			terr := g.Do(t.Context(), "new", 2, time.Second, func(context.Context) error { return nil })

			close(release)
			wg.Wait()

			want := "# HELP sidekick_guard_keys_full_total Guarded calls that are not source tool calls, such as watcher HTTP probes, refused because the guard already held limits for its maximum number of targets, all in use.\n" +
				"# TYPE sidekick_guard_keys_full_total counter\n"

			if tt.wantFull {
				require.NotNil(t, terr)
				require.Equal(t, KindUnavailable, terr.Kind)
				require.Contains(t, terr.Message, "too many other targets")
				require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want+"sidekick_guard_keys_full_total 1\n"), MetricKeysFull))
			} else {
				require.Nil(t, terr)
				require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want+"sidekick_guard_keys_full_total 0\n"), MetricKeysFull))
			}

			g.mu.Lock()
			defer g.mu.Unlock()

			var keys []string
			for key := range g.keys {
				keys = append(keys, key)
			}

			require.ElementsMatch(t, tt.wantKeys, keys)
		})
	}
}

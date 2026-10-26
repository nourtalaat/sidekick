package watch

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

func TestAlertCheck(t *testing.T) {
	t.Parallel()

	groupID := uuid.MustParse("019a2b3c-0000-7000-8000-000000000001")

	tests := []struct {
		name       string
		require    Require
		states     []AlertState
		err        error
		noAlerts   bool
		wantResult Result
		wantDetail string
	}{
		{name: "all recovered", states: []AlertState{{Fingerprint: "a", Resolved: true}, {Fingerprint: "b", Resolved: true}}, wantResult: Pass, wantDetail: "2 of 2 alerts recovered"},
		{name: "all is the default", states: []AlertState{{Fingerprint: "a", Resolved: true}, {Fingerprint: "b"}}, wantResult: Fail, wantDetail: "1 of 2 alerts recovered"},
		{name: "all, one still firing", require: RequireAll, states: []AlertState{{Fingerprint: "a", Resolved: true}, {Fingerprint: "b"}}, wantResult: Fail, wantDetail: "1 of 2 alerts recovered"},
		{name: "any, one recovered", require: RequireAny, states: []AlertState{{Fingerprint: "a"}, {Fingerprint: "b", Resolved: true}}, wantResult: Pass, wantDetail: "1 of 2 alerts recovered"},
		{name: "any, none recovered", require: RequireAny, states: []AlertState{{Fingerprint: "a"}}, wantResult: Fail, wantDetail: "0 of 1 alerts recovered"},
		{name: "a group without alerts", states: nil, wantResult: Fail, wantDetail: "the group has no alerts"},
		{name: "the store fails", err: errors.New("connection reset to db.internal"), wantResult: Error, wantDetail: "cannot read the alerts of the group"},
		{name: "alert checks not set up", noAlerts: true, wantResult: Error, wantDetail: "alert checks are not set up"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			alerts := NewMockAlertStates(ctrl)

			opts := Options{Alerts: alerts}
			if tt.noAlerts {
				opts.Alerts = nil
			} else {
				alerts.EXPECT().AlertStates(gomock.Any(), groupID).Return(tt.states, tt.err)
			}

			c := newChecker(t, testDeps{opts: opts})

			o := c.Run(t.Context(), Target{GroupID: groupID}, AlertCheck{Require: tt.require})
			require.Equal(t, tt.wantResult, o.Result)
			require.Equal(t, tt.wantDetail, o.Detail)
		})
	}
}

func TestAlertCheckValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		require Require
		wantErr string
	}{
		{name: "the default"},
		{name: "all", require: RequireAll},
		{name: "any", require: RequireAny},
		{name: "unknown", require: "most", wantErr: `require: expected all or any, got "most"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := AlertCheck{Require: tt.require}.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestCheckerRunMetricsAndDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		text       string
		wantResult Result
		wantDetail func(t *testing.T, detail string)
	}{
		{
			name: "a pass is counted", text: "up", wantResult: Pass,
			wantDetail: func(t *testing.T, detail string) {
				t.Helper()
				require.Equal(t, "the text matches textRegex", detail)
			},
		},
		{
			name: "a fail is counted", text: "down", wantResult: Fail,
			wantDetail: func(t *testing.T, detail string) {
				t.Helper()
				require.Equal(t, "the text does not match textRegex", detail)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			m := source.NewMockSource(ctrl)
			m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{{Name: "status", ReadOnly: true}}, nil)
			m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Text: tt.text}, nil)

			reg := prometheus.NewPedanticRegistry()
			c := newChecker(t, testDeps{sources: []tools.Source{{Name: "k8s", Source: m}}, reg: reg})

			o := c.Run(t.Context(), Target{}, ToolCheck{Source: "k8s", Tool: "status", Condition: Condition{TextRegex: "^up$"}})
			require.Equal(t, tt.wantResult, o.Result)
			tt.wantDetail(t, o.Detail)
			require.Positive(t, o.Duration)

			require.InDelta(t, 1, testutil.ToFloat64(c.opts.Metrics.checks.WithLabelValues("tool", string(tt.wantResult))), 0)
			require.Equal(t, 1, testutil.CollectAndCount(c.opts.Metrics.duration, MetricCheckDuration))
		})
	}
}

func TestCheckerDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "one line", in: "a\nb\t c", want: "a b c"},
		{name: "redacted", in: "token for ops@example.com", want: "token for [REDACTED:"},
		{name: "cut", in: strings.Repeat("y", 2*maxDetailLen), want: strings.Repeat("y", maxDetailLen)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newChecker(t, testDeps{}).detail(tt.in)
			require.LessOrEqual(t, len(got), maxDetailLen)
			require.True(t, strings.HasPrefix(got, tt.want), got)
		})
	}
}

func TestCheckerValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		check   Check
		wantErr []string
	}{
		{name: "an allowed host", check: HTTPCheck{URL: "https://status.example.com/health"}},
		{name: "a host not allowed", check: HTTPCheck{URL: "https://other.example.com/"}, wantErr: []string{"url: the host other.example.com is not in watch.http.allowHosts"}},
		{name: "a bad URL is reported once", check: HTTPCheck{URL: "ftp://status.example.com/"}, wantErr: []string{`url: the scheme "ftp"`}},
		{name: "a check's own problems", check: HTTPCheck{URL: "https://other.example.com/", Timeout: -1}, wantErr: []string{"timeout: must not be negative", "url: the host other.example.com"}},
		{name: "a tool check", check: ToolCheck{Source: "prom"}, wantErr: []string{"tool: required", "condition: "}},
		{name: "an alert check", check: AlertCheck{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := newChecker(t, testDeps{allowHosts: []string{"status.example.com"}}).Validate(tt.check)
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.Len(t, strings.Split(err.Error(), "\n"), len(tt.wantErr), err.Error())

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    func(t *testing.T) Options
		wantErr bool
	}{
		{name: "nothing set", opts: func(*testing.T) Options { return Options{} }, wantErr: true},
		{
			name: "no HTTP client",
			opts: func(t *testing.T) Options {
				t.Helper()

				guard, err := tools.New(nil, tools.Options{Redactor: newRedactor(t)})
				require.NoError(t, err)

				return Options{Guard: guard, Redactor: newRedactor(t)}
			},
			wantErr: true,
		},
		{
			name: "everything required",
			opts: func(t *testing.T) Options {
				t.Helper()

				guard, err := tools.New(nil, tools.Options{Redactor: newRedactor(t)})
				require.NoError(t, err)

				client, err := safehttp.New(safehttp.Options{})
				require.NoError(t, err)

				return Options{Guard: guard, HTTP: client, Redactor: newRedactor(t)}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(tt.opts(t))
			if tt.wantErr {
				require.ErrorIs(t, err, errMissingOption)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a second registration fails, and nil records nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewPedanticRegistry()
			_, err := NewMetrics(reg)
			require.NoError(t, err)

			_, err = NewMetrics(reg)
			require.Error(t, err, tt.name)

			var m *Metrics

			require.NotPanics(t, func() { m.recordCheck(TypeHTTP, Pass, 0) })
		})
	}
}

func TestCheckTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check Check
		want  Type
	}{
		{check: HTTPCheck{}, want: TypeHTTP},
		{check: ToolCheck{}, want: TypeTool},
		{check: AlertCheck{}, want: TypeAlert},
	}

	for _, tt := range tests {
		t.Run(string(tt.want), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.check.Type())
		})
	}
}

// Compile-time checks that the types are checks.
var (
	_ Check = HTTPCheck{}
	_ Check = ToolCheck{}
	_ Check = AlertCheck{}
)

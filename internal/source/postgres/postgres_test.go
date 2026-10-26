package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       Config
		noSecrets bool
		wantErr   error
		wantMsg   string
	}{
		{name: "a valid config", cfg: Config{Databases: []Database{{Name: "orders", URLSecret: testURLRef}}}},
		{name: "no secrets resolver", noSecrets: true, wantErr: errNoSecrets},
		{
			name:    "settings the guard would refuse",
			cfg:     Config{Settings: source.Settings{Tools: source.ToolSettings{Rename: map[string]string{toolRunningQueries: "bad name"}}}},
			wantMsg: "tools.rename.running_queries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := registry.Deps{}
			if !tt.noSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "db", tt.cfg, deps)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				require.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)

				list, err := s.Tools(t.Context())
				require.NoError(t, err)
				require.Len(t, list, len(toolList(nil)))
				require.NoError(t, s.Close())
			}
		})
	}
}

// TestCallInput covers calls that end before any secret is read or any
// connection is made.
func TestCallInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tool          string
		input         string
		wantErr       error
		wantToolError string
	}{
		{name: "a tool that writes", tool: "terminate_backend", input: `{"database":"orders"}`, wantErr: errUnknownTool},
		{name: "an empty tool name", tool: "", input: `{}`, wantErr: errUnknownTool},
		{
			name:          "a database the config does not list",
			tool:          toolRunningQueries,
			input:         `{"database":"payroll"}`,
			wantToolError: `database "payroll" is not one Sidekick may read; known databases: orders, billing`,
		},
		{name: "input that does not decode", tool: toolRunningQueries, input: `{"database":"orders","limit":"ten"}`, wantToolError: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newUnitSource(t, Config{})

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.wantToolError)
		})
	}
}

func TestFailure(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name          string
		ctx           context.Context
		err           error
		wantToolError string
	}{
		{
			name:          "a statement timeout",
			err:           &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
			wantToolError: "the query ran longer than the statement timeout of 10s and was cancelled",
		},
		{
			name:          "a lock timeout",
			err:           &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
			wantToolError: "the query waited longer than the lock timeout of 1s",
		},
		{
			name:          "a missing privilege",
			err:           &pgconn.PgError{Code: "42501", Message: "permission denied for view pg_stat_statements"},
			wantToolError: "Postgres answered 42501: permission denied for view pg_stat_statements. The source's role needs pg_monitor",
		},
		{
			name:          "pg_stat_statements that is not loaded",
			err:           &pgconn.PgError{Code: "55000", Message: `pg_stat_statements must be loaded via "shared_preload_libraries"`},
			wantToolError: `Postgres answered 55000: pg_stat_statements must be loaded via "shared_preload_libraries"`,
		},
		{name: "a refused login", err: &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}},
		{name: "a server that is shutting down", err: &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}},
		{name: "too many connections", err: &pgconn.PgError{Code: "53300", Message: "too many clients already"}},
		{name: "a database that does not exist", err: &pgconn.PgError{Code: "3D000", Message: `database "orders" does not exist`}},
		{name: "a superuser role", err: errSuperuser},
		{name: "a call whose context ended", ctx: cancelled, err: &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := tt.ctx
			if ctx == nil {
				ctx = t.Context()
			}

			s := &Source{}

			_, err := s.failure(ctx, toolRunningQueries, errors.Join(errors.New("query"), tt.err))

			toolErr, ok := errors.AsType[*source.ToolError](err)
			if tt.wantToolError == "" {
				require.False(t, ok, "want a failure of the source, got the tool error %v", toolErr)
				require.ErrorIs(t, err, tt.err)

				return
			}

			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.wantToolError)
		})
	}
}

func TestBuildReplication(t *testing.T) {
	t.Parallel()

	lag := int64(4096)
	paused := true
	streaming := "streaming"
	replayedAt := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		state    serverState
		standbys []standby
		want     string
		wantNone bool
	}{
		{name: "a primary without standbys has nothing to show", wantNone: true},
		{
			name:     "a primary with a standby",
			standbys: []standby{{PID: 42, Application: "replica-1", State: &streaming, ReplayBehindBytes: &lag}},
			want: `{"database":"orders","serverRole":"primary","standbys":[
				{"pid":42,"application":"replica-1","state":"streaming","replayBehindBytes":4096}
			]}`,
		},
		{
			name: "a standby with paused replay",
			state: serverState{
				InRecovery: true, ReplayPaused: &paused, LastReplayAt: &replayedAt, ReplayBehindBytes: &lag, ReceiverStatus: &streaming,
			},
			want: `{"database":"orders","serverRole":"standby","standby":{
				"replayPaused":true,"lastReplayAt":"2026-05-04T10:00:00Z","replayBehindBytes":4096,"receiverStatus":"streaming"
			},"standbys":[],"note":` + mustJSON(t, replicationNote) + `}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := buildReplication(request{database: "orders"}, tt.state, tt.standbys, false)
			require.NoError(t, err)

			if tt.wantNone {
				require.True(t, res.NoData)
				require.Contains(t, res.Text, "no standby streams from it")

				return
			}

			require.False(t, res.NoData)
			require.JSONEq(t, tt.want, string(res.Structured))
			require.Len(t, res.Evidence, 1)
		})
	}
}

func TestBuildLockWaits(t *testing.T) {
	t.Parallel()

	long := "UPDATE accounts SET balance = balance - 1 WHERE id = 1 AND note = '" + strings.Repeat("x", maxQueryLen) + "'"

	tests := []struct {
		name      string
		waiting   []lockWait
		blockers  []blocker
		wantRoots []int32
		wantPIDs  []int32
		wantNote  bool
	}{
		{
			name:      "a chain of two: the root blocks a session that blocks another",
			waiting:   []lockWait{{PID: 3, BlockedBy: []int32{2}}, {PID: 2, BlockedBy: []int32{1}, Query: &long}},
			blockers:  []blocker{{PID: 1}, {PID: 2, Waiting: true}},
			wantRoots: []int32{1},
			wantPIDs:  []int32{1, 2},
		},
		{
			name:      "two sessions behind one blocker, and a prepared transaction",
			waiting:   []lockWait{{PID: 5, BlockedBy: []int32{4, 0}}, {PID: 6, BlockedBy: []int32{4}}},
			blockers:  []blocker{{PID: 4}},
			wantRoots: []int32{4},
			wantPIDs:  []int32{0, 4},
			wantNote:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pids := blockingPIDs(tt.waiting)
			require.Equal(t, tt.wantPIDs, pids)

			res := buildLockWaits(request{database: "orders", queryText: true}, tt.waiting, tt.blockers, pids[0] == preparedPID)
			require.Equal(t, tt.wantRoots, res.RootBlockers)
			require.Equal(t, tt.wantNote, res.Note != "")

			for _, w := range res.Waiting {
				if w.Query != nil {
					require.LessOrEqual(t, len(*w.Query), maxQueryLen)
				}
			}
		})
	}
}

// TestThroughTheGuard shows what the model sees: every tool with the
// source prefix, and a database the config does not list refused by the
// schema before the source sees the call.
func TestThroughTheGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "a database the config does not list", input: `{"database":"payroll"}`},
		{name: "a key the schema does not have", input: `{"database":"orders","query":"DELETE FROM accounts"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newUnitSource(t, Config{})

			r, err := redact.New(redact.Options{HashKey: []byte("test")})
			require.NoError(t, err)

			g, err := tools.New([]tools.Source{{Name: "db", Source: s}}, tools.Options{Redactor: r})
			require.NoError(t, err)

			run := g.Start(t.Context(), "inv-1", nil)

			var names []string
			for _, spec := range run.Tools() {
				names = append(names, spec.Name)
			}

			require.Equal(t, []string{"db__slow_queries", "db__running_queries", "db__lock_waits", "db__replication_lag"}, names)

			res := run.Call(t.Context(), model.ToolCall{ID: "call-1", Name: "db__running_queries", Input: json.RawMessage(tt.input)})
			require.True(t, res.ToolResult.IsError)
		})
	}
}

// newUnitSource returns a source with the databases orders and billing and
// a secrets resolver that expects no call, so a test that reaches the
// network fails.
func newUnitSource(t *testing.T, cfg Config) *Source {
	t.Helper()

	cfg.Databases = []Database{{Name: "orders", URLSecret: testURLRef}, {Name: "billing", URLSecret: testURLRef}}

	s, err := New(t.Context(), "db", cfg, registry.Deps{Secrets: registry.NewMockSecrets(gomock.NewController(t))})
	require.NoError(t, err)

	built, ok := s.(*Source)
	require.True(t, ok)

	t.Cleanup(func() { require.NoError(t, built.Close()) })

	return built
}

// mustJSON returns v as JSON.
func mustJSON(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return string(data)
}

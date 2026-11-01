//go:build integration

package gate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/gate"
	_ "github.com/nourtalaat/sidekick/internal/intake/alertmanager" // Intake adapter "alertmanager", which the sample scenario uses.
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/strategies"
)

const (
	// repoRoot is the root of the repository, from this package.
	repoRoot = "../.."
	// strategyName is the strategy the tests propose a change to.
	strategyName = "checkout-latency"
	// baselineBody is version 1 of the strategy.
	baselineBody = "name: checkout-latency\ndescription: Look at deploys first.\n"
	// betterBody is a proposal the scripted model treats like the baseline.
	betterBody = "name: checkout-latency\ndescription: Look at deploys and at the connection pool first.\n"
	// worseMarker is in the description of a proposal that makes the
	// scripted model give a wrong verdict.
	worseMarker = "SKIP-THE-DEPLOYS"
	// worseBody is a proposal the scripted model gets wrong.
	worseBody = "name: checkout-latency\ndescription: " + worseMarker + ". Do not look at deploys.\n"
	// testRepeats is how many times the tests run each scenario in each arm.
	testRepeats = 2
	// testScenarios is how many scenarios cover the strategy in the tests.
	testScenarios = 3
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// scriptedConfig is the config of the scripted model provider.
type scriptedConfig struct {
	model.Settings `yaml:",inline"`
}

// configYAML is the config the tests parse. Its connector is the scripted
// one the tests register.
const configYAML = `
database:
  urlSecret: { name: db, key: url }
model:
  connectors:
    scripted: { provider: scripted, model: claude-opus-5-5 }
  roles:
    investigate: scripted
routes:
  - name: default
`

// dispatcher is a model connector that investigates the sample scenario: it
// calls two tools, then submits a verdict. The verdict is right unless the
// strategy in the prompt holds worseMarker. It counts the investigations it
// starts, and can fail from a given one on.
type dispatcher struct {
	mu      sync.Mutex
	started int
	// failFrom, when above zero, makes every call of the investigation with
	// that number or a higher one fail as an overloaded provider.
	failFrom int
	// calls counts every call, the judge's included.
	calls int
	// onStart, when set, is called with the number of each investigation as
	// it starts.
	onStart func(n int)
}

var _ model.Connector = (*dispatcher)(nil)

// Capabilities says the dispatcher calls tools.
func (d *dispatcher) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalling: true}
}

// Generate answers the first turn of an investigation with tool calls and the
// second with the verdict.
func (d *dispatcher) Generate(_ context.Context, req model.Request) (model.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++

	assistant := 0

	for _, m := range req.Messages {
		if m.Role == model.Assistant {
			assistant++
		}
	}

	if assistant == 0 {
		d.started++

		if d.onStart != nil {
			d.onStart(d.started)
		}
	}

	if d.failFrom > 0 && d.started >= d.failFrom {
		return model.Response{}, &model.Error{Kind: model.ErrOverloaded, Err: errors.New("overloaded")}
	}

	usage := model.Usage{InputTokens: 1000, OutputTokens: 200}

	if assistant == 0 {
		return modeltest.ToolUse(usage,
			modeltest.Call("c1", "github__list_commits", `{"repo": "acme/checkout", "since": "2026-05-04T06:00:00Z"}`),
			modeltest.Call("c2", "github__get_commit", `{"repo": "acme/checkout", "ref": "abc123"}`),
		).Response, nil
	}

	return modeltest.ToolUse(usage, modeltest.Call("v1", report.SubmitVerdictTool, verdictJSON(worse(req)))).Response, nil
}

// investigations returns how many investigations the dispatcher has started.
func (d *dispatcher) investigations() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.started
}

// modelCalls returns how many calls the dispatcher has answered.
func (d *dispatcher) modelCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.calls
}

// worse reports whether the strategy of the prompt is the worse one.
func worse(req model.Request) bool {
	for _, s := range req.System {
		if strings.Contains(s.Text, worseMarker) {
			return true
		}
	}

	return false
}

// verdictJSON returns the report of a run: a true positive that cites both
// tool calls, or an inconclusive one when bad is set.
func verdictJSON(bad bool) string {
	if bad {
		return `{"verdict": "inconclusive", "confidence": "low", "reason": "Nothing shows the cause.", "summary": "Not clear.",
			"findings": [{"claim": "No deploy was checked.", "evidenceToolCallIds": ["c1"]}]}`
	}

	return `{"verdict": "true_positive", "confidence": "high",
		"reason": "Latency rose right after commit abc123 shrank the DB pool.",
		"summary": "The DB pool ran out after abc123.", "cause": "Commit abc123 lowered the pool size to 5.",
		"impact": "Checkout p99 is above 2s.",
		"findings": [{"claim": "abc123 lowered the pool size to 5 at 10:05.", "evidenceToolCallIds": ["c1", "c2"]}],
		"fixes": [{"title": "Restore the pool size", "detail": "Set pool_size back to 20 or roll back abc123."}]}`
}

// envOptions change the environment of a test.
type envOptions struct {
	// scenarios are the scenarios the gate chooses from. Nil means
	// testScenarios scenarios of the strategy.
	scenarios []bench.Scenario
	// maxCostUSD is bench.gate.maxCostUSD. Zero means 100 dollars.
	maxCostUSD float64
	// daily are the daily caps.
	daily gate.Daily
	// failFrom is the dispatcher's failFrom.
	failFrom int
}

// env is a database, a scripted model and a gate on them.
type env struct {
	pool *pgxpool.Pool
	url  string
	conn *dispatcher
	gate *gate.Gate
	// set holds the strategies in effect, and cfg and regs are what the gate
	// runs with.
	set  *strategy.Set
	cfg  *config.Config
	regs config.Registries
	// scenarios are the scenarios the gate chooses from.
	scenarios []bench.Scenario
}

// newEnv returns an environment with a gate.
func newEnv(t *testing.T, opts envOptions) *env {
	t.Helper()

	url := storetest.NewURL(t)

	pool, err := store.Open(t.Context(), url, 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	conn := &dispatcher{failFrom: opts.failFrom}

	models := registry.New[model.Connector]("model")
	models.Register("scripted", registry.Typed(func(context.Context, string, scriptedConfig, registry.Deps) (model.Connector, error) {
		return conn, nil
	}))

	regs := config.Registries{Models: models}

	cfg, err := config.Parse(t.Context(), []byte(configYAML), config.LoadOptions{Registries: regs})
	require.NoError(t, err)

	cfg.Bench.Gate.MinRepeats = testRepeats
	cfg.Bench.Gate.MaxCostUSD = opts.maxCostUSD

	if opts.maxCostUSD == 0 {
		cfg.Bench.Gate.MaxCostUSD = 100
	}

	base, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	saved, problems := strategy.ParseRuntime([]byte(baselineBody), 1)
	require.Empty(t, problems)

	scenarios := opts.scenarios
	if scenarios == nil {
		scenarios = coveringScenarios(t, testScenarios)
	}

	set := base.WithRuntime([]strategy.Strategy{saved})

	g, err := gate.New(gate.Options{
		Pool:        pool,
		Config:      cfg,
		Registries:  regs,
		DatabaseURL: func(context.Context) (string, error) { return url, nil },
		Strategies:  set,
		Scenarios:   gate.ScenarioFunc(func(context.Context) ([]bench.Scenario, error) { return scenarios, nil }),
		Daily:       opts.daily,
	})
	require.NoError(t, err)

	return &env{pool: pool, url: url, conn: conn, gate: g, set: set, cfg: cfg, regs: regs, scenarios: scenarios}
}

// coveringScenarios returns n copies of the sample scenario that name the
// strategy, with no rubric so no judge is called.
func coveringScenarios(t *testing.T, n int) []bench.Scenario {
	t.Helper()

	loaded, err := bench.LoadScenarios(repoRoot+"/bench/scenarios/sample/checkout-pool-size.yaml", bench.LoadOptions{BaseDir: repoRoot})
	require.NoError(t, err)
	require.Len(t, loaded, 1)

	out := make([]bench.Scenario, n)

	for i := range out {
		sc := loaded[0]
		sc.ID = sc.ID + "-" + string(rune('a'+i))
		sc.Strategy = strategyName
		sc.Expected.RootCause.Rubric = nil
		out[i] = sc
	}

	return out
}

// insertProposal adds a pending proposal that proposes body, and returns its
// ID.
func insertProposal(t *testing.T, pool *pgxpool.Pool, body string) uuid.UUID {
	t.Helper()

	id := uuid.New()

	_, err := pool.Exec(t.Context(), `INSERT INTO strategy_proposals
		(id, name, base_version, base_origin, base_body, body, rationale, trigger_count, status, created_at, updated_at)
		VALUES ($1, $2, 'runtime:1', 'runtime', $3, $4, 'Because', 3, 'pending', now(), now())`, id, strategyName, baselineBody, body)
	require.NoError(t, err)

	return id
}

// jobFor returns the bench_gate job of a proposal.
func jobFor(t *testing.T, id uuid.UUID) db.Job {
	t.Helper()

	payload, err := json.Marshal(proposals.BenchGatePayload{ProposalID: id})
	require.NoError(t, err)

	return db.Job{ID: 1, Kind: "bench_gate", Payload: payload}
}

// loadProposal reads a proposal.
func loadProposal(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) db.StrategyProposal {
	t.Helper()

	p, err := db.New(pool).GetStrategyProposal(t.Context(), id)
	require.NoError(t, err)

	return p
}

// detailOf decodes the gate detail of a proposal.
func detailOf(t *testing.T, p db.StrategyProposal) gate.Detail {
	t.Helper()

	var d gate.Detail

	require.NoError(t, json.Unmarshal(p.GateDetail, &d))

	return d
}

// count returns the number of rows a query counts.
func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()

	var n int

	require.NoError(t, pool.QueryRow(t.Context(), query, args...).Scan(&n))

	return n
}

// benchSchemas returns the schemas the benchmark made and left.
func benchSchemas(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT nspname FROM pg_namespace WHERE nspname LIKE $1 ORDER BY nspname", bench.SchemaPrefix+"%")
	require.NoError(t, err)

	defer rows.Close()

	var out []string

	for rows.Next() {
		var s string

		require.NoError(t, rows.Scan(&s))

		out = append(out, s)
	}

	require.NoError(t, rows.Err())

	return slices.Clone(out)
}

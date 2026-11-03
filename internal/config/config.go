package config

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/lessons"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// Defaults for settings that are not set in the file.
const (
	DefaultListen        = ":8080"
	DefaultMetricsListen = ":9090"
	DefaultMaxConns      = 20
	DefaultMigrations    = MigrationsStartup
	DefaultHMACTolerance = 5 * time.Minute
	DefaultRoutePriority = 100
	DefaultMaxToolCalls  = 40
	DefaultMaxWallTime   = 10 * time.Minute
	DefaultConcurrency   = 8
	// DefaultGroupingWindow is how long a group stays open after its
	// newest event.
	DefaultGroupingWindow = 30 * time.Minute
	DefaultLogLevel       = LogLevelInfo
	// DefaultStrategy is the strategy of a route that names none.
	DefaultStrategy = strategy.GenericAlert
	// DefaultProposalWrongThreshold is how many ratings of wrong make a
	// strategy version get a proposal.
	DefaultProposalWrongThreshold = 3
	// DefaultProposalWindow is how far back those ratings are counted.
	DefaultProposalWindow = 14 * Day
	// MinGateRepeats is the fewest times a gate runs each scenario. The
	// config cannot set bench.gate.minRepeats lower.
	MinGateRepeats = 5
	// DefaultGateMaxDrop is the default of bench.gate.maxDrop.
	DefaultGateMaxDrop = 0.02
	// DefaultGateMaxCostUSD is the default of bench.gate.maxCostUSD.
	DefaultGateMaxCostUSD = 5.0
)

// DefaultRouteName is the name of the route that takes every event no other
// route matches. It is always evaluated last.
const DefaultRouteName = "default"

// microDollarsPerUSD converts prices in config, given in US dollars, to the
// micro-dollars model.Price uses.
const microDollarsPerUSD = 1_000_000

// Config is the content of sidekick.yaml. Load returns it decoded, with
// defaults applied, and checked.
type Config struct {
	Server     Server             `yaml:"server"`
	Database   Database           `yaml:"database"`
	Secrets    Secrets            `yaml:"secrets"`
	Model      Model              `yaml:"model"`
	Sources    map[string]Section `yaml:"sources"`
	Intake     Intake             `yaml:"intake"`
	Slack      *Slack             `yaml:"slack"`
	Teams      *Teams             `yaml:"teams"`
	OIDC       *OIDC              `yaml:"oidc"`
	Outputs    map[string]Section `yaml:"outputs"`
	Actions    Actions            `yaml:"actions"`
	Validator  MonitorValidator   `yaml:"validator"`
	Strategies Strategies         `yaml:"strategies"`
	Bench      Bench              `yaml:"bench"`
	Routes     []Route            `yaml:"routes"`
	Budgets    Budgets            `yaml:"budgets"`
	Report     Report             `yaml:"report"`
	Redaction  Redaction          `yaml:"redaction"`
	Telemetry  Telemetry          `yaml:"telemetry"`
	Watch      Watch              `yaml:"watch"`
	Memory     Memory             `yaml:"memory"`
	Lessons    Lessons            `yaml:"lessons"`
	Retention  Retention          `yaml:"retention"`

	// Listeners are the file listeners, by name. Their events go through
	// the Slack app or the Teams bot.
	Listeners map[string]Listener `yaml:"listeners"`

	// strategySet holds the built-in and file strategies, loaded by Parse.
	strategySet *strategy.Set

	// registries are the plugin registries Parse checked the file with, so
	// that routes saved at runtime are checked against the same plugin
	// types. The zero value means the global registries.
	registries Registries
}

// Server holds the HTTP listen addresses.
type Server struct {
	// PublicURL is the address users reach Sidekick at, for links in
	// Slack. Empty turns those links off.
	PublicURL string `yaml:"publicURL"`
	// Listen serves the API, the UI and webhooks.
	Listen string `yaml:"listen"`
	// MetricsListen serves /metrics, /healthz and /readyz.
	MetricsListen string `yaml:"metricsListen"`
}

// Database is the Postgres connection.
type Database struct {
	// URLSecret holds the connection URL.
	URLSecret registry.SecretRef `yaml:"urlSecret"`
	// MaxConns is the size of the connection pool.
	MaxConns int `yaml:"maxConns"`
	// Migrations says when the schema is migrated.
	Migrations MigrationMode `yaml:"migrations"`
}

// MigrationMode says when the database schema is migrated.
type MigrationMode string

// Migration modes. The Helm chart's job mode, which migrates in a
// pre-upgrade hook Job, runs Sidekick itself in manual mode.
const (
	// MigrationsStartup migrates at start-up. When several replicas start
	// at once, one migrates and the others wait for it.
	MigrationsStartup MigrationMode = "startup"
	// MigrationsManual never migrates; an operator runs `sidekick migrate
	// up`. Until the schema is current, the replica is not ready.
	MigrationsManual MigrationMode = "manual"
)

// Secrets says where secret files are mounted.
type Secrets struct {
	// Dir holds one directory per secret and one file per key.
	Dir string `yaml:"dir"`
}

// Model holds the model connectors and the roles they fill.
type Model struct {
	// Connectors are named connector instances.
	Connectors map[string]Connector `yaml:"connectors"`
	// Roles maps each role to a connector name. Routes can override it.
	Roles map[model.Role]string `yaml:"roles"`
	// Prices override the shipped price table, by model ID.
	Prices map[string]Price `yaml:"prices"`
}

// Price is the cost of one model, in US dollars per million tokens.
type Price struct {
	InputPerMTok      float64 `yaml:"inputPerMTok"`
	OutputPerMTok     float64 `yaml:"outputPerMTok"`
	CacheReadPerMTok  float64 `yaml:"cacheReadPerMTok"`
	CacheWritePerMTok float64 `yaml:"cacheWritePerMTok"`
}

// Intake holds the webhook endpoints.
type Intake struct {
	Webhooks map[string]Webhook `yaml:"webhooks"`
}

// Actions holds the actions Sidekick may propose, and how proposals are
// handled. Besides the two settings, every key is the name of an action
// instance, such as monitor-pr. The action's type decodes the rest of its
// section, for example its credentials and limits. Names are lowercase,
// so they never collide with the settings.
type Actions struct {
	// ProposalTTL is how long a proposal waits for a decision before it
	// expires.
	ProposalTTL time.Duration `yaml:"proposalTTL"`
	// MaxPerInvestigation is the most proposals one investigation may
	// make, decided or not.
	MaxPerInvestigation int `yaml:"maxPerInvestigation"`
	// Definitions are the action instances, by name.
	Definitions map[string]Section `yaml:",inline"`
}

// Strategies says where file strategies are read from.
type Strategies struct {
	// Dir holds file strategies, one YAML file each. They are read, never
	// written. Empty, or a directory that does not exist, means only the
	// built-in strategies are used.
	Dir string `yaml:"dir"`
	// Proposals says when wrong feedback leads to a proposed change to a
	// strategy.
	Proposals StrategyProposals `yaml:"proposals"`
}

// StrategyProposals says when a strategy version gets a proposed change. A
// proposal costs one summarize call to draft and at least twice
// bench.gate.minRepeats benchmark runs of every scenario that covers the
// strategy to check, so it is off unless enabled.
type StrategyProposals struct {
	// Enabled turns proposals on. Off, nothing is counted, drafted or run,
	// and nothing is spent.
	Enabled bool `yaml:"enabled"`
	// WrongThreshold is how many ratings of wrong on the verdict or a
	// finding, on investigations that ran one version of a strategy, make
	// that version get a proposal.
	WrongThreshold int `yaml:"wrongThreshold"`
	// Window is how far back the ratings are counted.
	Window Period `yaml:"window"`
}

// Bench holds the settings of the benchmark that run inside Sidekick, as
// opposed to those of bench.yaml, which `sidekick bench run` reads.
type Bench struct {
	// Gate sets the benchmark gate that checks a proposed strategy change.
	Gate BenchGate `yaml:"gate"`
}

// BenchGate sets the benchmark gate: it runs the scenarios that cover a
// strategy with its current version and with a proposed change, and lets
// the proposal pass only when it is not clearly worse.
type BenchGate struct {
	// MinRepeats is how many times each scenario runs in each arm. It
	// cannot be lower than MinGateRepeats.
	MinRepeats int `yaml:"minRepeats"`
	// MaxDrop is how far below the current version the lower end of the 95%
	// interval of the proposal's score difference may be and still pass.
	MaxDrop float64 `yaml:"maxDrop"`
	// MaxCostUSD caps what one gate may spend on benchmark runs and
	// grading. The gate stops and fails with over_budget when the next run
	// would pass it.
	MaxCostUSD float64 `yaml:"maxCostUSD"`
	// PublicDir is a directory with the public benchmark scenarios, which
	// join the reviewed scenarios from the database. Empty means the gate
	// uses the reviewed scenarios alone.
	PublicDir string `yaml:"publicDir"`
	// BaseDir is the directory the tool snapshot paths in PublicDir's
	// scenarios are relative to, the root of a Sidekick checkout. Empty
	// means the working directory.
	BaseDir string `yaml:"baseDir"`
}

// Route decides how matching events are investigated and where the report
// goes (RT-1, RT-2).
type Route struct {
	Name string `yaml:"name"`
	// Priority orders routes, lower first; ties are ordered by name.
	Priority int `yaml:"priority"`
	// Match selects events. The default route has none.
	Match *Match `yaml:"match"`
	// Strategy names the investigation strategy, built-in or from
	// strategies.dir. A route that names none uses DefaultStrategy.
	Strategy string `yaml:"strategy"`
	// Roles override model.roles for this route.
	Roles map[model.Role]string `yaml:"roles"`
	// Sources lists the sources the investigation may use.
	Sources []string `yaml:"sources"`
	// Mode is the action mode: read-only; approved, where the model may
	// propose the route's actions and a person approves each one; or
	// read-write, where a proposal of a trusted event runs without one.
	Mode action.Mode `yaml:"mode"`
	// AllowUntrustedWrite lets a read-write route select intakes and
	// listeners that are not trusted, and keeps read-write mode for their
	// events (ACT-6). Sidekick logs a warning for it on every start.
	AllowUntrustedWrite bool `yaml:"allowUntrustedWrite"`
	// Actions lists the actions the route may propose, by their names in
	// the actions section. It is empty in read-only mode.
	Actions []string `yaml:"actions"`
	// Outputs lists where reports go (OUT-5).
	Outputs []string `yaml:"outputs"`
	// Details are fixed facts about the route, such as the owning team or
	// the environment. The prompt shows them to the model as trusted text,
	// so they hold no secrets and nothing copied from an alert. CheckRouteDetails
	// has the limits.
	Details map[string]string `yaml:"details"`
	// Budgets limit one investigation and the route's daily spending.
	// After Load, unset limits hold the global values from
	// budgets.perInvestigation.
	Budgets RouteBudgets `yaml:"budgets"`
	// Concurrency is the most investigations of this route that run at
	// once across every replica. Zero means only budgets.concurrency holds
	// them.
	Concurrency int `yaml:"concurrency"`
	// RateLimit limits how many of the route's events are routed in a
	// window. Nil means only budgets.rateLimit holds them.
	RateLimit *RateLimit `yaml:"rateLimit"`
	// Grouping decides which of the route's events join one group and one
	// investigation (RT-3).
	Grouping Grouping `yaml:"grouping"`
	// WatcherDefaults are the defaults of the route's watchers, including
	// those the model starts. Zero values come from watch.defaults.
	WatcherDefaults strategy.WatcherSettings `yaml:"watcherDefaults"`
	// Watchers start when a group of the route opens for a matching event.
	Watchers []strategy.Watcher `yaml:"watchers"`
	// WatcherLimits limit what start_watcher may use on this route, and
	// bound the settings of every watcher of the route.
	WatcherLimits WatcherLimits `yaml:"watcherLimits"`
}

// WatcherLimits are what start_watcher may use on a route, and the route's
// bounds of watcher settings, inside the global ones.
type WatcherLimits struct {
	// AllowModel set to false turns start_watcher off for the route. Unset
	// follows watch.allowModel.
	AllowModel *bool `yaml:"allowModel"`
	// Types are the check types start_watcher may use. Empty allows every
	// type.
	Types []watch.Type `yaml:"types"`
	// Tools are the qualified names of the tools tool checks may call,
	// such as signoz__query_metrics. Empty allows none.
	Tools []string `yaml:"tools"`
	// Hosts are the host patterns HTTP checks may reach, each inside
	// watch.http.allowHosts. Empty means watch.http.allowHosts.
	Hosts       []string      `yaml:"hosts"`
	MinInterval time.Duration `yaml:"minInterval"`
	MaxInterval time.Duration `yaml:"maxInterval"`
	MaxTimeout  time.Duration `yaml:"maxTimeout"`
}

// Grouping decides which events join an open group.
type Grouping struct {
	// Window is how long a group stays open after its newest event. An
	// event with the same fingerprint that arrives within it joins the
	// group.
	Window time.Duration `yaml:"window"`
	// FingerprintLabels are the labels whose values, with the intake and
	// the alert name, make the fingerprint of an alert. Empty means the
	// fingerprint the intake gave the alert, which covers all its labels.
	FingerprintLabels []string `yaml:"fingerprintLabels"`
	// RelatedWindow lets an event join an open group of the same service,
	// or of a service it depends on or that depends on it, when that
	// group's newest event is at most this old. Dependencies come from the
	// service catalog sources. Zero, the default, turns it off.
	RelatedWindow time.Duration `yaml:"relatedWindow"`
}

// Match selects events for a route. Every field that is set must match.
type Match struct {
	// Intake lists webhook endpoint and listener names, and
	// SlackMentionIntake for mentions.
	Intake []string `yaml:"intake"`
	// Labels must all be present with these values.
	Labels map[string]string `yaml:"labels"`
	// Service is a glob in path.Match syntax.
	Service string `yaml:"service"`
	// Severity lists accepted severities.
	Severity []event.Severity `yaml:"severity"`
	// Text is a regular expression matched against the event text.
	Text string `yaml:"text"`
}

// Budgets hold the global investigation limits. They are the defaults for
// routes and the ceilings route budgets must stay within (CFG-1).
type Budgets struct {
	PerInvestigation Limits `yaml:"perInvestigation"`
	// Daily is the global daily spending cap and the time zone of every
	// daily cap.
	Daily DailyBudget `yaml:"daily"`
	// Concurrency is the most investigations that run at once across every
	// replica.
	Concurrency int `yaml:"concurrency"`
	// RateLimit limits how many events are routed in a window, across
	// every route. Nil means no global limit.
	RateLimit *RateLimit `yaml:"rateLimit"`
}

// Limits bound one investigation.
type Limits struct {
	// MaxTokens counts the input tokens, including those read from or
	// written to the prompt cache, and the output tokens of the whole run.
	MaxTokens    int           `yaml:"maxTokens"`
	MaxToolCalls int           `yaml:"maxToolCalls"`
	MaxWallTime  time.Duration `yaml:"maxWallTime"`
	// MaxCostUSD is the most the run's model calls may cost, priced with
	// the price table.
	MaxCostUSD float64 `yaml:"maxCostUSD"`
}

// Report holds the rules applied to submitted verdicts.
type Report struct {
	// MinFalsePositiveConfidence is the lowest confidence a false_positive
	// verdict may have. Empty means report.DefaultMinFalsePositiveConfidence.
	MinFalsePositiveConfidence report.Confidence `yaml:"minFalsePositiveConfidence"`
}

// EmbeddingsName names the embeddings connector of vector memory in error
// messages, metric labels and traces.
const EmbeddingsName = "memory-embeddings"

// Memory says how prompts use memory: the investigations people
// confirmed.
type Memory struct {
	// Enabled shows similar confirmed investigations in each prompt. It is
	// on unless set to false.
	Enabled bool `yaml:"enabled"`
	// Examples is the most similar investigations a prompt shows.
	Examples int `yaml:"examples"`
	// Vector turns on search by meaning next to full-text search. It is
	// off unless enabled.
	Vector Vector `yaml:"vector"`
}

// Lessons says how lessons are approved and chosen for prompts: the
// guidance that people reviewed.
type Lessons struct {
	// RequireApproval makes a lesson drafted from feedback wait for an
	// admin to approve it. It is on unless set to false; with it off, a
	// drafted lesson is used at once.
	RequireApproval bool `yaml:"requireApproval"`
	// MaxPerPrompt is the most lessons a prompt shows.
	MaxPerPrompt int `yaml:"maxPerPrompt"`
	// MaxTokens is the most tokens the lessons of a prompt may use.
	MaxTokens int `yaml:"maxTokens"`
	// GlobalShare is the share, from 0 to 1, of the places and the tokens
	// that lessons without a scope may use.
	GlobalShare float64 `yaml:"globalShare"`
}

// Vector configures vector memory: memory entries are embedded with a
// model, and a prompt's examples and memory__search merge the entries
// nearest in meaning with the full-text matches. It needs a Postgres with
// the pgvector extension.
type Vector struct {
	// Enabled turns vector memory on.
	Enabled bool `yaml:"enabled"`
	// Embeddings is the connector that embeds text, a model connector
	// section of its own: the provider, the embedding model, its
	// dimensions and the credentials. It is required when enabled.
	Embeddings Connector `yaml:"embeddings"`

	// Dimensions is the length of the vectors, read from Embeddings when
	// the config is validated. It is zero when vector memory is off.
	Dimensions int `yaml:"-"`
}

// PromptExamples returns how many examples a prompt shows: zero when
// memory is off.
func (m Memory) PromptExamples() int {
	if !m.Enabled {
		return 0
	}

	return m.Examples
}

// Redaction configures what is removed from event text and tool results.
type Redaction struct {
	// BuiltIn names the built-in rules to use.
	BuiltIn []string `yaml:"builtIn"`
	// Custom are named regular expressions.
	Custom []RedactionRule `yaml:"custom"`
	// HashKeySecret is the key of the HMAC that makes placeholders stable.
	HashKeySecret *registry.SecretRef `yaml:"hashKeySecret"`
}

// RedactionRule is one custom redaction rule.
type RedactionRule struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
}

// Watch holds the settings of watchers.
type Watch struct {
	// AllowModel offers investigations the start_watcher tool. It is off
	// unless set; routes can still turn it off.
	AllowModel bool `yaml:"allowModel"`
	// MaxPerGroup is the most watchers one group may have.
	MaxPerGroup int `yaml:"maxPerGroup"`
	// MaxTotal is the most watchers that run at once in the deployment.
	MaxTotal int `yaml:"maxTotal"`
	// Defaults are used when no route, strategy or watcher sets a value.
	// After Load, the values the file leaves out hold the built-in
	// defaults.
	Defaults strategy.WatcherSettings `yaml:"defaults"`
	// MinInterval, MaxInterval and MaxTimeout are the global bounds.
	MinInterval time.Duration `yaml:"minInterval"`
	MaxInterval time.Duration `yaml:"maxInterval"`
	MaxTimeout  time.Duration `yaml:"maxTimeout"`
	HTTP        WatchHTTP     `yaml:"http"`
}

// WatchHTTP holds the rules of watcher HTTP checks. Only the config file
// sets them.
type WatchHTTP struct {
	// AllowHosts are the hosts HTTP checks may reach: exact names or IP
	// addresses, or "*." and a name for every name below it. Empty allows
	// none.
	AllowHosts []string `yaml:"allowHosts"`
	// AllowPrivate are private or special address ranges HTTP checks may
	// reach anyway, in CIDR form or as single addresses.
	AllowPrivate []string `yaml:"allowPrivate"`
}

// Telemetry configures traces, metrics and logs.
type Telemetry struct {
	// OTLPEndpoint receives traces. Empty turns trace export off.
	OTLPEndpoint string `yaml:"otlpEndpoint"`
	// LogLevel is the lowest level logged.
	LogLevel LogLevel `yaml:"logLevel"`
	// ServiceLabel adds the service label to metrics.
	ServiceLabel bool `yaml:"serviceLabel"`
}

// LogLevel is a log level name.
type LogLevel string

// Log levels.
const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// Registries are the plugin registries a config's types are looked up in.
// A nil field means the global registry of that kind, so the zero value is
// what the binary uses; tests pass their own registries.
type Registries struct {
	Models  *registry.Registry[model.Connector]
	Sources *registry.Registry[source.Source]
	Intakes *registry.Registry[intake.Adapter]
	Outputs *registry.Registry[output.Sink]
	Actions *registry.Registry[action.Action]
}

// LoadOptions change how a config is checked.
type LoadOptions struct {
	// Registries are where plugin types are looked up.
	Registries Registries
	// CheckSecrets also checks that every referenced secret file exists and
	// is readable. Start-up always sets it; config validate sets it with
	// --check-secrets.
	CheckSecrets bool
	// Secrets resolves references when CheckSecrets is set. Nil means the
	// files under the config's secrets.dir.
	Secrets registry.Secrets
	// Logger gets a line for each file strategy that replaces a built-in
	// one. Nil logs nothing.
	Logger *slog.Logger
}

// WithDefaults returns r with each nil registry replaced by the global one.
func (r Registries) WithDefaults() Registries {
	return Registries{
		Models:  cmp.Or(r.Models, registry.Models),
		Sources: cmp.Or(r.Sources, registry.Sources),
		Intakes: cmp.Or(r.Intakes, registry.Intakes),
		Outputs: cmp.Or(r.Outputs, registry.Outputs),
		Actions: cmp.Or(r.Actions, registry.Actions),
	}
}

// RoutesInOrder returns the routes in the order they are matched: by
// priority, lower first, then by name, with the default route last.
func (c *Config) RoutesInOrder() []Route {
	routes := slices.Clone(c.Routes)
	slices.SortStableFunc(routes, func(a, b Route) int {
		if (a.Name == DefaultRouteName) != (b.Name == DefaultRouteName) {
			if a.Name == DefaultRouteName {
				return 1
			}

			return -1
		}

		if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
			return c
		}

		return cmp.Compare(a.Name, b.Name)
	})

	return routes
}

// DatabaseURL reads the Postgres URL from database.urlSecret through s, or
// from the files under secrets.dir when s is nil. The URL holds the
// password, so it is never written to the file or passed as a flag.
func (c *Config) DatabaseURL(ctx context.Context, s registry.Secrets) (string, error) {
	if s == nil {
		s = secrets.NewDir(c.Secrets.Dir)
	}

	url, err := secrets.ResolveText(ctx, s, c.Database.URLSecret)
	if err != nil {
		return "", fmt.Errorf("read database.urlSecret: %w", err)
	}

	return url, nil
}

// StrategySet returns the strategies routes can use: the built-in ones and
// those from strategies.dir. It is set on configs returned by Load and
// Parse, and nil on others.
func (c *Config) StrategySet() *strategy.Set {
	return c.strategySet
}

// WithStrategySet returns a copy of the config whose strategies are set. A
// benchmark uses it to run a version of a strategy that is not the one in
// effect.
func (c *Config) WithStrategySet(set *strategy.Set) Config {
	out := *c
	out.strategySet = set

	return out
}

// PriceTable returns the price overrides in micro-dollars, by model ID.
func (m Model) PriceTable() map[string]model.Price {
	out := make(map[string]model.Price, len(m.Prices))
	for id, p := range m.Prices {
		out[id] = p.ModelPrice()
	}

	return out
}

// ModelPrice converts the price to micro-dollars per million tokens.
func (p Price) ModelPrice() model.Price {
	return model.Price{
		InputPerMTok:      toMicroDollars(p.InputPerMTok),
		OutputPerMTok:     toMicroDollars(p.OutputPerMTok),
		CacheReadPerMTok:  toMicroDollars(p.CacheReadPerMTok),
		CacheWritePerMTok: toMicroDollars(p.CacheWritePerMTok),
	}
}

// RouteConcurrency returns the concurrency limit of each route that sets
// one, by route name.
func (c *Config) RouteConcurrency() map[string]int {
	out := map[string]int{}

	for _, r := range c.Routes {
		if r.Concurrency > 0 {
			out[r.Name] = r.Concurrency
		}
	}

	return out
}

// Rules returns the verdict rules for report.Report.Validate.
func (r Report) Rules() report.Rules {
	return report.Rules{MinFalsePositiveConfidence: r.MinFalsePositiveConfidence}
}

// Level returns the log level for log/slog.
func (t Telemetry) Level() slog.Level {
	switch t.LogLevel {
	case LogLevelDebug:
		return slog.LevelDebug
	case LogLevelWarn:
		return slog.LevelWarn
	case LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Load reads and checks the config file at path. On problems in the file
// the error wraps Errors, which lists every problem with its field path.
func Load(ctx context.Context, path string, opts LoadOptions) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The operator chooses which config file to read.
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg, err := Parse(ctx, data, opts)
	if err != nil {
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}

	return cfg, nil
}

// Parse decodes and checks a config file's content. On problems in the
// content the error wraps Errors, which lists every problem with its field
// path; get it with errors.As.
func Parse(ctx context.Context, data []byte, opts LoadOptions) (*Config, error) {
	doc, err := parseDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	var errs errorList

	cfg := &Config{}
	cfg.setDefaults()

	d := decoder{errs: &errs}
	d.decode(doc, reflect.ValueOf(cfg).Elem(), "")

	v := newValidator(ctx, cfg, opts)
	v.run()

	var invalid Errors
	if errors.As(v.errs.err(), &invalid) {
		for _, e := range invalid {
			if !d.underFailed(e.Path) {
				errs.addAll(Errors{e})
			}
		}
	}

	if err := errs.err(); err != nil {
		return nil, err
	}

	cfg.resolveBudgets()
	cfg.Watch.Defaults = watch.Complete(cfg.Watch.Defaults)
	cfg.strategySet = v.strategies
	cfg.registries = v.regs

	return cfg, nil
}

// setDefaults sets the defaults of every section, so sections missing from
// the file have them too.
func (c *Config) setDefaults() {
	c.Server.setDefaults()
	c.Database.setDefaults()
	c.Secrets.setDefaults()
	c.Budgets.setDefaults()
	c.Telemetry.setDefaults()
	c.Actions.setDefaults()
	c.Validator.setDefaults()
	c.Watch.setDefaults()
	c.Memory.setDefaults()
	c.Lessons.setDefaults()
	c.Retention.setDefaults()
	c.Strategies.setDefaults()
	c.Bench.setDefaults()
}

// resolveBudgets fills each route's unset limits with the global ones.
func (c *Config) resolveBudgets() {
	for i := range c.Routes {
		resolveRouteBudgets(&c.Routes[i].Budgets, c.Budgets)
	}
}

func (s *Server) setDefaults() {
	s.Listen = DefaultListen
	s.MetricsListen = DefaultMetricsListen
}

func (d *Database) setDefaults() {
	d.MaxConns = DefaultMaxConns
	d.Migrations = DefaultMigrations
}

func (s *Secrets) setDefaults() {
	s.Dir = secrets.DefaultDir
}

func (r *Route) setDefaults() {
	r.Priority = DefaultRoutePriority
	r.Mode = action.ReadOnly
	r.Strategy = DefaultStrategy
	r.Grouping.Window = DefaultGroupingWindow
}

func (b *Budgets) setDefaults() {
	b.PerInvestigation = Limits{
		MaxTokens:    DefaultMaxTokens,
		MaxToolCalls: DefaultMaxToolCalls,
		MaxWallTime:  DefaultMaxWallTime,
		MaxCostUSD:   DefaultMaxCostUSD,
	}
	b.Daily.setDefaults()
	b.Concurrency = DefaultConcurrency
}

func (t *Telemetry) setDefaults() {
	t.LogLevel = DefaultLogLevel
}

func (a *Actions) setDefaults() {
	a.ProposalTTL = action.DefaultProposalTTL
	a.MaxPerInvestigation = action.DefaultMaxPerInvestigation
}

func (w *Watch) setDefaults() {
	w.MaxPerGroup = watch.DefaultMaxPerGroup
	w.MaxTotal = watch.DefaultMaxTotal
	w.MinInterval = watch.DefaultMinInterval
	w.MaxInterval = watch.DefaultMaxInterval
	w.MaxTimeout = watch.DefaultMaxTimeout
}

func (s *Strategies) setDefaults() {
	s.Proposals.WrongThreshold = DefaultProposalWrongThreshold
	s.Proposals.Window = Period(DefaultProposalWindow)
}

func (b *Bench) setDefaults() {
	b.Gate.MinRepeats = MinGateRepeats
	b.Gate.MaxDrop = DefaultGateMaxDrop
	b.Gate.MaxCostUSD = DefaultGateMaxCostUSD
}

func (m *Memory) setDefaults() {
	m.Enabled = true
	m.Examples = memory.DefaultExamples
}

func (l *Lessons) setDefaults() {
	l.RequireApproval = true
	l.MaxPerPrompt = lessons.DefaultMaxPerPrompt
	l.MaxTokens = lessons.DefaultMaxTokens
	l.GlobalShare = lessons.DefaultGlobalShare
}

// parseDocument parses data as exactly one YAML document.
func parseDocument(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Errors{{Message: "the file is empty"}}
		}

		return nil, Errors{{Message: "invalid YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}}
	}

	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, Errors{{Message: "the file must hold one YAML document"}}
	}

	return &doc, nil
}

// toMicroDollars converts US dollars to micro-dollars, rounded to the
// nearest one.
func toMicroDollars(usd float64) int64 {
	return int64(math.Round(usd * microDollarsPerUSD))
}

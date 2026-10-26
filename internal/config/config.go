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
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Defaults for settings that are not set in the file.
const (
	DefaultListen        = ":8080"
	DefaultMetricsListen = ":9090"
	DefaultMaxConns      = 20
	DefaultHMACTolerance = 5 * time.Minute
	DefaultRoutePriority = 100
	DefaultMaxToolCalls  = 40
	DefaultMaxWallTime   = 10 * time.Minute
	DefaultLogLevel      = LogLevelInfo
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
	Outputs    map[string]Section `yaml:"outputs"`
	Strategies Strategies         `yaml:"strategies"`
	Routes     []Route            `yaml:"routes"`
	Budgets    Budgets            `yaml:"budgets"`
	Report     Report             `yaml:"report"`
	Redaction  Redaction          `yaml:"redaction"`
	Telemetry  Telemetry          `yaml:"telemetry"`
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
}

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

// Slack holds the Slack credentials. M0 posts with the bot token only.
type Slack struct {
	BotTokenSecret registry.SecretRef `yaml:"botTokenSecret"`
}

// Strategies says where file strategies are read from.
type Strategies struct {
	Dir string `yaml:"dir"`
}

// Route decides how matching events are investigated and where the report
// goes (RT-1, RT-2).
type Route struct {
	Name string `yaml:"name"`
	// Priority orders routes, lower first; ties are ordered by name.
	Priority int `yaml:"priority"`
	// Match selects events. The default route has none.
	Match *Match `yaml:"match"`
	// Strategy names the investigation strategy.
	Strategy string `yaml:"strategy"`
	// Roles override model.roles for this route.
	Roles map[model.Role]string `yaml:"roles"`
	// Sources lists the sources the investigation may use.
	Sources []string `yaml:"sources"`
	// Mode is the action mode. Only read-only exists until M4.
	Mode action.Mode `yaml:"mode"`
	// Actions lists allowed actions. It is empty until M4.
	Actions []string `yaml:"actions"`
	// Outputs lists where reports go (OUT-5).
	Outputs []string `yaml:"outputs"`
	// Budgets limit one investigation. After Load, unset limits hold the
	// global values from budgets.perInvestigation.
	Budgets Limits `yaml:"budgets"`
}

// Match selects events for a route. Every field that is set must match.
type Match struct {
	// Intake lists webhook endpoint names.
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
}

// Limits bound one investigation. M0 limits tool calls and wall time.
type Limits struct {
	MaxToolCalls int           `yaml:"maxToolCalls"`
	MaxWallTime  time.Duration `yaml:"maxWallTime"`
}

// Report holds the rules applied to submitted verdicts.
type Report struct {
	// MinFalsePositiveConfidence is the lowest confidence a false_positive
	// verdict may have. Empty means report.DefaultMinFalsePositiveConfidence.
	MinFalsePositiveConfidence report.Confidence `yaml:"minFalsePositiveConfidence"`
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
// content the error is Errors, which lists every problem with its field
// path.
func Parse(ctx context.Context, data []byte, opts LoadOptions) (*Config, error) {
	doc, err := parseDocument(data)
	if err != nil {
		return nil, err
	}

	var errs errorList

	cfg := &Config{}
	cfg.setDefaults()

	d := decoder{errs: &errs}
	d.decode(doc, reflect.ValueOf(cfg).Elem(), "")

	var invalid Errors
	if errors.As(Validate(ctx, cfg, opts), &invalid) {
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
}

// resolveBudgets fills each route's unset limits with the global ones.
func (c *Config) resolveBudgets() {
	global := c.Budgets.PerInvestigation
	for i := range c.Routes {
		b := &c.Routes[i].Budgets
		if b.MaxToolCalls == 0 {
			b.MaxToolCalls = global.MaxToolCalls
		}

		if b.MaxWallTime == 0 {
			b.MaxWallTime = global.MaxWallTime
		}
	}
}

func (s *Server) setDefaults() {
	s.Listen = DefaultListen
	s.MetricsListen = DefaultMetricsListen
}

func (d *Database) setDefaults() {
	d.MaxConns = DefaultMaxConns
}

func (s *Secrets) setDefaults() {
	s.Dir = secrets.DefaultDir
}

func (r *Route) setDefaults() {
	r.Priority = DefaultRoutePriority
	r.Mode = action.ReadOnly
}

func (b *Budgets) setDefaults() {
	b.PerInvestigation = Limits{MaxToolCalls: DefaultMaxToolCalls, MaxWallTime: DefaultMaxWallTime}
}

func (t *Telemetry) setDefaults() {
	t.LogLevel = DefaultLogLevel
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

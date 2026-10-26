package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/strategies"
)

// namePattern is the rule for connector, source, output, webhook endpoint
// and route names. Source names become part of tool names, and webhook
// names part of the URL path /hooks/<name>, so they are limited to plain
// characters and a short length.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// nameRule explains namePattern in error messages.
const nameRule = `must start with a lower-case letter and hold only lower-case letters, digits and "-", at most 24 characters`

// slackOutputType is the output type that posts with the bot token from
// the slack section.
const slackOutputType = "slack"

// The values config accepts for enumerations, for checks and suggestions.
var (
	knownRoles      = []model.Role{model.RoleTriage, model.RoleInvestigate, model.RoleJudge, model.RoleSummarize}
	knownModes      = []action.Mode{action.ReadOnly, action.Approved, action.ReadWrite}
	knownSeverities = []event.Severity{event.SeverityUnknown, event.SeverityInfo, event.SeverityWarning, event.SeverityCritical}
	knownLogLevels  = []LogLevel{LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError}
	knownMigrations = []MigrationMode{MigrationsStartup, MigrationsManual}
	knownConfidence = []report.Confidence{report.Low, report.Medium, report.High}
)

// Validate checks cfg against every rule and returns Errors listing all the
// problems, or nil. Besides the config's own rules it runs each plugin
// section's checks through the registries (strict decoding and the plugin's
// Validate method) without building any instance, so it needs no secrets
// and makes no network calls. It reads the strategy files in
// strategies.dir, to check them and the strategy each route names. With
// opts.CheckSecrets it also checks that every referenced secret file exists
// and is readable.
func Validate(ctx context.Context, cfg *Config, opts LoadOptions) error {
	v := newValidator(ctx, cfg, opts)
	v.run()

	return v.errs.err()
}

// validator holds the state of one Validate call.
type validator struct {
	ctx  context.Context
	cfg  *Config
	opts LoadOptions
	regs Registries
	errs *errorList
	// caps are the capabilities connector configs declare, by connector.
	caps map[string]model.Capabilities
	// refs are the secret references found in decoded plugin sections.
	refs []found[registry.SecretRef]
	// strategies are the built-in and file strategies, or nil when they
	// could not be loaded.
	strategies *strategy.Set
}

// run runs every check, recording the problems in v.errs.
func (v *validator) run() {
	v.checkServer()
	v.checkDatabase()
	v.checkSecretsDir()
	v.checkConnectors()
	v.checkRoles("model.roles", v.cfg.Model.Roles, true)
	v.checkPrices()
	v.checkSources()
	v.checkWebhooks()
	v.checkSlack()
	v.checkOutputs()
	v.loadStrategies()
	v.checkRoutes()
	v.checkBudgets()
	v.checkReport()
	v.checkRedaction()
	v.checkTelemetry()
	v.checkSecretRefs()

	if v.opts.CheckSecrets {
		v.checkSecretFiles(cmp.Or[registry.Secrets](v.opts.Secrets, secrets.NewDir(v.cfg.Secrets.Dir)))
	}
}

// loadStrategies loads the built-in strategies and those in strategies.dir,
// and records every problem in the files at strategies.dir.
func (v *validator) loadStrategies() {
	set, err := strategy.Load(strategies.FS, v.cfg.Strategies.Dir, v.opts.Logger)
	if err == nil {
		v.strategies = set

		return
	}

	var problems strategy.Errors
	if !errors.As(err, &problems) {
		v.errs.add("strategies.dir", "%v", err)

		return
	}

	for _, p := range problems {
		v.errs.add("strategies.dir", "%s", p)
	}
}

func (v *validator) checkServer() {
	s := v.cfg.Server
	if s.PublicURL != "" && !isHTTPURL(s.PublicURL) {
		v.errs.add("server.publicURL", "expected an absolute http or https URL, got %q", s.PublicURL)
	}

	v.checkListen("server.listen", s.Listen)
	v.checkListen("server.metricsListen", s.MetricsListen)
}

func (v *validator) checkListen(path, addr string) {
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
		v.errs.add(path, `expected host:port such as ":8080", got %q`, addr)
	}
}

func (v *validator) checkDatabase() {
	if v.cfg.Database.URLSecret == (registry.SecretRef{}) {
		v.errs.add("database.urlSecret", "required")
	}

	v.positive("database.maxConns", v.cfg.Database.MaxConns)

	if m := v.cfg.Database.Migrations; !slices.Contains(knownMigrations, m) {
		v.errs.add("database.migrations", "expected startup or manual, got %q%s", m, didYouMean(string(m), asStrings(knownMigrations)))
	}
}

func (v *validator) checkSecretsDir() {
	if v.cfg.Secrets.Dir == "" {
		v.errs.add("secrets.dir", "required")
	}
}

// checkConnectors checks each model connector, and records the capabilities
// of those whose config declares them.
func (v *validator) checkConnectors() {
	for _, name := range sortedKeys(v.cfg.Model.Connectors) {
		c := v.cfg.Model.Connectors[name]
		v.checkName(c.Path(), name)

		decoded, ok := checkPlugin(v, v.regs.Models, "model provider", providerKey, name, c.Section)
		if !ok {
			continue
		}

		declarer, ok := decoded.(model.CapabilityDeclarer)
		if !ok {
			continue
		}

		caps := declarer.Capabilities()
		v.caps[name] = caps

		for _, s := range findAll[model.Settings](decoded, c.Path()) {
			v.errs.addAll(fieldErrors(s.Path, s.Value.Validate(caps)))
		}
	}
}

// checkRoles checks a role map: known roles, existing connectors, and tool
// calling for the investigate role where capabilities are declared. The
// global map must set investigate.
func (v *validator) checkRoles(path string, roles map[model.Role]string, global bool) {
	if global && roles[model.RoleInvestigate] == "" {
		v.errs.add(joinPath(path, string(model.RoleInvestigate)), "required")
	}

	for _, role := range sortedKeys(roles) {
		rolePath := joinPath(path, string(role))
		if !slices.Contains(knownRoles, role) {
			v.errs.add(rolePath, "unknown role%s", didYouMean(string(role), asStrings(knownRoles)))

			continue
		}

		connector := roles[role]
		if !v.reference(rolePath, "connector", connector, slices.Collect(maps.Keys(v.cfg.Model.Connectors))) {
			continue
		}

		if caps, ok := v.caps[connector]; ok {
			checkRoleCaps(v.errs, rolePath, role, connector, caps)
		}
	}
}

func (v *validator) checkPrices() {
	for _, id := range sortedKeys(v.cfg.Model.Prices) {
		p := v.cfg.Model.Prices[id]
		base := joinPath("model.prices", id)

		for key, value := range map[string]float64{
			"inputPerMTok":      p.InputPerMTok,
			"outputPerMTok":     p.OutputPerMTok,
			"cacheReadPerMTok":  p.CacheReadPerMTok,
			"cacheWritePerMTok": p.CacheWritePerMTok,
		} {
			if value < 0 {
				v.errs.add(joinPath(base, key), "must not be negative")
			}
		}
	}
}

// checkSources checks each source section through its plugin, then the
// shared source settings of configs that embed source.Settings, which need
// the source name for the qualified tool names.
func (v *validator) checkSources() {
	for _, name := range sortedKeys(v.cfg.Sources) {
		s := v.cfg.Sources[name]
		v.checkName(s.Path(), name)

		decoded, ok := checkPlugin(v, v.regs.Sources, "source type", typeKey, name, s)
		if !ok {
			continue
		}

		if c, ok := decoded.(source.Configured); ok {
			v.errs.addAll(fieldErrors(s.Path(), c.SourceSettings().Check(name)))
		}
	}
}

func (v *validator) checkWebhooks() {
	for _, name := range sortedKeys(v.cfg.Intake.Webhooks) {
		w := v.cfg.Intake.Webhooks[name]
		v.checkName(w.Path(), name)
		checkPlugin(v, v.regs.Intakes, "intake adapter", adapterKey, name, w.Section)

		authPath := joinPath(w.Path(), "auth")

		switch {
		case w.Auth.Bearer != nil && w.Auth.HMAC != nil:
			v.errs.add(authPath, "set exactly one of bearer and hmac, not both")
		case w.Auth.Bearer == nil && w.Auth.HMAC == nil:
			v.errs.add(authPath, "set bearer or hmac; every webhook endpoint must authenticate its deliveries")
		}

		if w.Auth.Tolerance <= 0 {
			v.errs.add(joinPath(authPath, "tolerance"), "must be positive")
		}
	}
}

func (v *validator) checkSlack() {
	if v.cfg.Slack != nil && v.cfg.Slack.BotTokenSecret == (registry.SecretRef{}) {
		v.errs.add("slack.botTokenSecret", "required")
	}
}

func (v *validator) checkOutputs() {
	for _, name := range sortedKeys(v.cfg.Outputs) {
		s := v.cfg.Outputs[name]
		v.checkName(s.Path(), name)
		checkPlugin(v, v.regs.Outputs, "output type", typeKey, name, s)

		if s.Type == slackOutputType && v.cfg.Slack == nil {
			v.errs.add(s.Path(), "type slack needs slack.botTokenSecret")
		}
	}
}

// checkRoutes checks every route and that exactly one default route exists.
func (v *validator) checkRoutes() {
	seen := map[string]string{}
	defaults := 0

	for i, r := range v.cfg.Routes {
		p := fmt.Sprintf("routes[%d]", i)
		v.checkRouteName(p, r.Name, seen)

		if r.Name == DefaultRouteName {
			defaults++

			if r.Match != nil {
				v.errs.add(p+".match", "the default route must not have a match")
			}
		}

		v.checkRoute(p, r)
	}

	if defaults == 0 {
		v.errs.add("routes", "a route named %q is required; it takes every event no other route matches", DefaultRouteName)
	}
}

func (v *validator) checkRouteName(path, name string, seen map[string]string) {
	namePath := path + ".name"

	switch {
	case name == "":
		v.errs.add(namePath, "required")
	case !namePattern.MatchString(name):
		v.errs.add(namePath, "route name %q %s", name, nameRule)
	case seen[name] != "":
		v.errs.add(namePath, "route %q is already defined at %s", name, seen[name])
	default:
		seen[name] = path
	}
}

func (v *validator) checkRoute(path string, r Route) {
	if r.Match != nil {
		v.checkMatch(path+".match", *r.Match)
	}

	v.checkRoles(path+".roles", r.Roles, false)
	v.references(path+".sources", "source", r.Sources, slices.Collect(maps.Keys(v.cfg.Sources)))
	v.references(path+".outputs", "output", r.Outputs, slices.Collect(maps.Keys(v.cfg.Outputs)))

	if v.strategies != nil {
		v.reference(path+".strategy", "strategy", r.Strategy, v.strategies.Names())
	}

	switch {
	case r.Mode == action.ReadOnly:
	case slices.Contains(knownModes, r.Mode):
		v.errs.add(path+".mode", "mode %q is not available yet: routes are read-only until action modes arrive in M4", r.Mode)
	default:
		v.errs.add(path+".mode", "unknown mode %q%s", r.Mode, didYouMean(string(r.Mode), asStrings(knownModes)))
	}

	if len(r.Actions) > 0 {
		v.errs.add(path+".actions", "must be empty: routes are read-only until action modes arrive in M4")
	}

	v.checkRouteBudgets(path+".budgets", r.Budgets)
}

func (v *validator) checkMatch(at string, m Match) {
	v.references(at+".intake", "intake", m.Intake, slices.Collect(maps.Keys(v.cfg.Intake.Webhooks)))

	if m.Service != "" {
		if _, err := path.Match(m.Service, ""); err != nil {
			v.errs.add(at+".service", "invalid glob %q: %v", m.Service, err)
		}
	}

	for i, s := range m.Severity {
		if !slices.Contains(knownSeverities, s) {
			v.errs.add(fmt.Sprintf("%s.severity[%d]", at, i), "unknown severity %q%s", s, didYouMean(string(s), asStrings(knownSeverities)))
		}
	}

	if m.Text != "" {
		if _, err := regexp.Compile(m.Text); err != nil {
			v.errs.add(at+".text", "invalid regular expression: %v", err)
		}
	}
}

// checkRouteBudgets checks that a route's limits are within the global
// ceilings. Zero means the route uses the global value.
func (v *validator) checkRouteBudgets(path string, b Limits) {
	ceiling := v.cfg.Budgets.PerInvestigation

	switch {
	case b.MaxToolCalls < 0:
		v.errs.add(path+".maxToolCalls", "must not be negative")
	case ceiling.MaxToolCalls > 0 && b.MaxToolCalls > ceiling.MaxToolCalls:
		v.errs.add(path+".maxToolCalls", "%d is above the global ceiling budgets.perInvestigation.maxToolCalls %d", b.MaxToolCalls, ceiling.MaxToolCalls)
	}

	switch {
	case b.MaxWallTime < 0:
		v.errs.add(path+".maxWallTime", "must not be negative")
	case ceiling.MaxWallTime > 0 && b.MaxWallTime > ceiling.MaxWallTime:
		v.errs.add(path+".maxWallTime", "%s is above the global ceiling budgets.perInvestigation.maxWallTime %s", b.MaxWallTime, ceiling.MaxWallTime)
	}
}

func (v *validator) checkBudgets() {
	b := v.cfg.Budgets.PerInvestigation
	v.positive("budgets.perInvestigation.maxToolCalls", b.MaxToolCalls)
	v.positiveDuration("budgets.perInvestigation.maxWallTime", b.MaxWallTime)
}

func (v *validator) checkReport() {
	c := v.cfg.Report.MinFalsePositiveConfidence
	if c != "" && !slices.Contains(knownConfidence, c) {
		v.errs.add("report.minFalsePositiveConfidence", "unknown confidence %q%s", c, didYouMean(string(c), asStrings(knownConfidence)))
	}
}

func (v *validator) checkRedaction() {
	builtIn := redact.BuiltInRuleNames()

	for i, name := range v.cfg.Redaction.BuiltIn {
		if !slices.Contains(builtIn, name) {
			v.errs.add(fmt.Sprintf("redaction.builtIn[%d]", i), "unknown built-in rule %q%s", name, didYouMean(name, builtIn))
		}
	}

	seen := map[string]bool{}

	for i, rule := range v.cfg.Redaction.Custom {
		p := fmt.Sprintf("redaction.custom[%d]", i)

		switch {
		case rule.Name == "":
			v.errs.add(p+".name", "required")
		case seen[rule.Name]:
			v.errs.add(p+".name", "rule %q is defined more than once", rule.Name)
		default:
			if err := redact.CheckRuleName(rule.Name); err != nil {
				v.errs.add(p+".name", "%v", err)
			}
		}

		seen[rule.Name] = true

		if rule.Pattern == "" {
			v.errs.add(p+".pattern", "required")
		} else if _, err := regexp.Compile(rule.Pattern); err != nil {
			v.errs.add(p+".pattern", "invalid regular expression: %v", err)
		}
	}
}

func (v *validator) checkTelemetry() {
	t := v.cfg.Telemetry
	if t.OTLPEndpoint != "" && !isHTTPURL(t.OTLPEndpoint) {
		v.errs.add("telemetry.otlpEndpoint", "expected an absolute URL such as http://otel-collector:4317, got %q", t.OTLPEndpoint)
	}

	if !slices.Contains(knownLogLevels, t.LogLevel) {
		v.errs.add("telemetry.logLevel", "unknown log level %q%s", t.LogLevel, didYouMean(string(t.LogLevel), asStrings(knownLogLevels)))
	}
}

// checkSecretRefs checks the shape of every secret reference that is set,
// in the core sections and in the decoded plugin sections. Whether a
// reference is required is up to the section that holds it.
func (v *validator) checkSecretRefs() {
	for _, ref := range v.allRefs() {
		v.errs.addAll(fieldErrors(ref.Path, secrets.CheckRef(ref.Value)))
	}
}

// checkSecretFiles resolves every secret reference that is set and reports
// the ones that cannot be read. It never reports secret values.
func (v *validator) checkSecretFiles(s registry.Secrets) {
	for _, ref := range v.allRefs() {
		if secrets.CheckRef(ref.Value) != nil {
			continue
		}

		_, err := s.Resolve(v.ctx, ref.Value)

		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			v.errs.add(ref.Path, "cannot read secret %s/%s: the file does not exist", ref.Value.Name, ref.Value.Key)
		case errors.Is(err, fs.ErrPermission):
			v.errs.add(ref.Path, "cannot read secret %s/%s: permission denied", ref.Value.Name, ref.Value.Key)
		default:
			v.errs.add(ref.Path, "cannot read secret %s/%s: %v", ref.Value.Name, ref.Value.Key, err)
		}
	}
}

// allRefs returns the secret references that are set, in path order.
func (v *validator) allRefs() []found[registry.SecretRef] {
	all := append(findAll[registry.SecretRef](v.cfg, ""), v.refs...)
	all = slices.DeleteFunc(all, func(f found[registry.SecretRef]) bool { return f.Value == (registry.SecretRef{}) })
	slices.SortStableFunc(all, func(a, b found[registry.SecretRef]) int { return comparePaths(a.Path, b.Path) })

	return all
}

// references checks that every name in a list refers to something defined.
func (v *validator) references(path, kind string, names, defined []string) {
	for i, name := range names {
		v.reference(fmt.Sprintf("%s[%d]", path, i), kind, name, defined)
	}
}

// reference checks that name refers to something defined, and reports
// whether it does.
func (v *validator) reference(path, kind, name string, defined []string) bool {
	if slices.Contains(defined, name) {
		return true
	}

	if name == "" {
		v.errs.add(path, "required")
	} else {
		v.errs.add(path, "%s %q is not defined%s", kind, name, didYouMean(name, defined))
	}

	return false
}

// checkName checks the name of a connector, source, output or webhook
// endpoint.
func (v *validator) checkName(path, name string) {
	if !namePattern.MatchString(name) {
		v.errs.add(path, "name %q %s", name, nameRule)
	}
}

func (v *validator) positive(path string, n int) {
	if n <= 0 {
		v.errs.add(path, "must be positive")
	}
}

func (v *validator) positiveDuration(path string, d time.Duration) {
	if d <= 0 {
		v.errs.add(path, "must be positive")
	}
}

// withDefaults fills nil registries with the global ones.
func (r Registries) withDefaults() Registries {
	return Registries{
		Models:  cmp.Or(r.Models, registry.Models),
		Sources: cmp.Or(r.Sources, registry.Sources),
		Intakes: cmp.Or(r.Intakes, registry.Intakes),
		Outputs: cmp.Or(r.Outputs, registry.Outputs),
	}
}

// newValidator returns a validator for one check of cfg.
func newValidator(ctx context.Context, cfg *Config, opts LoadOptions) *validator {
	return &validator{
		ctx:  ctx,
		cfg:  cfg,
		opts: opts,
		regs: opts.Registries.withDefaults(),
		caps: map[string]model.Capabilities{},
		errs: &errorList{},
	}
}

// checkPlugin checks one plugin section: that its type is set and
// registered, and the plugin's own checks through reg.Check. It records the
// secret references in the decoded config and returns the decoded config,
// and whether every check passed.
func checkPlugin[T any](v *validator, reg *registry.Registry[T], kind, typeName, name string, s Section) (any, bool) {
	typePath := joinPath(s.Path(), typeName)
	if s.Type == "" {
		v.errs.add(typePath, "required")

		return nil, false
	}

	if !slices.Contains(reg.Types(), s.Type) {
		v.errs.add(typePath, "%s %q is not registered%s", kind, s.Type, didYouMean(s.Type, reg.Types()))

		return nil, false
	}

	decoded, err := reg.Check(v.ctx, s.Type, name, s)
	if err != nil {
		v.errs.addAll(pluginErrors(s.Path(), err))

		return nil, false
	}

	v.refs = append(v.refs, findAll[registry.SecretRef](decoded, s.Path())...)

	return decoded, true
}

// checkRoleCaps checks that a connector with caps can fill role.
func checkRoleCaps(errs *errorList, path string, role model.Role, connector string, caps model.Capabilities) {
	if err := model.CheckRole(role, caps); err != nil {
		if errors.Is(err, model.ErrNoToolCalling) {
			errs.add(path, "connector %q cannot call tools, which the %s role needs", connector, role)
		} else {
			errs.add(path, "connector %q cannot fill this role: %v", connector, err)
		}
	}
}

// pluginErrors turns an error from a plugin's check or build into Errors.
// Errors from strict decoding already carry their paths. Errors from the
// decode and validate stages are split into lines, and a line that starts
// with a field's key path and a colon, as Validate methods write them, is
// placed at that field. Constructor errors and anything else stay at the
// section's path.
func pluginErrors(path string, err error) Errors {
	var errs Errors
	if errors.As(err, &errs) {
		return errs
	}

	var stageErr *registry.StageError
	if !errors.As(err, &stageErr) {
		return Errors{{Path: path, Message: err.Error()}}
	}

	if stageErr.Stage == registry.StageConstruct {
		return Errors{{Path: path, Message: "cannot build: " + stageErr.Err.Error()}}
	}

	return fieldErrors(path, stageErr.Err)
}

// fieldPrefix matches a line that starts with a field's key path, such as
// "thinking.mode: ...".
var fieldPrefix = regexp.MustCompile(`^((?:[A-Za-z_][A-Za-z0-9_-]*|\[[^\]]+\])(?:\.[A-Za-z_][A-Za-z0-9_-]*|\[[^\]]+\])*): (.+)$`)

// fieldErrors turns err, one problem per line, into Errors below path,
// moving a line's leading field key path into the error's path.
func fieldErrors(path string, err error) Errors {
	if err == nil {
		return nil
	}

	var out Errors

	for line := range strings.SplitSeq(err.Error(), "\n") {
		m := fieldPrefix.FindStringSubmatch(line)
		if m == nil {
			out = append(out, Error{Path: path, Message: line})

			continue
		}

		field := m[1]
		if !strings.HasPrefix(field, "[") && path != "" {
			field = "." + field
		}

		out = append(out, Error{Path: path + field, Message: m[2]})
	}

	return out
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func sortedKeys[K ~string, V any](m map[K]V) []K {
	return slices.Sorted(maps.Keys(m))
}

func asStrings[S ~string](values []S) []string {
	out := make([]string, len(values))
	for i, s := range values {
		out[i] = string(s)
	}

	return out
}

package slackcmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// verbSet is the only verb of /sidekick route.
const verbSet = "set"

// noneValue clears a list or match field.
const noneValue = "none"

// routeField is a route field /sidekick route set can change.
type routeField struct {
	name  string
	value string
	set   func(r *rules.Route, value string) error
}

// routeFields are the fields /sidekick route set can change. Connectors,
// action allowlists and other modes are set in the config file or, later,
// the web UI.
var routeFields = []routeField{
	{name: "priority", value: "<number>, lower is tried first", set: setPriority},
	{name: "enabled", value: "true or false", set: setEnabled},
	{name: "mode", value: "read-only, the only mode until action modes arrive", set: setMode},
	{name: "strategy", value: "<strategy name>", set: setStrategy},
	{name: "sources", value: "<source>,<source> or none", set: setSources},
	{name: "outputs", value: "<output>,<output> or none", set: setOutputs},
	{name: "intake", value: "<intake>,<intake> or none, for match.intake", set: setIntake},
	{name: "service", value: "<glob> or none, for match.service", set: setService},
	{name: "severity", value: "<severity>,<severity> or none, for match.severity", set: setSeverity},
	{name: "labels", value: "<name>=<value>,... or none, for match.labels", set: setLabels},
	{name: "text", value: "<regex> or none, for match.text", set: setText},
	{name: "maxToolCalls", value: "<number>, 0 for the global value", set: setMaxToolCalls},
	{name: "maxTokens", value: "<number>, 0 for the global value", set: setMaxTokens},
	{name: "maxCostUSD", value: "<dollars> such as 2.50, 0 for the global value", set: setMaxCostUSD},
	{name: "maxWallTime", value: "<duration> such as 5m, 0 for the global value", set: setMaxWallTime},
	{name: "window", value: "<duration> such as 30m, for grouping.window", set: setWindow},
	{name: "relatedWindow", value: "<duration> such as 15m, 0 to turn it off, for grouping.relatedWindow", set: setRelatedWindow},
}

const routeSetUsage = "Usage: `/sidekick route set <name> <field> <value>`"

// route runs /sidekick route set.
func (h *Handler) route(ctx context.Context, cmd slack.SlashCommand, args []string) (string, error) {
	if len(args) == 0 || !strings.EqualFold(args[0], verbSet) {
		return "", &userError{msg: routeSetUsage + "\n\n" + fieldsText()}
	}

	if err := h.require(ctx, cmd, roles.Admin); err != nil {
		return "", err
	}

	if len(args) < 4 {
		return "", &userError{msg: routeSetUsage + "\n\n" + fieldsText()}
	}

	name, fieldName, value := args[1], args[2], strings.Join(args[3:], " ")

	i := slices.IndexFunc(routeFields, func(f routeField) bool { return strings.EqualFold(f.name, fieldName) })
	if i < 0 {
		return "", &userError{msg: fmt.Sprintf("%s is not a field Slack can set.\n\n%s", code(fieldName), fieldsText())}
	}

	field := routeFields[i]

	res, err := h.store.SetRoute(ctx, rules.SlackUser(cmd.UserID), name, func(r *rules.Route) error { return field.set(r, value) })

	switch {
	case errors.Is(err, rules.ErrFileRule):
		return "", &userError{msg: fmt.Sprintf("Route %s is set in the Sidekick config file; only a config change can alter it.", code(name))}
	case err != nil:
		return "", fmt.Errorf("set %s of route %s: %w", field.name, name, err)
	case !res.Changed:
		return fmt.Sprintf("Route %s already has %s %s; nothing changed.", code(name), field.name, code(value)), nil
	}

	verb := "updated"
	if res.Created {
		verb = "added"
	}

	h.log.InfoContext(ctx, "runtime route "+verb, "route", name, "field", field.name, "user", cmd.UserID)

	return fmt.Sprintf("Route %s %s: %s is now %s. Every replica uses it within seconds.\n%s",
		code(name), verb, field.name, code(value), describeRoute(res.New.Config, res.New.Enabled)), nil
}

// listRoutes lists the routes in effect, in the order they are tried, and
// the runtime ones that are not.
func (h *Handler) listRoutes(ctx context.Context, cmd slack.SlashCommand) (string, error) {
	if err := h.require(ctx, cmd, roles.Viewer); err != nil {
		return "", err
	}

	e := h.rules.Current()

	lines := make([]string, 0, len(e.Routes))
	for i, r := range e.Routes {
		lines = append(lines, fmt.Sprintf("%d. %s (%s, priority %d): %s", i+1, code(r.Config.Name), layerText(r.Layer), r.Config.Priority, describeRoute(r.Config, true)))
	}

	return "*Routes*, in the order they are tried\n" + strings.Join(lines, "\n") + skippedText(e, rules.KindRoute), nil
}

// describeRoute says what a route matches and how it investigates.
func describeRoute(r config.Route, enabled bool) string {
	var parts []string

	if m := r.Match; m != nil {
		if len(m.Intake) > 0 {
			parts = append(parts, "intake "+codeList(m.Intake))
		}

		if m.Service != "" {
			parts = append(parts, "service "+code(m.Service))
		}

		if len(m.Severity) > 0 {
			parts = append(parts, "severity "+codeList(asStrings(m.Severity)))
		}

		for _, k := range slices.Sorted(maps.Keys(m.Labels)) {
			parts = append(parts, "label "+code(k+"="+m.Labels[k]))
		}

		if m.Text != "" {
			parts = append(parts, "text "+code(m.Text))
		}
	}

	matches := "every event"
	if len(parts) > 0 {
		matches = strings.Join(parts, ", ")
	}

	out := fmt.Sprintf("matches %s; strategy %s, mode %s", matches, code(r.Strategy), code(string(r.Mode)))

	if len(r.Sources) > 0 {
		out += ", sources " + codeList(r.Sources)
	}

	if len(r.Outputs) > 0 {
		out += ", outputs " + codeList(r.Outputs)
	}

	if !enabled {
		out += "; turned off"
	}

	return out
}

func fieldsText() string {
	lines := make([]string, len(routeFields))
	for i, f := range routeFields {
		lines[i] = fmt.Sprintf("• %s: %s", code(f.name), escape(f.value))
	}

	return "Fields:\n" + strings.Join(lines, "\n")
}

func setPriority(r *rules.Route, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return &userError{msg: fmt.Sprintf("priority must be a whole number, not %s.", code(v))}
	}

	r.Config.Priority = n

	return nil
}

func setEnabled(r *rules.Route, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return &userError{msg: fmt.Sprintf("enabled must be true or false, not %s.", code(v))}
	}

	r.Enabled = b

	return nil
}

func setMode(r *rules.Route, v string) error {
	if action.Mode(v) != action.ReadOnly {
		return &userError{msg: fmt.Sprintf("Slack can set only mode `read-only` for now, not %s.", code(v))}
	}

	r.Config.Mode = action.ReadOnly

	return nil
}

func setStrategy(r *rules.Route, v string) error {
	r.Config.Strategy = v

	return nil
}

func setSources(r *rules.Route, v string) error {
	r.Config.Sources = listValue(v)

	return nil
}

func setOutputs(r *rules.Route, v string) error {
	r.Config.Outputs = listValue(v)

	return nil
}

func setIntake(r *rules.Route, v string) error {
	match(r).Intake = listValue(v)
	dropEmptyMatch(r)

	return nil
}

func setService(r *rules.Route, v string) error {
	match(r).Service = clearable(v)
	dropEmptyMatch(r)

	return nil
}

func setSeverity(r *rules.Route, v string) error {
	var out []event.Severity
	for _, s := range listValue(v) {
		out = append(out, event.Severity(s))
	}

	match(r).Severity = out
	dropEmptyMatch(r)

	return nil
}

func setLabels(r *rules.Route, v string) error {
	var labels map[string]string

	for _, pair := range listValue(v) {
		k, value, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return &userError{msg: fmt.Sprintf("labels must be <name>=<value> pairs separated by commas; %s is not one.", code(pair))}
		}

		if labels == nil {
			labels = map[string]string{}
		}

		labels[strings.TrimSpace(k)] = strings.TrimSpace(value)
	}

	match(r).Labels = labels
	dropEmptyMatch(r)

	return nil
}

func setText(r *rules.Route, v string) error {
	match(r).Text = clearable(v)
	dropEmptyMatch(r)

	return nil
}

func setMaxToolCalls(r *rules.Route, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return &userError{msg: fmt.Sprintf("maxToolCalls must be a whole number, not %s.", code(v))}
	}

	r.Config.Budgets.MaxToolCalls = n

	return nil
}

func setMaxTokens(r *rules.Route, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return &userError{msg: fmt.Sprintf("maxTokens must be a whole number, not %s.", code(v))}
	}

	r.Config.Budgets.MaxTokens = n

	return nil
}

func setMaxCostUSD(r *rules.Route, v string) error {
	usd, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return &userError{msg: fmt.Sprintf("maxCostUSD must be an amount in dollars such as 2.50, not %s.", code(v))}
	}

	r.Config.Budgets.MaxCostUSD = usd

	return nil
}

func setMaxWallTime(r *rules.Route, v string) error {
	d, err := duration(v)
	if err != nil {
		return err
	}

	r.Config.Budgets.MaxWallTime = d

	return nil
}

func setWindow(r *rules.Route, v string) error {
	d, err := duration(v)
	if err != nil {
		return err
	}

	r.Config.Grouping.Window = d

	return nil
}

func setRelatedWindow(r *rules.Route, v string) error {
	d, err := duration(v)
	if err != nil {
		return err
	}

	r.Config.Grouping.RelatedWindow = d

	return nil
}

// match returns the route's match, adding an empty one when it has none.
func match(r *rules.Route) *config.Match {
	if r.Config.Match == nil {
		r.Config.Match = &config.Match{}
	}

	return r.Config.Match
}

// dropEmptyMatch removes a match that sets nothing, so the checks see that
// the route has none.
func dropEmptyMatch(r *rules.Route) {
	m := r.Config.Match
	if m != nil && len(m.Intake) == 0 && len(m.Labels) == 0 && m.Service == "" && len(m.Severity) == 0 && m.Text == "" {
		r.Config.Match = nil
	}
}

// clearable returns v, or "" for noneValue.
func clearable(v string) string {
	if v == noneValue {
		return ""
	}

	return v
}

func duration(v string) (time.Duration, error) {
	if v == "0" {
		return 0, nil
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, &userError{msg: fmt.Sprintf("%s is not a duration such as 30s, 5m or 1h.", code(v))}
	}

	return d, nil
}

func codeList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = code(item)
	}

	return strings.Join(quoted, ", ")
}

func asStrings[S ~string](values []S) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}

	return out
}

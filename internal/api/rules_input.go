package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/problem"
)

// listenerOf is the listener that a request body describes. It does not
// check it; the store runs the config checks.
func listenerOf(spec apigen.ListenerSpec) config.Listener {
	l := config.Listener{
		Source:   config.ListenerSource(valueOf(spec.Source)),
		Channel:  spec.Channel,
		Extract:  valueOf(spec.Extract),
		Recovery: valueOf(spec.Recovery),
	}

	if m := spec.Match; m != nil {
		l.Match.Text = valueOf(m.Text)

		if s := m.Sender; s != nil {
			l.Match.Sender = config.ListenerSender{
				Kind: event.SenderKind(valueOf(s.Kind)), Names: valueOf(s.Names), Bots: valueOf(s.Bots), Users: valueOf(s.Users),
			}
		}
	}

	return l
}

// routeOf is the route that a request body describes, starting from the
// defaults a route gets in the config file. It does not check the route;
// the store runs the config checks. It answers 422 for a duration that
// does not parse, which the checks never see.
func routeOf(name string, spec apigen.RouteSpec) (config.Route, error) {
	r := config.NewRoute(name)

	if spec.Priority != nil {
		r.Priority = *spec.Priority
	}

	if s := valueOf(spec.Strategy); s != "" {
		r.Strategy = s
	}

	if spec.Mode != nil {
		r.Mode = action.Mode(*spec.Mode)
	}

	r.Sources = valueOf(spec.Sources)
	r.Outputs = valueOf(spec.Outputs)
	r.Match = matchOf(spec.Match)

	var parse durationParser

	if b := spec.Budgets; b != nil {
		r.Budgets.MaxTokens = valueOf(b.MaxTokens)
		r.Budgets.MaxToolCalls = valueOf(b.MaxToolCalls)
		r.Budgets.MaxCostUSD = valueOf(b.MaxCostUSD)
		r.Budgets.MaxWallTime = parse.duration("budgets.maxWallTime", b.MaxWallTime)
	}

	if g := spec.Grouping; g != nil {
		r.Grouping.FingerprintLabels = valueOf(g.FingerprintLabels)
		r.Grouping.RelatedWindow = parse.duration("grouping.relatedWindow", g.RelatedWindow)

		if w := parse.duration("grouping.window", g.Window); w != 0 {
			r.Grouping.Window = w
		}
	}

	if len(parse.fields) > 0 {
		return config.Route{}, &Error{
			Status: http.StatusUnprocessableEntity, Code: codeValidation, Fields: parse.fields,
			Detail: "The route does not pass the checks: a duration does not parse.",
		}
	}

	return r, nil
}

// matchOf is the match of a route body, or nil when it sets nothing, as a
// route without a match has none.
func matchOf(m *apigen.RouteMatch) *config.Match {
	if m == nil {
		return nil
	}

	out := &config.Match{
		Intake: valueOf(m.Intake), Labels: valueOf(m.Labels), Service: valueOf(m.Service), Text: valueOf(m.Text),
	}

	for _, s := range valueOf(m.Severity) {
		out.Severity = append(out.Severity, event.Severity(s))
	}

	if len(out.Intake) == 0 && len(out.Labels) == 0 && out.Service == "" && len(out.Severity) == 0 && out.Text == "" {
		return nil
	}

	return out
}

// durationParser reads the durations of a body, and keeps a field error
// for each that does not parse.
type durationParser struct {
	fields []problem.FieldError
}

// duration parses text as a Go duration such as 5m. Nil, "" and "0" are
// zero.
func (p *durationParser) duration(field string, text *string) time.Duration {
	if text == nil || *text == "" || *text == "0" {
		return 0
	}

	d, err := time.ParseDuration(strings.TrimSpace(*text))
	if err != nil {
		p.fields = append(p.fields, problem.FieldError{Field: field, Message: fmt.Sprintf("%q is not a duration such as 30s, 5m or 1h", *text)})

		return 0
	}

	return d
}

// valueOf returns what p points to, or the zero value when p is nil.
func valueOf[T any](p *T) T {
	if p == nil {
		var zero T

		return zero
	}

	return *p
}

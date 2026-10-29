package api

import (
	"slices"
	"time"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// listenerView is the API's view of the parts of a listener that a person
// sets, with the name and whether it is trusted.
func listenerView(name string, l config.Listener) apigen.Listener {
	out := apigen.Listener{
		Name:     name,
		Channel:  l.Channel,
		Source:   nonEmptyText(string(l.Source)),
		Extract:  listOf(l.Extract),
		Recovery: nonEmptyText(l.Recovery),
		Trusted:  l.Trusted,
	}

	sender, text := l.Match.Sender, l.Match.Text
	if sender.Kind == "" && len(sender.Names) == 0 && len(sender.Bots) == 0 && len(sender.Users) == 0 && text == "" {
		return out
	}

	out.Match = &apigen.ListenerMatch{Text: nonEmptyText(text)}

	if sender.Kind != "" || len(sender.Names) > 0 || len(sender.Bots) > 0 || len(sender.Users) > 0 {
		out.Match.Sender = &apigen.ListenerSender{
			Kind: nonEmptyText(string(sender.Kind)), Names: listOf(sender.Names), Bots: listOf(sender.Bots), Users: listOf(sender.Users),
		}
	}

	return out
}

// withListenerStore adds what is stored of a runtime listener to its view.
func withListenerStore(item apigen.Listener, l rules.Listener) apigen.Listener {
	item.Enabled = l.Enabled
	item.Version = new(l.Version)
	item.UpdatedBy = nonEmptyText(l.UpdatedBy)
	item.UpdatedAt = timeOf(l.UpdatedAt)

	return item
}

// routeView is the API's view of the parts of a route that a person sets,
// with its name.
func routeView(r config.Route) apigen.Route {
	out := apigen.Route{
		Name:     r.Name,
		Priority: new(r.Priority),
		Strategy: nonEmptyText(r.Strategy),
		Sources:  listOf(r.Sources),
		Outputs:  listOf(r.Outputs),
		Mode:     new(apigen.RouteMode(r.Mode)),
		Budgets: &apigen.RouteBudgets{
			MaxTokens:    nonZero(r.Budgets.MaxTokens),
			MaxToolCalls: nonZero(r.Budgets.MaxToolCalls),
			MaxWallTime:  durationText(r.Budgets.MaxWallTime),
			MaxCostUSD:   nonZero(r.Budgets.MaxCostUSD),
		},
		Grouping: &apigen.RouteGrouping{
			Window:            durationText(r.Grouping.Window),
			FingerprintLabels: listOf(r.Grouping.FingerprintLabels),
			RelatedWindow:     durationText(r.Grouping.RelatedWindow),
		},
	}

	if m := r.Match; m != nil {
		severity := make([]string, len(m.Severity))
		for i, s := range m.Severity {
			severity[i] = string(s)
		}

		out.Match = &apigen.RouteMatch{Intake: listOf(m.Intake), Service: nonEmptyText(m.Service), Severity: listOf(severity), Text: nonEmptyText(m.Text)}
		if len(m.Labels) > 0 {
			out.Match.Labels = &m.Labels
		}
	}

	return out
}

// withRouteStore adds what is stored of a runtime route to its view. The
// view takes the position from the route in effect, when given.
func withRouteStore(item apigen.Route, r rules.Route, inEffect apigen.Route) apigen.Route {
	item.Enabled = r.Enabled
	item.Version = new(r.Version)
	item.UpdatedBy = nonEmptyText(r.UpdatedBy)
	item.UpdatedAt = timeOf(r.UpdatedAt)
	item.Layer, item.Status, item.Position = inEffect.Layer, inEffect.Status, inEffect.Position

	return item
}

// skippedStatus says why a runtime rule of kind and name is not in effect:
// its status and the reason.
func skippedStatus(e *rules.Effective, kind, name string) (apigen.RuleStatus, *string) {
	for _, group := range []struct {
		skipped []rules.Skipped
		status  apigen.RuleStatus
	}{
		{e.Shadowed, apigen.Shadowed},
		{e.Disabled, apigen.Disabled},
		{e.Invalid, apigen.Invalid},
	} {
		for _, s := range group.skipped {
			if s.Kind == kind && s.Name == name {
				return group.status, nonEmptyText(s.Reason)
			}
		}
	}

	return apigen.Invalid, new("the rule is not in effect")
}

// listOf returns a pointer to a copy of items, or nil when there are none,
// so an empty list is left out of the answer.
func listOf(items []string) *[]string {
	if len(items) == 0 {
		return nil
	}

	return new(slices.Clone(items))
}

func nonEmptyText(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func nonZero[T int | float64](n T) *T {
	if n == 0 {
		return nil
	}

	return &n
}

// durationText writes d as a Go duration, or nil for zero.
func durationText(d time.Duration) *string {
	if d == 0 {
		return nil
	}

	return new(d.String())
}

func timeOf(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}

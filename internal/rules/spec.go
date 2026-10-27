package rules

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// listenerMatch is the match column of a runtime listener: every part of a
// file listener except its channel and trust.
type listenerMatch struct {
	Sender   senderSpec `json:"sender,omitzero"`
	Text     string     `json:"text,omitempty"`
	Extract  []string   `json:"extract,omitempty"`
	Recovery string     `json:"recovery,omitempty"`
}

type senderSpec struct {
	Kind  string   `json:"kind,omitempty"`
	Names []string `json:"names,omitempty"`
	Bots  []string `json:"bots,omitempty"`
	Users []string `json:"users,omitempty"`
}

// routeSpec is the spec column of a runtime route: every part of a file
// route except its name and priority. Durations are written as Go
// durations, such as 10m0s.
type routeSpec struct {
	Match    *matchSpec   `json:"match,omitempty"`
	Strategy string       `json:"strategy,omitempty"`
	Sources  []string     `json:"sources,omitempty"`
	Mode     string       `json:"mode"`
	Outputs  []string     `json:"outputs,omitempty"`
	Budgets  budgetSpec   `json:"budgets,omitzero"`
	Grouping groupingSpec `json:"grouping,omitzero"`
}

type matchSpec struct {
	Intake   []string          `json:"intake,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Service  string            `json:"service,omitempty"`
	Severity []string          `json:"severity,omitempty"`
	Text     string            `json:"text,omitempty"`
}

type budgetSpec struct {
	MaxToolCalls int    `json:"maxToolCalls,omitempty"`
	MaxWallTime  string `json:"maxWallTime,omitempty"`
}

type groupingSpec struct {
	Window            string   `json:"window,omitempty"`
	FingerprintLabels []string `json:"fingerprintLabels,omitempty"`
}

// listenerView is what the audit log records of a listener. It holds only
// names, IDs and patterns, never a secret.
type listenerView struct {
	Channel string        `json:"channel"`
	Match   listenerMatch `json:"match"`
	Trusted bool          `json:"trusted"`
	Enabled bool          `json:"enabled"`
}

// routeView is what the audit log records of a route. Routes name the
// connectors, sources and outputs they use, and never hold a secret.
type routeView struct {
	Priority int       `json:"priority"`
	Enabled  bool      `json:"enabled"`
	Spec     routeSpec `json:"spec"`
}

func (l Listener) view() listenerView {
	return listenerView{Channel: l.Config.Channel, Match: matchOfListener(l.Config), Trusted: l.Config.Trusted, Enabled: l.Enabled}
}

func (r Route) view() routeView {
	return routeView{Priority: r.Config.Priority, Enabled: r.Enabled, Spec: specOfRoute(r.Config)}
}

// listenerOfRow decodes a stored runtime listener.
func listenerOfRow(row db.Listener) (Listener, error) {
	var m listenerMatch
	if err := json.Unmarshal(row.Match, &m); err != nil {
		return Listener{}, fmt.Errorf("decode listener %s: %w", row.Name, err)
	}

	return Listener{
		Name: row.Name,
		Config: config.Listener{
			Channel: row.SlackChannel,
			Match: config.ListenerMatch{
				Sender: config.ListenerSender{
					Kind: event.SenderKind(m.Sender.Kind), Names: m.Sender.Names, Bots: m.Sender.Bots, Users: m.Sender.Users,
				},
				Text: m.Text,
			},
			Extract:  m.Extract,
			Recovery: m.Recovery,
			Trusted:  row.Trusted,
		},
		Enabled:   row.Enabled,
		Version:   int(row.Version),
		UpdatedBy: deref(row.UpdatedBy),
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// routeOfRow decodes a stored runtime route. Fields the spec leaves out
// keep the defaults of config.NewRoute.
func routeOfRow(row db.Route) (Route, error) {
	var s routeSpec
	if err := json.Unmarshal(row.Spec, &s); err != nil {
		return Route{}, fmt.Errorf("decode route %s: %w", row.Name, err)
	}

	r := config.NewRoute(row.Name)
	r.Priority = int(row.Priority)
	r.Sources = s.Sources
	r.Outputs = s.Outputs
	r.Grouping.FingerprintLabels = s.Grouping.FingerprintLabels
	r.Budgets.MaxToolCalls = s.Budgets.MaxToolCalls

	if s.Strategy != "" {
		r.Strategy = s.Strategy
	}

	if s.Mode != "" {
		r.Mode = action.Mode(s.Mode)
	}

	if s.Match != nil {
		r.Match = &config.Match{
			Intake: s.Match.Intake, Labels: s.Match.Labels, Service: s.Match.Service,
			Severity: severities(s.Match.Severity), Text: s.Match.Text,
		}
	}

	var err error
	if r.Budgets.MaxWallTime, err = optionalDuration(s.Budgets.MaxWallTime); err != nil {
		return Route{}, fmt.Errorf("decode route %s: budgets.maxWallTime: %w", row.Name, err)
	}

	if s.Grouping.Window != "" {
		if r.Grouping.Window, err = time.ParseDuration(s.Grouping.Window); err != nil {
			return Route{}, fmt.Errorf("decode route %s: grouping.window: %w", row.Name, err)
		}
	}

	return Route{
		Config:    r,
		Enabled:   row.Enabled,
		Version:   int(row.Version),
		UpdatedBy: deref(row.UpdatedBy),
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func matchOfListener(l config.Listener) listenerMatch {
	s := l.Match.Sender

	return listenerMatch{
		Sender:   senderSpec{Kind: string(s.Kind), Names: s.Names, Bots: s.Bots, Users: s.Users},
		Text:     l.Match.Text,
		Extract:  l.Extract,
		Recovery: l.Recovery,
	}
}

func specOfRoute(r config.Route) routeSpec {
	s := routeSpec{
		Strategy: r.Strategy,
		Sources:  r.Sources,
		Mode:     string(r.Mode),
		Outputs:  r.Outputs,
		Budgets:  budgetSpec{MaxToolCalls: r.Budgets.MaxToolCalls, MaxWallTime: durationText(r.Budgets.MaxWallTime)},
		Grouping: groupingSpec{Window: durationText(r.Grouping.Window), FingerprintLabels: r.Grouping.FingerprintLabels},
	}

	if m := r.Match; m != nil {
		severity := make([]string, len(m.Severity))
		for i, sev := range m.Severity {
			severity[i] = string(sev)
		}

		s.Match = &matchSpec{Intake: m.Intake, Labels: m.Labels, Service: m.Service, Severity: severity, Text: m.Text}
		if len(severity) == 0 {
			s.Match.Severity = nil
		}
	}

	return s
}

func severities(names []string) []event.Severity {
	if len(names) == 0 {
		return nil
	}

	out := make([]event.Severity, len(names))
	for i, n := range names {
		out[i] = event.Severity(n)
	}

	return out
}

// durationText writes d as a Go duration, or "" for zero.
func durationText(d time.Duration) string {
	if d == 0 {
		return ""
	}

	return d.String()
}

// optionalDuration parses a Go duration, or returns zero for "".
func optionalDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse duration: %w", err)
	}

	return d, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

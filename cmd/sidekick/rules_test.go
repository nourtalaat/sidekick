package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// gatedAction is an approval-only action that does nothing.
type gatedAction struct{ action.ReadWriteGate }

func (gatedAction) Spec() action.Spec { return action.Spec{Name: "gated"} }

func (gatedAction) Validate(context.Context, json.RawMessage) error { return nil }

func (gatedAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

// plainAction is an action that is not approval-only.
type plainAction struct{}

func (plainAction) Spec() action.Spec { return action.Spec{Name: "note"} }

func (plainAction) Validate(context.Context, json.RawMessage) error { return nil }

func (plainAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

func TestWarnReadWriteActions(t *testing.T) {
	t.Parallel()

	actions := map[string]action.Action{
		"opted-in": gatedAction{action.ReadWriteGate{AllowReadWrite: true}},
		"blocked":  gatedAction{},
		"note":     plainAction{},
	}

	route := func(name string, mode action.Mode, names ...string) rules.EffectiveRoute {
		return rules.EffectiveRoute{Config: config.Route{Name: name, Mode: mode, Actions: names}, Layer: rules.LayerFile}
	}

	tests := []struct {
		name   string
		routes []rules.EffectiveRoute
		want   int
	}{
		{name: "an opted-in action on a read-write route", routes: []rules.EffectiveRoute{route("ops", action.ReadWrite, "opted-in")}, want: 1},
		{name: "an action that is blocked", routes: []rules.EffectiveRoute{route("ops", action.ReadWrite, "blocked")}},
		{name: "an action that is not approval-only", routes: []rules.EffectiveRoute{route("ops", action.ReadWrite, "note")}},
		{name: "an approved route", routes: []rules.EffectiveRoute{route("ops", action.Approved, "opted-in")}},
		{
			name:   "one warning for each route and action",
			routes: []rules.EffectiveRoute{route("a", action.ReadWrite, "opted-in", "note"), route("b", action.ReadWrite, "opted-in")},
			want:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			log := slog.New(slog.NewJSONHandler(&out, nil))
			warnReadWriteActions(t.Context(), log, &rules.Effective{Routes: tt.routes}, actions)

			require.Equal(t, tt.want, bytes.Count(out.Bytes(), []byte(`"level":"WARN"`)))
		})
	}
}

// Package grafanasilence is the action grafana.silence: it silences alerts
// in the Alertmanager that is built into Grafana alerting, for a limited
// time, after an approver approved the silence, and it can end the silence
// early. The rules are those of alertmanager.silence: a silence may only
// match what the scopes allow and may not last longer than
// limits.maxDuration. Grafana serves the Alertmanager API v2 under another
// path, so this package only describes Grafana to the silencer of
// internal/action/alertmanagersilence and reads Grafana's own config keys.
//
// What the action relies on from Grafana, as its public source shows (tag
// v13.2.5 of github.com/grafana/grafana; paths are in that repository under
// pkg/services/). It supports Grafana 11.6 and later: the facts below are the
// same at v11.6.9 and v12.4.14, and the silence routes and permission checks
// are the same at v13.3.0.
//
//   - ngalert/api/authorization.go and ngalert/api/tooling/post.json (the
//     published spec): POST, GET and DELETE of /api/alertmanager/grafana/api/v2/silences
//     and /silence/{id}, and GET /api/alertmanager/grafana/api/v2/alerts with
//     the filter parameter. A silence has matchers, startsAt, endsAt,
//     createdBy and comment, all required, and the matchers are the
//     Alertmanager ones (name, value, isRegex, isEqual, which is true when
//     absent).
//   - ngalert/api/api_alertmanager_silences.go: POST answers 202 with
//     {"silenceID": "..."}, where Alertmanager answers 200. DELETE answers 200
//     with {"message": "silence deleted"} and expires the silence, it does not
//     remove it. GET lists silences in every state.
//   - ngalert/accesscontrol/silences.go and ngalert/api/authorization.go:
//     a silence without an equality matcher on __alert_rule_uid__ (see
//     ngalert/models/silence.go, GetRuleUID) is a general silence. Making one
//     needs alert.instances:create and alert.instances:read (or
//     alert.silences:read), and ending one needs alert.instances:write and
//     read. alert.silences:create and alert.silences:write only count for
//     silences that name a rule, and are scoped to folders. The firing alerts
//     of the preview need alert.instances:read. The action strings are in
//     accesscontrol/models.go; the one named ActionAlertingInstanceUpdate is
//     alert.instances:write.
//   - authn/authnimpl/service.go (orgIDFromRequest) and
//     authn/clients/api_key.go: a service account token belongs to one
//     organization. Without an X-Grafana-Org-Id header the request runs in
//     that organization, and a header that names another one is refused with
//     401. So the action has no organization key and sends no such header.
//   - ngalert/api/api_alertmanager_silences.go, ngalert/notifier/compat.go and
//     ngalert/notifier/silence_svc.go: createdBy and comment are stored as
//     posted. Grafana does not replace createdBy with the token's user.
//   - github.com/grafana/prometheus-alertmanager, silence/silence.go (Upsert,
//     set, canUpdate), reached through github.com/grafana/alerting
//     notify/silences.go: a post with an ID changes the silence in place when
//     its matchers are the same and, for an active one, its start is the
//     same; otherwise it expires the old silence and makes a new one with a
//     new ID. Expire is safe to repeat. A start in the past moves to now, and
//     an end in the past is refused with 400. The comment, with the marker,
//     is copied either way, so the marker finds the silence whatever its ID.
//     ngalert/notifier/multiorg_alertmanager.go answers 404 for an unknown
//     ID, and 429 when the silence limit of the service is reached.
//   - public/app/features/alerting/routes.tsx: /alerting/silence/:id/edit
//     exists in Grafana 11.6, 12.4 and 13.
package grafanasilence

import (
	"context"
	"fmt"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/alertmanagersilence"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: grafana.silence".
const Type = "grafana.silence"

// apiPath is where Grafana serves the API v2 of its own Alertmanager.
const apiPath = "/api/alertmanager/grafana/api/v2"

// backend describes Grafana to the silencer.
var backend = alertmanagersilence.Backend{
	Vendor:          "Grafana",
	APIPath:         apiPath,
	TargetPrefix:    "grafana",
	SilenceURL:      func(root, id string) string { return root + "/alerting/silence/" + url.PathEscape(id) + "/edit" },
	CreateLimitHint: "Grafana has reached its limit on silences; remove old silences in Grafana (Alerting, Silences) and try again",
}

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// New builds the action from its validated config. It does not call
// Grafana: a service that is down must not stop Sidekick from starting.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	built, err := alertmanagersilence.NewSilencer(ctx, name, backend, cfg.silencerConfig(), deps)
	if err != nil {
		return nil, fmt.Errorf("build the silencer: %w", err)
	}

	return built, nil
}

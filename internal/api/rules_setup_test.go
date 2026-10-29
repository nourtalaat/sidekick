//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The responder of fixture/rules_api.sql.
var reSession = sessionValue(0x04)

const reCSRF = "csrf-re"

// The tokens of the fixtures, by role.
//
//nolint:gosec // Test tokens of the fixtures.
const (
	viewerToken    = "skt_viewer"
	responderToken = "skt_responder"
	adminToken     = "skt_admin"
)

// The IDs of the tokens of the fixtures that the audit log records.
const (
	responderTokenID = "01920000-0000-7000-8000-000000000051"
	adminTokenID     = "01920000-0000-7000-8000-000000000042"
	adminUserID      = "01920000-0000-7000-8000-000000000040"
	responderUserID  = "01920000-0000-7000-8000-000000000050"
)

// ruleCeilingToolCalls is budgets.perInvestigation.maxToolCalls of the
// config of the rule tests, which a runtime route may not exceed.
const ruleCeilingToolCalls = 40

// setupRules is setup with a config that has a Slack app, a source, an
// output, a listener and two routes, and the runtime rules and the
// responder of fixture/rules_api.sql.
func setupRules(t *testing.T) server {
	t.Helper()

	s := setupWith(t, func(o *api.Options) { withRuleConfig(o.Config) })
	storetest.LoadFixture(t, s.pool, "fixture/rules_api.sql")

	return s
}

// withRuleConfig adds to cfg what the checks of runtime rules need.
func withRuleConfig(cfg *config.Config) {
	payments := config.NewRoute("payments")
	payments.Priority = 50
	payments.Match = &config.Match{Service: "payments-*"}
	payments.Outputs = []string{"slack-thread"}

	def := config.NewRoute(config.DefaultRouteName)
	def.Outputs = []string{"slack-thread"}

	cfg.Slack = &config.Slack{
		BotTokenSecret: registry.SecretRef{Name: "sidekick-slack", Key: "botToken"},
		AppTokenSecret: registry.SecretRef{Name: "sidekick-slack", Key: "appToken"},
		Mode:           config.SlackModeSocket,
	}
	cfg.Outputs = map[string]config.Section{"slack-thread": {Type: "slack"}}
	cfg.Listeners = map[string]config.Listener{"alerts": {Channel: "#alerts"}}
	cfg.Routes = []config.Route{def, payments}
	cfg.Budgets = config.Budgets{
		PerInvestigation: config.Limits{MaxToolCalls: ruleCeilingToolCalls, MaxWallTime: 10 * time.Minute},
		Concurrency:      8,
	}
}

// ruleCaller says how a request is authenticated.
type ruleCaller struct {
	name    string
	session string
	csrf    string
	token   string
}

var (
	viewerCaller    = ruleCaller{name: "a viewer", session: viSession, csrf: viCSRF}
	responderCaller = ruleCaller{name: "a responder", session: reSession, csrf: reCSRF}
	adminCaller     = ruleCaller{name: "an admin", session: adSession, csrf: adCSRF}

	viewerTokenCaller    = ruleCaller{name: "a viewer token", token: viewerToken}
	responderTokenCaller = ruleCaller{name: "a responder token", token: responderToken}
	adminTokenCaller     = ruleCaller{name: "an admin token", token: adminToken}
)

// sendRule sends a request as c with body encoded as JSON, when it is not nil.
func (s server) sendRule(t *testing.T, c ruleCaller, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)

		reader = bytes.NewReader(raw)
	}

	if c.token != "" {
		return s.doWithToken(t, method, path, c.token, reader)
	}

	return s.do(t, method, path, c.session, c.csrf, reader)
}

// ruleProblem is the problem details of an error answer.
type ruleProblem struct {
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

// ruleProblemOf decodes the problem details in rec.
func ruleProblemOf(t *testing.T, rec *httptest.ResponseRecorder) ruleProblem {
	t.Helper()

	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"), rec.Body.String())

	var p ruleProblem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Equal(t, rec.Code, p.Status)

	return p
}

// fieldMessages returns the message of each refused field.
func (p ruleProblem) fieldMessages() map[string]string {
	out := map[string]string{}
	for _, e := range p.Errors {
		out[e.Field] = e.Message
	}

	return out
}

// decodeRule reads the JSON body of rec into v.
func decodeRule(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), v), rec.Body.String())
}

// ruleAudit is a row of the audit log as the rule tests read it.
type ruleAudit struct {
	ActorKind string
	ActorID   string
	Action    string
	Target    string
	Details   map[string]any
}

// ruleAudits returns the audit log, oldest first.
func (s server) ruleAudits(t *testing.T) []ruleAudit {
	t.Helper()

	rows, err := s.pool.Query(t.Context(),
		"SELECT actor_kind, coalesce(actor_id, ''), action, coalesce(target_kind || ':' || target_id, ''), details FROM audit_log ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []ruleAudit

	for rows.Next() {
		var (
			e       ruleAudit
			details []byte
		)

		require.NoError(t, rows.Scan(&e.ActorKind, &e.ActorID, &e.Action, &e.Target, &details))
		require.NoError(t, json.Unmarshal(details, &e.Details))
		out = append(out, e)
	}

	require.NoError(t, rows.Err())

	return out
}

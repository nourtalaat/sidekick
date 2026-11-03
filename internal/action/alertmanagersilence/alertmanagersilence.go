// Package alertmanagersilence is the action alertmanager.silence: it
// silences alerts in a Prometheus Alertmanager for a limited time, after an
// approver approved the silence, and it can end the silence early. A
// silence may only match what the operator's scopes allow, and may not last
// longer than limits.maxDuration. The rules and the text that every mute
// action shares are in internal/action/mute.
//
// The package also carries the Grafana backend: Grafana's built-in
// Alertmanager serves the same API v2 under another path, so the action
// grafana.silence (internal/action/grafanasilence) builds this silencer with
// its own Backend and Config. A Backend names the vendor in messages, gives
// the API path, the target prefix and the address of a silence in the UI,
// and may add a hint to the error for a refused create.
//
// What the action relies on from Alertmanager, as its public source at the
// release tag v0.34.1 shows (https://github.com/prometheus/alertmanager,
// paths below are in that repository):
//
//   - api/v2/openapi.yaml: a silence has matchers (at least one), startsAt,
//     endsAt, createdBy and comment, all required. A matcher has name, value
//     and isRegex, and isEqual, which is true when absent. POST /api/v2/silences
//     answers 200 with {"silenceID": "..."}. GET /api/v2/silences lists
//     silences in every state unless active, pending or expired is false.
//     GET /api/v2/alerts takes the filter parameter, once for each matcher.
//     The file sets no minimum or maximum for startsAt and endsAt.
//   - api/v2/api.go: DELETE /api/v2/silence/{id} calls Expire, which ends
//     the silence and keeps it; it does not delete it. A post whose start is
//     not before its end, or whose end is in the past, is refused with 400.
//     The filter of GET /api/v2/silences keeps the silences whose matchers
//     equal the filter, so it cannot find a silence by its comment: the
//     action lists the silences and looks at the comments. GET /api/v2/alerts
//     leaves out resolved alerts and gives each alert a state of active,
//     suppressed or unprocessed.
//   - silence/silence.go: Expire is safe to repeat for a silence that has
//     ended, sets the end of an active silence to now, and answers not found
//     (404) for an unknown ID. Set moves a start in the past to now. A post
//     with an id changes that silence in place when its matchers are the same
//     and its start does not move, and otherwise ends it and makes a new one
//     with a new ID. So a person who edits a silence in the UI may keep its
//     ID or change it, and the comment, which carries the marker, is copied
//     either way.
//   - api/v2/compat.go: a silence made through this API has one set of
//     matchers. GET /api/v2/silences answers 500 for the whole list if any
//     silence has more than one set.
//   - matcher/compat/parse.go: the filter value is read by the matcher
//     parser, which asks for double-quoted values with backslashes escaped.
//   - ui/app/src/Parsing.elm and Views/SilenceView/Parsing.elm: the web UI
//     routes on the URL fragment, so a silence is at <root>/#/silences/<id>.
//
// Grafana Mimir (release mimir-3.1.8, pkg/api/api.go) serves the same API
// under -http.alertmanager-http-prefix, /alertmanager by default, and takes
// the tenant from the X-Scope-OrgID header, which the config's headers
// carry.
package alertmanagersilence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: alertmanager.silence".
const Type = "alertmanager.silence"

var (
	errNoSecrets    = errors.New("no secrets resolver to read the credentials with")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errBadSnapshot  = errors.New("the snapshot of the proposal cannot be read")
	errTargetDiffer = errors.New("the snapshot is not of these parameters")
	errNoUndoOf     = errors.New("the undo does not say which proposal it undoes")
)

// Backend says which service a silencer talks to, so one silencer serves
// Alertmanager and the Alertmanager that is built into Grafana. The
// Alertmanager backend is what New uses.
type Backend struct {
	// Vendor names the service in messages and in the description the model
	// reads, such as "Alertmanager" or "Grafana".
	Vendor string
	// APIPath is the path of the API v2 below the root URL, such as /api/v2.
	APIPath string
	// TargetPrefix starts Snapshot.Target: "grafana" gives
	// grafana:silence{...}.
	TargetPrefix string
	// SilenceURL returns the address of the silence with id in the web UI
	// of the service whose root URL is root.
	SilenceURL func(root, id string) string
	// CreateLimitHint, when set, is added to the error of a create that the
	// service answers with 429, to say what to do about it. The error is
	// still retried, like any 429.
	CreateLimitHint string
}

// alertmanagerBackend is the Backend of Prometheus Alertmanager.
var alertmanagerBackend = Backend{
	Vendor:       "Alertmanager",
	APIPath:      "/api/v2",
	TargetPrefix: "alertmanager",
	SilenceURL:   func(root, id string) string { return root + "/#/silences/" + url.PathEscape(id) },
}

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// params are what the model fills in when it proposes the action.
type params struct {
	Matchers []mute.Matcher `json:"matchers"`
	Duration string         `json:"duration"`
	Comment  string         `json:"comment"`
}

// silencer is one configured alertmanager.silence action.
type silencer struct {
	name        string
	backend     Backend
	scopes      []mute.Scope
	maxDuration time.Duration
	createdBy   string
	client      *client
	now         func() time.Time
}

var (
	_ action.Action      = (*silencer)(nil)
	_ action.Snapshotter = (*silencer)(nil)
	_ action.Undoer      = (*silencer)(nil)
)

// request is params after they were checked against the limits.
type request struct {
	matchers []mute.Matcher
	duration time.Duration
	comment  string
}

// New builds the action from its validated config. It does not call
// Alertmanager: a service that is down must not stop Sidekick from
// starting.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	return NewSilencer(ctx, name, alertmanagerBackend, cfg, deps)
}

// NewSilencer builds the action for the service that backend describes,
// from the config of one action. It does not call the service. A Grafana
// config is turned into a Config first: its token becomes the bearer token.
func NewSilencer(_ context.Context, name string, backend Backend, cfg Config, deps registry.Deps) (action.Action, error) {
	limit, err := mute.MaxDuration(cfg.Limits.MaxDuration)
	if err != nil {
		return nil, fmt.Errorf("limits.maxDuration: %w", err)
	}

	if cfg.UsesSecrets() && deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	next, err = httpauth.WrapTransport(next, cfg.TLS, deps.Secrets)
	if err != nil {
		return nil, fmt.Errorf("set up TLS: %w", err)
	}

	wrapped := *hc
	wrapped.Transport = next

	return &silencer{
		name:        name,
		backend:     backend,
		scopes:      cfg.Limits.Scopes,
		maxDuration: limit,
		createdBy:   cfg.createdBy(),
		client: &client{
			http: &wrapped, root: strings.TrimSuffix(cfg.URL, "/"), userAgent: "sidekick/" + buildinfo.Version,
			auth: httpauth.NewAuth(cfg.Config, deps.Secrets), backend: backend,
		},
		now: time.Now,
	}, nil
}

// Spec implements action.Action. The description tells the model which
// alerts it may silence and for how long.
func (s *silencer) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Silences alerts in " + s.backend.Vendor + " for a limited time, from " + mute.FormatDuration(mute.MinDuration) +
		" to " + mute.FormatDuration(s.maxDuration) + ". The silence starts when it is approved and ends by itself. ")
	b.WriteString("Give the matchers of the alerts to silence, each with name, value, isRegex and isEqual (false means not equal), " +
		"the duration as a Go duration such as 30m or 2h, and the reason as the comment. ")
	fmt.Fprintf(&b, "At most %d matchers. A silence is allowed only when it pins every label of at least one scope with an equality "+
		"matcher (isEqual true, isRegex false) on an allowed value; * in a value matches any text. Further matchers of any kind are allowed. %s. ",
		mute.MaxMatchers, mute.DescribeScopes(s.scopes))
	b.WriteString("The proposal shows the firing alerts the silence would mute now. An approver can end the silence early.")

	return action.Spec{Name: s.name, Description: b.String(), ParamsSchema: s.paramsSchema()}
}

// Validate implements action.Action. It runs without calling Alertmanager,
// so it checks what the parameters alone show: the matchers, the scopes,
// the duration and the comment. A limit violation wraps action.ErrLimit.
func (s *silencer) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := s.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// parse decodes the parameters and checks them against the limits.
func (s *silencer) parse(raw json.RawMessage) (request, error) {
	var p params
	if err := action.DecodeParams(raw, &p); err != nil {
		return request{}, fmt.Errorf("decode the parameters: %w", err)
	}

	return s.check(p.Matchers, p.Duration, p.Comment)
}

// check checks matchers, duration and comment against the limits, and
// returns them in the form the action keeps.
func (s *silencer) check(matchers []mute.Matcher, duration, comment string) (request, error) {
	if err := mute.CheckMatchers(matchers); err != nil {
		return request{}, fmt.Errorf("check the matchers: %w", err)
	}

	if err := mute.Permits(s.scopes, matchers); err != nil {
		return request{}, fmt.Errorf("check the matchers against limits.scopes: %w", err)
	}

	d, err := mute.ParseDuration(duration, s.maxDuration)
	if err != nil {
		return request{}, fmt.Errorf("check the duration: %w", err)
	}

	text, err := mute.CleanComment(comment)
	if err != nil {
		return request{}, fmt.Errorf("check the comment: %w", err)
	}

	return request{matchers: matchers, duration: d, comment: text}, nil
}

// paramsSchema is the JSON Schema of params. Sidekick checks the parameters
// against it before it calls Validate.
func (s *silencer) paramsSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["matchers", "duration", "comment"],
  "properties": {
    "matchers": {
      "type": "array",
      "minItems": 1,
      "maxItems": %d,
      "description": "The label matchers of the alerts to silence. All of them must match.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "value", "isRegex", "isEqual"],
        "properties": {
          "name": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The label name."},
          "value": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The label value, or a regular expression when isRegex is true."},
          "isRegex": {"type": "boolean"},
          "isEqual": {"type": "boolean", "description": "False turns = into != and =~ into !~."}
        }
      }
    },
    "duration": {"type": "string", "maxLength": 32, "description": "How long the silence lasts, as a Go duration such as 30m or 2h, from %s to %s."},
    "comment": {"type": "string", "minLength": 1, "maxLength": %d, "description": "Why the alerts are silenced."}
  }
}`, mute.MaxMatchers, mute.MaxMatcherNameBytes, mute.MaxMatcherValueBytes,
		mute.FormatDuration(mute.MinDuration), mute.FormatDuration(s.maxDuration), mute.MaxCommentRunes))
}

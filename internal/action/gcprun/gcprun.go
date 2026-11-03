// Package gcprun is the action gcp.cloud_run_rollback: it sends the traffic of
// a Cloud Run service back to an earlier ready revision, through the Cloud Run
// Admin API v2, after an approver approved it. The action is approval-only.
// It reads the service and its revisions, refuses what its limits do not
// allow, and changes only the traffic list of the service. It never deploys,
// splits traffic or waits for the service to be healthy.
//
// Traffic set to a revision by name no longer follows the latest revision, so
// later deploys get no traffic until traffic is set back. The proposal, the
// result and the docs say so, and Undo restores the traffic list that was
// there before.
//
// The calls are plain REST, like the Google Cloud source, with the tokens of
// internal/gcpauth. What the action relies on from Google, as its public API
// definitions show. The protos are in the repository
// https://github.com/googleapis/googleapis at the commit
// c063f49852ce7fc08c722c87f3a6f676209985a2 (it has no release tags for Cloud
// Run), under google/cloud/run/v2. The discovery document is
// run/v2/run-api.json in https://github.com/googleapis/google-api-go-client at
// the tag v0.297.0.
//
//   - service.proto, rpc GetService: GET /v2/{name=projects/*/locations/*/services/*}.
//     Service has traffic (a list of TrafficTarget), latest_ready_revision,
//     last_modifier, reconciling and etag. "If traffic is empty or not
//     provided, defaults to 100% traffic to the latest Ready Revision." The
//     etag is "a system-generated fingerprint for this version of the
//     resource. May be used to detect modification conflict during updates".
//   - traffic_target.proto: a TrafficTarget has type, revision, percent and
//     tag. The types are TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST ("the
//     Service's latest ready Revision") and
//     TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION ("a Revision by name").
//   - service.proto, rpc UpdateService: PATCH
//     /v2/{service.name=projects/*/locations/*/services/*} with the service
//     as the body, and the query updateMask, "the list of fields to be
//     updated". The answer is a long-running operation.
//   - revision.proto, rpc ListRevisions: GET
//     /v2/{parent=projects/*/locations/*/services/*}/revisions. The service
//     is the parent in the path, so the list has no filter by service. "Results
//     are sorted by creation time, descending." It takes pageSize and
//     pageToken and answers revisions and nextPageToken. A Revision has name,
//     createTime, creator, containers[].image and conditions.
//   - condition.proto: a Condition has type and state. "Ready" is the type
//     that is "True when the Resource is ready", and the state of a finished
//     one is CONDITION_SUCCEEDED.
//   - run_v2.yaml and run-api.json: GET /v2/{name=projects/*/locations/*/operations/*}
//     answers an operation with name, done and, when it failed, an error
//     with a code and a message.
//
// What the definitions do not say, so the action does not depend on it for
// safety:
//
//   - Whether UpdateService refuses a stale etag. Execute sends the live
//     etag, and compares the etag with the snapshot's itself before it writes.
//     An answer of 409 is read as a conflict.
//   - Whether a service body whose template is not in the update mask is
//     accepted. The template is marked required, so the action sends the
//     whole service as it read it, with only traffic replaced.
//   - Whether the revision field of a traffic entry and latest_ready_revision
//     hold a short name or a full path. The action reads both and writes the
//     short name.
//   - Whether a revision that has no traffic still has its Ready condition
//     succeeded. The action only looks at that condition.
//   - Which IAM permissions an update of the traffic needs, and whether
//     iam.serviceAccounts.actAs is one. docs/configuration.md says what is
//     known.
package gcprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/gcpauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: gcp.cloud_run_rollback".
const Type = "gcp.cloud_run_rollback"

// cloudRunAPI is the root of the Cloud Run Admin API.
const cloudRunAPI = "https://run.googleapis.com"

// defaultPollInterval is how long Execute waits between two looks at a
// long-running operation.
const defaultPollInterval = 2 * time.Second

var (
	errNoSecrets    = errors.New("no secrets resolver to read the credential file with")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errBadSnapshot  = errors.New("the snapshot of the proposal cannot be read")
	errTargetDiffer = errors.New("the snapshot is not of these parameters")

	errNoOperation       = errors.New("Google Cloud answered the update with an operation that has no name")
	errTrafficNotWritten = errors.New("the traffic of the service is not what the update wrote")
)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// params are what the model fills in when it proposes the action.
type params struct {
	Project       string `json:"project"`
	Location      string `json:"location"`
	Service       string `json:"service"`
	RevisionsBack *int   `json:"revisionsBack"`
}

// request is params after they were checked against the limits.
type request struct {
	service serviceRef
	back    int
}

// Rollback is one configured gcp.cloud_run_rollback action. Embedding the
// gate makes it approval-only.
type Rollback struct {
	action.ReadWriteGate
	action.RevisionLimit

	name         string
	limits       Limits
	client       *client
	pollInterval time.Duration
}

var (
	_ action.Action      = (*Rollback)(nil)
	_ action.Snapshotter = (*Rollback)(nil)
	_ action.Undoer      = (*Rollback)(nil)
)

// New builds the action from its validated config. It does not call Google
// Cloud: a service that is down must not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if cfg.CredentialsSecret != nil && deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	return &Rollback{
		ReadWriteGate: cfg.ReadWriteGate,
		RevisionLimit: cfg.RevisionLimit,
		name:          name,
		limits:        cfg.Limits,
		client: &client{
			http:      hc,
			tokens:    gcpauth.NewTokens(cfg.CredentialsSecret, deps.Secrets, hc),
			userAgent: "sidekick/" + buildinfo.Version,
			baseURL:   cloudRunAPI,
		},
		pollInterval: defaultPollInterval,
	}, nil
}

// Spec implements action.Action. The description tells the model what it may
// roll back and what a rollback does to the service.
func (r *Rollback) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Sends the traffic of a Cloud Run service back to an earlier ready revision, through the Cloud Run Admin API. ")
	b.WriteString("revisionsBack counts ready revisions older than the one that serves now, from 1 to " + fmt.Sprint(r.Max()) + ". ")
	b.WriteString("It is refused when traffic is split between revisions, when the service is being updated, or when there are not enough earlier ready revisions. ")
	b.WriteString("The service is then pinned to that revision: later deploys get no traffic until traffic is set back to the latest revision, which an approver can do with Undo. ")
	b.WriteString("Allowed projects: " + strings.Join(r.limits.Projects, ", ") + ". ")
	b.WriteString("Allowed locations: " + strings.Join(r.limits.Locations, ", ") + ". ")
	b.WriteString("Allowed services: " + strings.Join(r.limits.Services, ", ") + ". ")
	b.WriteString("Leave project or location out only when exactly one is allowed.")

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: r.paramsSchema()}
}

// Validate implements action.Action. It runs without calling Google Cloud,
// so it checks only what the parameters show. A limit violation wraps
// action.ErrLimit.
func (r *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := r.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// parse decodes the parameters and checks them against the limits.
func (r *Rollback) parse(raw json.RawMessage) (request, error) {
	var p params
	if err := action.DecodeParams(raw, &p); err != nil {
		return request{}, fmt.Errorf("decode the parameters: %w", err)
	}

	project, err := oneOf("project", p.Project, r.limits.Projects)
	if err != nil {
		return request{}, fmt.Errorf("check the project: %w", err)
	}

	location, err := oneOf("location", p.Location, r.limits.Locations)
	if err != nil {
		return request{}, fmt.Errorf("check the location: %w", err)
	}

	if !servicePattern.MatchString(p.Service) {
		return request{}, fmt.Errorf("%w: service %q is not a Cloud Run service name", action.ErrParams, p.Service)
	}

	if !matchesAny(p.Service, r.limits.Services) {
		return request{}, fmt.Errorf("service %q is not one of limits.services: %w", p.Service, action.ErrLimit)
	}

	back := 1
	if p.RevisionsBack != nil {
		back = *p.RevisionsBack
	}

	if err := r.CheckRequest(back); err != nil {
		return request{}, fmt.Errorf("check revisionsBack: %w", err)
	}

	return request{service: serviceRef{project: project, location: location, name: p.Service}, back: back}, nil
}

// paramsSchema is the JSON Schema of params. Sidekick checks the parameters
// against it before it calls Validate.
func (r *Rollback) paramsSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["service"],
  "properties": {
    "project": {"type": "string", "maxLength": 64, "description": "The project ID. Leave it out when exactly one project is allowed."},
    "location": {"type": "string", "maxLength": 64, "description": "The region of the service, such as europe-west1. Leave it out when exactly one location is allowed."},
    "service": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The name of the Cloud Run service."},
    "revisionsBack": {"type": "integer", "minimum": 1, "maximum": %d, "description": "How many ready revisions back to go. Default 1, at most %d here."}
  }
}`, maxServiceLen, action.MaxRevisionsBackCeiling, r.Max()))
}

// oneOf returns value when it is in allowed, or the only entry of allowed
// when value is empty. A value that is not allowed wraps action.ErrLimit, and
// an empty value with several allowed entries wraps action.ErrParams.
func oneOf(key, value string, allowed []string) (string, error) {
	switch {
	case value == "" && len(allowed) == 1:
		return allowed[0], nil
	case value == "":
		return "", fmt.Errorf("%w: %s is needed, because limits.%ss lists several", action.ErrParams, key, key)
	case !slices.Contains(allowed, value):
		return "", fmt.Errorf("%s %q is not one of limits.%ss: %w", key, value, key, action.ErrLimit)
	}

	return value, nil
}

// matchesAny reports whether name matches one of the patterns, in which "*"
// stands for any text. Names and patterns passed the checks of the service
// name pattern, so "*" is the only character that path.Match reads.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}

	return false
}

// refusal is an error for a request the action must not carry out. It wraps
// action.ErrLimit, so Sidekick shows the model the text and lets it propose
// again.
func refusal(format string, args ...any) error {
	return fmt.Errorf("%w: %s", action.ErrLimit, fmt.Sprintf(format, args...))
}

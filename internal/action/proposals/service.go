package proposals

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/redact"
)

// ProposeActionTool is the name of the built-in tool the model proposes
// actions with.
const ProposeActionTool = "propose_action"

// The actions of the audit entries a proposal writes. Each entry has the
// proposal as its target and the investigation's ID.
const (
	AuditProposed  = "action.proposed"
	AuditApproved  = "action.approved"
	AuditRejected  = "action.rejected"
	AuditExpired   = "action.expired"
	AuditExecuting = "action.executing"
	AuditSucceeded = "action.succeeded"
	AuditFailed    = "action.failed"
)

// auditTargetKind is the target kind of the audit entries of a proposal.
const auditTargetKind = "action_proposal"

// Limits on the text of a proposal and its decision, in characters.
const (
	maxReasonChars  = 1000
	maxNoteChars    = 500
	maxRefusalChars = 500
	// maxCheckRefusalChars is longer, because a check that calls another
	// service can list several errors for the model to fix.
	maxCheckRefusalChars = 2000
)

var (
	errNoPool   = errors.New("new proposals service: Pool and Policy are required")
	errNoCallID = errors.New("a propose_action call needs its tool call ID")
)

// Options set up a Service. Pool and Policy are required.
type Options struct {
	// Pool is the database the proposals, the audit log and the jobs are in.
	Pool *pgxpool.Pool
	// Actions are the configured action instances, by name.
	Actions map[string]action.Action
	// Policy says which actions each route may propose and run now.
	Policy action.Policy
	// Audit writes the audit entries. Nil means audit.NewWriter().
	Audit *audit.Writer
	// TTL is how long a proposal waits for a decision. Zero means
	// action.DefaultProposalTTL.
	TTL time.Duration
	// MaxPerInvestigation is the most proposals one investigation may
	// make. Zero means action.DefaultMaxPerInvestigation.
	MaxPerInvestigation int
	// Redactor redacts the reason of a proposal before it is stored. Nil
	// stores it as it is.
	Redactor *redact.Redactor
	// Metrics counts the proposals by status. Nil counts nothing.
	Metrics *Metrics
	// Logger receives a line per change, with IDs only: never parameters,
	// reasons or errors from actions. Nil logs nothing.
	Logger *slog.Logger
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// TracerProvider makes the tracer of the action.execute spans. Nil
	// means the global tracer provider.
	TracerProvider trace.TracerProvider
}

// Service is the one place that creates proposals and moves them from one
// status to the next: the investigator proposes through it, Slack and the
// API approve and reject through it, and its Handle runs the approved
// ones. It is safe for concurrent use, in one replica and across replicas,
// because every change is a single compare-and-swap on the proposal's
// row. Make it with New.
type Service struct {
	opts    Options
	schemas map[string]*tools.InputSchema
	audit   *audit.Writer
	log     *slog.Logger
	now     func() time.Time
}

// New returns a Service.
func New(opts Options) (*Service, error) {
	if opts.Pool == nil || opts.Policy == nil {
		return nil, errNoPool
	}

	schemas := make(map[string]*tools.InputSchema, len(opts.Actions))

	for name, a := range opts.Actions {
		schema, err := tools.ParseInputSchema(a.Spec().ParamsSchema)
		if err != nil {
			return nil, fmt.Errorf("new proposals service: action %q: params schema: %w", name, err)
		}

		schemas[name] = schema
	}

	s := &Service{opts: opts, schemas: schemas, audit: opts.Audit, log: opts.Logger, now: opts.Now}

	if s.audit == nil {
		s.audit = audit.NewWriter()
	}

	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}

	if s.now == nil {
		s.now = time.Now
	}

	if s.opts.TTL <= 0 {
		s.opts.TTL = action.DefaultProposalTTL
	}

	if s.opts.MaxPerInvestigation <= 0 {
		s.opts.MaxPerInvestigation = action.DefaultMaxPerInvestigation
	}

	return s, nil
}

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/action/proposals"

// tracer returns the tracer of the execute spans.
func (o Options) tracer() trace.Tracer {
	tp := o.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	return tp.Tracer(tracerName)
}

// notAllowed returns why the route may not use the action now, or "" when
// it may: the route exists and is not read-only, it lists the action, and
// the action is defined. Sidekick asks when the model proposes, when
// someone approves and when the proposal runs.
func (s *Service) notAllowed(route, actionName string) string {
	rp, ok := s.opts.Policy.Route(route)

	switch {
	case !ok:
		return fmt.Sprintf("route %q is not configured any more", route)
	case rp.Mode == action.ReadOnly || rp.Mode == "":
		return fmt.Sprintf("route %q is read-only", route)
	case !slices.Contains(rp.Actions, actionName):
		return fmt.Sprintf("route %q does not list action %q", route, actionName)
	}

	if _, ok := s.opts.Actions[actionName]; !ok {
		return fmt.Sprintf("action %q is not defined in the config any more", actionName)
	}

	return ""
}

// Decider is who approves or rejects a proposal: a Sidekick user, an API
// token, or a Slack user who has no Sidekick user yet. At least one is set.
type Decider struct {
	UserID      uuid.UUID
	SlackUserID string
	// Token says UserID is the ID of an API token, not of a user.
	Token bool
}

// actor returns the audit actor kind and ID of d.
func (d Decider) actor() (kind, id string) {
	switch {
	case d.UserID != uuid.Nil && d.Token:
		return audit.ActorToken, d.UserID.String()
	case d.UserID != uuid.Nil:
		return audit.ActorUser, d.UserID.String()
	default:
		return audit.ActorUser, d.SlackUserID
	}
}

// cut returns s with at most limit characters, ending with an ellipsis
// when it was longer.
func cut(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}

	return string(runes[:limit-1]) + "…"
}

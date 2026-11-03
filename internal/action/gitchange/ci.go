package gitchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The CI states a read can give. CIPending is not final: the job reads
// again. CINone from a read is not final until the start grace has passed.
// CITimedOut is never given by a read; the job sets it.
const (
	CIPending  = "pending"
	CIPassed   = output.CIPassed
	CIFailed   = output.CIFailed
	CINone     = output.CINone
	CIUnknown  = output.CIUnknown
	CITimedOut = output.CITimedOut
)

// Limits of the CI report.
const (
	// defaultCIReportTimeout is how long the job waits for a final state
	// when ciReportTimeout is not set.
	defaultCIReportTimeout = 2 * time.Hour
	// maxCIReportTimeout is the longest ciReportTimeout a config may set.
	maxCIReportTimeout = 24 * time.Hour
	// MaxFailedChecks is the most failed checks a read names.
	MaxFailedChecks = 10
	// maxCheckNameChars is the most characters of a check name a read keeps.
	maxCheckNameChars = 80
	// ciStartGrace is how long a read of nothing at all is waited out before
	// the report says that no CI ran. A host does not say whether CI is
	// missing or has not started yet.
	ciStartGrace = 10 * time.Minute
	// minCIReportTimeout is the shortest ciReportTimeout a config may set.
	minCIReportTimeout = time.Minute
	// ciReportAttempts is how many times a failed CI report job is tried.
	ciReportAttempts = 10
)

// unknownMessage is what the report says for a CI that a host refused to
// show or an action could not read, when the reader has no sentence of its
// own.
const unknownMessage = "Sidekick could not read the checks. The host did not let it."

// CIReader is implemented by an action that opens pull requests or merge
// requests and can read the CI of one. The CI report job calls it for every
// configured action that implements it.
type CIReader interface {
	// ReadCI reads the CI of the head commit of pr, in the repository or
	// project that params, the proposal's parameters (for an undo, those of
	// the proposal it undoes), name. A permission the host does not give is
	// not an error: it gives State CIUnknown with a Message. An error that
	// wraps action.ErrPermanent gives CIUnknown with a generic message. Any
	// other error (a rate limit, a server error, the network) is tried again
	// until the timeout.
	ReadCI(ctx context.Context, params json.RawMessage, pr action.PullRequestRef) (CI, error)
	// CIReportTimeout is how long the job waits for a final state:
	// ciReportTimeout, 2 hours by default.
	CIReportTimeout() time.Duration
	// CIHost is "github" or "gitlab", the host label of the report counter.
	CIHost() string
}

// CI is what one read of the CI of a pull request found.
type CI struct {
	// State is CIPending, CIPassed, CIFailed, CINone or CIUnknown. A read
	// says CIFailed only when no check is still running; until then it says
	// CIPending, with the failed checks it has seen.
	State string
	// Passed and Total count the checks that passed and all that were read.
	Passed int
	Total  int
	// FailedCount is how many checks failed, and Failed names the first
	// MaxFailedChecks of them.
	FailedCount int
	Failed      []CICheck
	// Message is Sidekick's own sentence for CIUnknown.
	Message string
	// Repo is how the host names the repository or project, and URL is the
	// page of the pull request or merge request. A read that has them lets
	// the job record the request for the card.
	Repo string
	URL  string
}

// CICheck is one check or job that failed.
type CICheck struct {
	// Name is plain text, as CleanName makes it.
	Name string
	// URL is the page of the check on the host.
	URL string
}

// CIConfig is the key that every action that reports CI has at the top of
// its section, next to baseURL: a host puts it in its Config with the tag
// `yaml:",inline"`.
type CIConfig struct {
	// ReportTimeout is how long Sidekick waits for the CI of a pull request
	// to finish. The default is 2h and the most is 24h.
	ReportTimeout time.Duration `yaml:"ciReportTimeout"`
}

// Check returns an error when the timeout is out of range.
func (c CIConfig) Check() []error {
	if c.ReportTimeout != 0 && (c.ReportTimeout < minCIReportTimeout || c.ReportTimeout > maxCIReportTimeout) {
		return []error{fmt.Errorf("ciReportTimeout: must be from %s to %s, got %s", minCIReportTimeout, maxCIReportTimeout, c.ReportTimeout)}
	}

	return nil
}

// Timeout returns the timeout, or the default when it is not set.
func (c CIConfig) Timeout() time.Duration {
	if c.ReportTimeout == 0 {
		return defaultCIReportTimeout
	}

	return c.ReportTimeout
}

// CleanName makes the name of a check or job that a host gave plain text:
// control, format (such as zero-width and bidirectional) and separator
// characters other than the space are dropped, runs of white space become one
// space, and it is cut to maxCheckNameChars characters. It never adds
// markup; a sink still escapes what it shows.
func CleanName(name string) string {
	var b strings.Builder

	pendingSpace := false

	for _, r := range name {
		switch {
		case unicode.IsSpace(r) && !unicode.In(r, unicode.Zl, unicode.Zp):
			pendingSpace = b.Len() > 0
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs) || !unicode.IsPrint(r):
		default:
			if pendingSpace {
				b.WriteByte(' ')

				pendingSpace = false
			}

			b.WriteRune(r)
		}
	}

	runes := []rune(b.String())
	if len(runes) <= maxCheckNameChars {
		return string(runes)
	}

	return strings.TrimSpace(string(runes[:maxCheckNameChars-1])) + "…"
}

// EnqueueCIReport starts the CI report of the proposal with the ID, which
// succeeded and opened a pull request or merge request: it adds the report
// row and the job that reads the CI. It does nothing when the report exists.
// Call it in the transaction that ends the proposal.
func EnqueueCIReport(ctx context.Context, q db.DBTX, proposalID uuid.UUID, now time.Time) error {
	rows, err := db.New(q).InsertCIReport(ctx, db.InsertCIReportParams{ProposalID: proposalID, Now: now})
	if err != nil {
		return fmt.Errorf("start the CI report of proposal %s: %w", proposalID, err)
	}

	if rows == 0 {
		return nil
	}

	if err := enqueueCIJob(ctx, q, proposalID, now); err != nil {
		return fmt.Errorf("queue the CI report of proposal %s: %w", proposalID, err)
	}

	return nil
}

// ciPayload is the payload of a ci_report job.
type ciPayload struct {
	ProposalID uuid.UUID `json:"proposal_id"`
}

// ciUniqueKeyPrefix starts the unique key of a ci_report job, which is
// followed by the proposal ID. A job releases its key in the transaction that
// enqueues the next one, so one proposal never has two jobs.
const ciUniqueKeyPrefix = "ci_report:"

// enqueueCIJob adds the job that reads the CI at runAt.
func enqueueCIJob(ctx context.Context, q db.DBTX, proposalID uuid.UUID, runAt time.Time) error {
	payload, err := json.Marshal(ciPayload{ProposalID: proposalID})
	if err != nil {
		return fmt.Errorf("encode the payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:        queue.KindCIReport,
		Payload:     payload,
		UniqueKey:   ciUniqueKeyPrefix + proposalID.String(),
		RunAt:       runAt,
		MaxAttempts: ciReportAttempts,
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue: %w", err)
	}

	return nil
}

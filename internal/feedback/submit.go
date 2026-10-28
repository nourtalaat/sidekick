package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The target kinds of feedback besides TargetVerdict.
const (
	TargetFinding  = "finding"
	TargetFix      = "fix"
	TargetToolCall = "tool_call"
)

// The channels feedback comes through, as stored.
const (
	ChannelUI    = "ui"
	ChannelSlack = "slack"
)

// The most characters of the free text of feedback.
const (
	MaxReasonChars   = 2000
	MaxCauseChars    = 2000
	MaxToolCallChars = 200
)

var (
	// ErrBadTarget is returned for a target that is not in the
	// investigation's current report or steps.
	ErrBadTarget = errors.New("the target is not in the investigation's report or steps")
	// ErrBadVerdict is returned for a real verdict that is not a verdict,
	// or that contradicts the rating of the verdict.
	ErrBadVerdict = errors.New("the real verdict does not fit the rating")
)

// InvalidError is returned for feedback whose fields cannot be right,
// whatever the investigation holds. Detail says what is wrong, in words
// fit for the person who sent it.
type InvalidError struct {
	Detail string
}

// Author is who gave feedback: a signed-in user or a Slack user. Set one.
type Author struct {
	UserID      uuid.UUID
	SlackUserID string
}

// Target is the part of an investigation that feedback is about.
type Target struct {
	// Kind is TargetVerdict, TargetFinding, TargetFix or TargetToolCall.
	Kind string
	// Ref names the part: VerdictRef, FindingRef or FixRef of the current
	// run, or the ID of a tool call.
	Ref string
}

// Submission is one person's feedback on one part of an investigation.
type Submission struct {
	InvestigationID uuid.UUID
	Target          Target
	Rating          output.Rating
	// Reason says why, when the author does.
	Reason string
	// CorrectVerdict is the real verdict, when the author gives it, and
	// CorrectCause the real cause. A real verdict confirms the
	// investigation.
	CorrectVerdict report.Verdict
	CorrectCause   string
	Author         Author
	// Memory says how the investigation's memory entry is synced when the
	// submission changes the confirmation; set Embed with vector memory on.
	Memory memory.SyncOptions
}

// Submitted is what a submission stored and changed.
type Submitted struct {
	// Feedback is the stored row.
	Feedback db.Feedback
	// RatingChanged is false when the author gave the target the same
	// rating before, so only the details changed.
	RatingChanged bool
	// Confirmation says how the confirmed verdict changed.
	Confirmation Change
}

func (e *InvalidError) Error() string {
	return "invalid feedback: " + e.Detail
}

// Submit stores s in q's transaction, replacing the author's earlier
// feedback on the same part, and updates the confirmed verdict and the
// investigation's memory entry. A real verdict confirms the investigation,
// and every change it makes is audited. Call it in a transaction. It
// holds a lock on the investigation's row, so two submissions at once are
// applied in turn.
//
// It returns an *InvalidError for fields that cannot be right, and errors
// that wrap ErrNotFound, ErrNotRatable (a verdict, finding or fix of an
// investigation with no verdict yet), ErrBadTarget and ErrBadVerdict.
func Submit(ctx context.Context, q db.DBTX, s Submission) (Submitted, error) {
	s, err := s.normalized()
	if err != nil {
		return Submitted{}, fmt.Errorf("check the feedback: %w", err)
	}

	queries := db.New(q)

	inv, err := queries.LockInvestigation(ctx, s.InvestigationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Submitted{}, fmt.Errorf("investigation %s: %w", s.InvestigationID, ErrNotFound)
	}

	if err != nil {
		return Submitted{}, fmt.Errorf("lock investigation %s: %w", s.InvestigationID, err)
	}

	if err := checkSubmission(ctx, queries, inv, s); err != nil {
		return Submitted{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
	}

	changed, err := ratingChanged(ctx, queries, s)
	if err != nil {
		return Submitted{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
	}

	row, err := store(ctx, queries, s)
	if err != nil {
		return Submitted{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
	}

	change, err := confirmSubmission(ctx, q, inv, s)
	if err != nil {
		return Submitted{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
	}

	if change != Unchanged {
		if err := memory.Sync(ctx, q, inv.ID, s.Memory); err != nil {
			return Submitted{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
		}
	}

	// TODO: [SK-41] Enqueue draft_lesson here when s.Reason is not empty.

	return Submitted{Feedback: row, RatingChanged: changed, Confirmation: change}, nil
}

// normalized returns s with its text trimmed, or an *InvalidError.
func (s Submission) normalized() (Submission, error) {
	s.Reason = strings.TrimSpace(s.Reason)
	s.CorrectCause = strings.TrimSpace(s.CorrectCause)
	s.Target.Ref = strings.TrimSpace(s.Target.Ref)

	switch {
	case s.Rating != output.RatingCorrect && s.Rating != output.RatingWrong:
		return s, &InvalidError{Detail: "The rating must be correct or wrong"}
	case !validKind(s.Target.Kind):
		return s, &InvalidError{Detail: "The target kind must be verdict, finding, fix or tool_call"}
	case s.Target.Ref == "":
		return s, &InvalidError{Detail: "The target ref is required"}
	case utf8.RuneCountInString(s.Target.Ref) > MaxToolCallChars:
		return s, &InvalidError{Detail: fmt.Sprintf("The target ref is longer than %d characters", MaxToolCallChars)}
	case utf8.RuneCountInString(s.Reason) > MaxReasonChars:
		return s, &InvalidError{Detail: fmt.Sprintf("The reason is longer than %d characters", MaxReasonChars)}
	case utf8.RuneCountInString(s.CorrectCause) > MaxCauseChars:
		return s, &InvalidError{Detail: fmt.Sprintf("The real cause is longer than %d characters", MaxCauseChars)}
	case (s.Author.UserID == uuid.Nil) == (s.Author.SlackUserID == ""):
		return s, &InvalidError{Detail: "The author is a user or a Slack user"}
	}

	return s, nil
}

// checkSubmission checks s against inv, which must be locked: that its
// target is in the report or steps, and that a real verdict fits.
func checkSubmission(ctx context.Context, queries *db.Queries, inv db.Investigation, s Submission) error {
	realVerdict := s.CorrectVerdict != ""

	if realVerdict && !s.CorrectVerdict.Known() {
		return fmt.Errorf("%q: %w", s.CorrectVerdict, ErrBadVerdict)
	}

	// A tool call can be rated while the investigation still runs.
	if (s.Target.Kind != TargetToolCall || realVerdict) && (inv.Status != statusFinal || inv.Verdict == nil) {
		return fmt.Errorf("investigation is %s: %w", inv.Status, ErrNotRatable)
	}

	if err := checkTarget(ctx, queries, inv, s.Target); err != nil {
		return fmt.Errorf("check the target: %w", err)
	}

	if realVerdict && s.Target.Kind == TargetVerdict {
		reported := report.Verdict(*inv.Verdict)

		if (s.Rating == output.RatingCorrect) != (s.CorrectVerdict == reported) {
			return fmt.Errorf("%s on the verdict %s, real verdict %s: %w", s.Rating, reported, s.CorrectVerdict, ErrBadVerdict)
		}
	}

	return nil
}

// checkTarget checks that t is in the current report of inv, or for a tool
// call in its steps.
func checkTarget(ctx context.Context, queries *db.Queries, inv db.Investigation, t Target) error {
	switch t.Kind {
	case TargetVerdict:
		if t.Ref != VerdictRef(inv.Run) {
			return fmt.Errorf("%s is not the verdict of run %d: %w", t.Ref, inv.Run, ErrBadTarget)
		}
	case TargetFinding, TargetFix:
		if !inReport(inv, t) {
			return fmt.Errorf("%s is not in the report of run %d: %w", t.Ref, inv.Run, ErrBadTarget)
		}
	default:
		found, err := queries.ToolCallExists(ctx, db.ToolCallExistsParams{InvestigationID: inv.ID, ToolCallID: &t.Ref})
		if err != nil {
			return fmt.Errorf("look for tool call %s: %w", t.Ref, err)
		}

		if !found {
			return fmt.Errorf("no step has tool call %s: %w", t.Ref, ErrBadTarget)
		}
	}

	return nil
}

// inReport reports whether the finding or fix t is in the report of inv.
func inReport(inv db.Investigation, t Target) bool {
	var r report.Report
	if err := json.Unmarshal(inv.Report, &r); err != nil {
		return false
	}

	if t.Kind == TargetFinding {
		for _, f := range r.Findings {
			if t.Ref == FindingRef(inv.Run, f.ID) {
				return true
			}
		}

		return false
	}

	for _, f := range r.Fixes {
		if t.Ref == FixRef(inv.Run, f.ID) {
			return true
		}
	}

	return false
}

// FindingRef is the target ref of the finding with id in the report of run.
func FindingRef(run int32, id string) string {
	return fmt.Sprintf("run:%d/%s:%s", run, TargetFinding, id)
}

// FixRef is the target ref of the fix with id in the report of run.
func FixRef(run int32, id string) string {
	return fmt.Sprintf("run:%d/%s:%s", run, TargetFix, id)
}

// ratingChanged reports whether the author of s had not rated its target
// before, or rated it the other way.
func ratingChanged(ctx context.Context, queries *db.Queries, s Submission) (bool, error) {
	var (
		before string
		err    error
	)

	if s.Author.SlackUserID != "" {
		before, err = queries.GetSlackFeedbackRating(ctx, db.GetSlackFeedbackRatingParams{
			InvestigationID: s.InvestigationID, TargetKind: s.Target.Kind, TargetRef: s.Target.Ref, SlackUserID: s.Author.SlackUserID,
		})
	} else {
		before, err = queries.GetUserFeedbackRating(ctx, db.GetUserFeedbackRatingParams{
			InvestigationID: s.InvestigationID, TargetKind: s.Target.Kind, TargetRef: s.Target.Ref, UserID: &s.Author.UserID,
		})
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("read the earlier rating: %w", err)
	}

	return before != string(s.Rating), nil
}

// store writes s, replacing the author's earlier feedback on the target.
func store(ctx context.Context, queries *db.Queries, s Submission) (db.Feedback, error) {
	reason, verdict, cause := textOrNil(s.Reason), textOrNil(string(s.CorrectVerdict)), textOrNil(s.CorrectCause)

	var (
		row db.Feedback
		err error
	)

	if s.Author.SlackUserID != "" {
		row, err = queries.UpsertSlackFeedbackDetails(ctx, db.UpsertSlackFeedbackDetailsParams{
			ID: ids.New(), InvestigationID: s.InvestigationID, TargetKind: s.Target.Kind, TargetRef: s.Target.Ref,
			Rating: string(s.Rating), Reason: reason, CorrectVerdict: verdict, CorrectCause: cause,
			SlackUserID: s.Author.SlackUserID, Channel: ChannelSlack,
		})
	} else {
		row, err = queries.UpsertUserFeedback(ctx, db.UpsertUserFeedbackParams{
			ID: ids.New(), InvestigationID: s.InvestigationID, TargetKind: s.Target.Kind, TargetRef: s.Target.Ref,
			Rating: string(s.Rating), Reason: reason, CorrectVerdict: verdict, CorrectCause: cause,
			UserID: &s.Author.UserID, Channel: ChannelUI,
		})
	}

	if err != nil {
		return db.Feedback{}, fmt.Errorf("store the feedback: %w", err)
	}

	return row, nil
}

// confirmSubmission applies the confirmation that s leads to. A real
// verdict confirms it, and the first confirmation it makes is audited too.
// Without one, only a rating of the verdict confirms or takes back.
func confirmSubmission(ctx context.Context, q db.DBTX, inv db.Investigation, s Submission) (Change, error) {
	who := s.Author.actor()
	now := time.Now()
	cur := current(inv)

	switch {
	case s.CorrectVerdict != "":
		next, change := nextRealVerdict(cur, string(s.CorrectVerdict), s.realCause(inv), who, now)

		return applyConfirmation(ctx, q, inv, next, change, who, true)
	case s.Target.Kind == TargetVerdict:
		next, change := nextConfirmation(cur, s.Rating, *inv.Verdict, causeOf(inv.Report), who, now)

		return applyConfirmation(ctx, q, inv, next, change, who, false)
	default:
		return Unchanged, nil
	}
}

// realCause is the cause that goes with the real verdict of s: the one the
// author gave, or the report's when the real verdict is the report's.
func (s Submission) realCause(inv db.Investigation) *string {
	if s.CorrectCause != "" {
		return &s.CorrectCause
	}

	if inv.Verdict != nil && s.CorrectVerdict == report.Verdict(*inv.Verdict) {
		return causeOf(inv.Report)
	}

	return nil
}

// actor is a as the confirmation records it.
func (a Author) actor() actor {
	if a.SlackUserID != "" {
		return actor{slackUser: &a.SlackUserID}
	}

	return actor{userID: &a.UserID}
}

// validKind reports whether kind is a target kind of Submit.
func validKind(kind string) bool {
	switch kind {
	case TargetVerdict, TargetFinding, TargetFix, TargetToolCall:
		return true
	}

	return false
}

// textOrNil returns &s, or nil for an empty s.
func textOrNil(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

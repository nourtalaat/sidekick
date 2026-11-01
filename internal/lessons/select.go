package lessons

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// maxCandidates is the most lessons one selection reads. They come best
// first, and a prompt takes at most MaxPerPromptLimit lessons, so the ones
// past this many could only be used if most of the better ones were left
// out for their size or as global lessons.
const maxCandidates = 100

// maxLeftOutListed is the most left-out lessons a selection lists by name.
// The count of all of them is kept as well.
const maxLeftOutListed = 50

// maxSubjectTextBytes is the most of an event's text used to rank lessons.
const maxSubjectTextBytes = 2000

// bytesPerToken is how many bytes of text count as one token. Real text has
// more bytes per token, so the estimate errs on the large side.
const bytesPerToken = 3

// overheadTokens is what the heading and the wrapper around a lesson in a
// prompt count as, besides its text and scope.
const overheadTokens = 20

// percent is the whole of a share.
const percent = 100

// Reasons a lesson is left out of a prompt.
const (
	// ReasonMaxPerPrompt: the prompt had its most lessons already.
	ReasonMaxPerPrompt = "max_per_prompt"
	// ReasonMaxTokens: the lesson would take the prompt over its tokens.
	ReasonMaxTokens = "max_tokens"
	// ReasonGlobalShare: global lessons had their share of the places or of
	// the tokens already.
	ReasonGlobalShare = "global_share"
)

// Scope says which investigations a lesson applies to. A field that is
// empty is not part of the scope; a lesson with no field set is global.
type Scope struct {
	Service  string `json:"service,omitempty"`
	Alert    string `json:"alert,omitempty"`
	Route    string `json:"route,omitempty"`
	Strategy string `json:"strategy,omitempty"`
}

// Global reports whether no scope field is set.
func (s Scope) Global() bool {
	return s == Scope{}
}

// Describe lists the fields that are set, such as "service checkout-api,
// alert CheckoutLatency", or "every investigation" for a global scope.
func (s Scope) Describe() string {
	var parts []string

	for _, f := range []struct{ name, value string }{
		{"service", s.Service}, {"alert", s.Alert}, {"route", s.Route}, {"strategy", s.Strategy},
	} {
		if f.value != "" {
			parts = append(parts, f.name+" "+f.value)
		}
	}

	if len(parts) == 0 {
		return "every investigation"
	}

	return strings.Join(parts, ", ")
}

// Lesson is a lesson as a prompt needs it.
type Lesson struct {
	ID    uuid.UUID
	Text  string
	Scope Scope
	// Helpful and Unhelpful count how people rated the verdicts of the
	// investigations that used the lesson.
	Helpful, Unhelpful int
}

// Subject is the investigation lessons are chosen for.
type Subject struct {
	// Service and AlertName are those of the event that opened the group,
	// Route is the investigation's route, and Strategy the strategy the
	// prompt is built with. Empty ones match no lesson that sets the field.
	Service, AlertName, Route, Strategy string
	// Text is the title and text of the event, which lessons are ranked
	// against. It is cut to a few thousand bytes.
	Text string
}

// Limits are the limits on the lessons of one prompt.
type Limits struct {
	// MaxPerPrompt is the most lessons, and MaxTokens the most tokens of
	// lessons.
	MaxPerPrompt, MaxTokens int
	// GlobalPercent is the share of the places and of the tokens, from 0 to
	// 100, that lessons without a scope may use.
	GlobalPercent int
}

// LimitsOf makes Limits from the settings of the lessons section, where the
// global share is a fraction from 0 to 1.
func LimitsOf(maxPerPrompt, maxTokens int, globalShare float64) Limits {
	return Limits{MaxPerPrompt: maxPerPrompt, MaxTokens: maxTokens, GlobalPercent: int(math.Round(globalShare * percent))}
}

// LeftOut is a lesson that applies to the investigation but did not go into
// the prompt.
type LeftOut struct {
	ID     uuid.UUID `json:"id"`
	Reason string    `json:"reason"`
	Scope  Scope     `json:"scope"`
}

// Selection is the lessons chosen for one prompt.
type Selection struct {
	// Used are the lessons in the prompt, in the order of their rank.
	Used []Lesson
	// LeftOut are the first lessons that applied but did not fit, in rank
	// order, at most 50 of them. LeftOutTotal counts all of them, including
	// the ones that were not read because there were so many.
	LeftOut      []LeftOut
	LeftOutTotal int
	// Candidates is how many active lessons applied.
	Candidates int
}

// Selector chooses the lessons of prompts under fixed limits.
type Selector struct {
	limits Limits
}

// NewSelector returns a Selector that chooses under limits.
func NewSelector(limits Limits) *Selector {
	return &Selector{limits: limits}
}

// Select returns the lessons for the prompt of an investigation: the active
// lessons that apply, ranked, up to the limits. It records nothing; call
// Record in the transaction that saves the prompt. A nil Selector chooses
// none.
func (s *Selector) Select(ctx context.Context, q db.DBTX, subj Subject) (Selection, error) {
	if s == nil {
		return Selection{}, nil
	}

	rows, err := db.New(q).ListLessonCandidates(ctx, db.ListLessonCandidatesParams{
		MaxRows:   maxCandidates,
		Text:      source.CutText(subj.Text, maxSubjectTextBytes),
		Service:   subj.Service,
		AlertName: subj.AlertName,
		Route:     subj.Route,
		Strategy:  subj.Strategy,
	})
	if err != nil {
		return Selection{}, fmt.Errorf("list the lessons that apply: %w", err)
	}

	candidates := make([]Lesson, len(rows))
	total := 0

	for i, row := range rows {
		candidates[i] = Lesson{
			ID:   row.ID,
			Text: row.Text,
			Scope: Scope{
				Service: deref(row.ScopeService), Alert: deref(row.ScopeAlert),
				Route: deref(row.ScopeRoute), Strategy: deref(row.ScopeStrategy),
			},
			Helpful:   int(row.Helpful),
			Unhelpful: int(row.Unhelpful),
		}
		total = int(row.Total)
	}

	return Choose(candidates, total, s.limits), nil
}

// Choose picks the lessons that go into a prompt from candidates, which are
// ranked best first, and total, how many lessons applied in all (more than
// len(candidates) when not all were read). It goes down the list once and
// takes each lesson unless:
//
//   - the prompt has its most lessons already (ReasonMaxPerPrompt);
//   - the lesson would take the tokens of the lessons over the limit
//     (ReasonMaxTokens), in which case a shorter lesson further down may
//     still fit;
//   - the lesson is global and global lessons have used their places or
//     their tokens already (ReasonGlobalShare). The places are
//     MaxPerPrompt and the tokens MaxTokens, each times GlobalPercent.
func Choose(candidates []Lesson, total int, limits Limits) Selection {
	sel := Selection{Candidates: max(total, len(candidates))}

	var (
		tokens, globalPlaces, globalTokens int

		placesAllowed = limits.MaxPerPrompt * limits.GlobalPercent / percent
		tokensAllowed = limits.MaxTokens * limits.GlobalPercent / percent
	)

	for _, l := range candidates {
		cost := Tokens(l)

		var reason string

		switch {
		case len(sel.Used) >= limits.MaxPerPrompt:
			reason = ReasonMaxPerPrompt
		case tokens+cost > limits.MaxTokens:
			reason = ReasonMaxTokens
		case l.Scope.Global() && (globalPlaces >= placesAllowed || globalTokens+cost > tokensAllowed):
			reason = ReasonGlobalShare
		}

		if reason != "" {
			sel.leaveOut(l, reason)

			continue
		}

		sel.Used = append(sel.Used, l)
		tokens += cost

		if l.Scope.Global() {
			globalPlaces++
			globalTokens += cost
		}
	}

	sel.LeftOutTotal += sel.Candidates - len(candidates)

	return sel
}

// leaveOut notes that l did not fit.
func (s *Selection) leaveOut(l Lesson, reason string) {
	s.LeftOutTotal++

	if len(s.LeftOut) < maxLeftOutListed {
		s.LeftOut = append(s.LeftOut, LeftOut{ID: l.ID, Reason: reason, Scope: l.Scope})
	}
}

// StepRecord returns the selection as the JSON that steps.lessons holds.
func (s Selection) StepRecord() (json.RawMessage, error) {
	used := make([]uuid.UUID, len(s.Used))
	for i, l := range s.Used {
		used[i] = l.ID
	}

	leftOut := s.LeftOut
	if leftOut == nil {
		leftOut = []LeftOut{}
	}

	raw, err := json.Marshal(StepRecord{Used: used, LeftOut: leftOut, LeftOutTotal: s.LeftOutTotal, Candidates: s.Candidates})
	if err != nil {
		return nil, fmt.Errorf("encode the lessons of the step: %w", err)
	}

	return raw, nil
}

// StepRecord is what steps.lessons holds for the step that carries a
// prompt: the lessons it used and the ones left out.
type StepRecord struct {
	Used         []uuid.UUID `json:"used"`
	LeftOut      []LeftOut   `json:"leftOut"`
	LeftOutTotal int         `json:"leftOutTotal"`
	Candidates   int         `json:"candidates"`
}

// Record notes that the investigation used the lessons of sel, and when
// they were last used. A lesson it used already is left as it is. Call it
// in the transaction that saves the prompt.
func Record(ctx context.Context, q db.DBTX, investigationID uuid.UUID, sel Selection) error {
	if len(sel.Used) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, len(sel.Used))
	for i, l := range sel.Used {
		ids[i] = l.ID
	}

	queries := db.New(q)

	if err := queries.InsertLessonUses(ctx, db.InsertLessonUsesParams{LessonIds: ids, InvestigationID: investigationID}); err != nil {
		return fmt.Errorf("record the lessons of investigation %s: %w", investigationID, err)
	}

	if err := queries.TouchLessonsUsed(ctx, ids); err != nil {
		return fmt.Errorf("note when the lessons were used: %w", err)
	}

	return nil
}

// Tokens estimates how many tokens l takes in a prompt: its text and scope,
// and the heading and wrapper around them.
func Tokens(l Lesson) int {
	size := len(l.Text) + len(l.Scope.Describe())

	return (size+bytesPerToken-1)/bytesPerToken + overheadTokens
}

// deref returns the value p points to, or "" for nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}

	return *p
}

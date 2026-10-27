// Package ticket is what the ticket comment outputs share: finding the
// ticket an investigation is about, the layout of the comment, and posting
// and editing it. The linear and jira packages each implement Tracker for
// their ticket system and render a Doc in its markup.
//
// The sink posts one comment per investigation when the result is final,
// not while it runs: people read a ticket later rather than watch it, and
// every new comment, and in Jira every edit, emails the ticket's watchers.
// A newer final update, such as a run that could not finish and then did,
// edits the same comment in place. A comment that was deleted is posted
// again.
//
// The related ticket comes, in this order, from:
//
//  1. a label of the event named in the output's labels, holding a ticket
//     key such as OPS-123 or a link to the ticket;
//  2. a link of the event that points at a ticket of the workspace or site;
//  3. a ticket Sidekick created: a link in the result of a succeeded action
//     proposal in Update.Proposals. The publish job does not fill
//     Proposals yet; ticket actions arrive in a later release.
//
// There is no search. Only tickets in the output's teams or projects are
// used, checked both on the key and on the ticket the tracker returns, so
// an alert cannot send a report to a ticket in any team the credentials
// can reach. With no related ticket, nothing is posted.
package ticket

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
)

// maxLookups is how many candidate tickets one publish looks up at most, so
// an event with many links cannot make many API calls.
const maxLookups = 3

// The keys of Ref.Data.
const (
	dataIssueID    = "issue_id"
	dataIssueKey   = "issue"
	dataComment    = "comment"
	dataSeq        = "seq"
	dataGeneration = "generation"
)

// keyPattern is what a ticket key looks like once upper-cased: a team or
// project key, a dash and a number, such as OPS-123.
var keyPattern = regexp.MustCompile(`^([A-Z][A-Z0-9_]{0,19})-[1-9][0-9]{0,9}$`)

// Options set up a Sink. Name, Tracker and Scopes are required.
type Options struct {
	// Name is the output's name in the config.
	Name    string
	Tracker Tracker
	// Labels are the event labels that may hold the related ticket, in
	// order.
	Labels []string
	// Scopes are the team or project keys whose tickets may get a comment.
	Scopes []string
	// Logger receives lines with IDs and ticket keys only. Nil logs
	// nothing.
	Logger *slog.Logger
}

// Sink posts the result of an investigation as a comment on its related
// ticket.
type Sink struct {
	name    string
	tracker Tracker
	labels  []string
	scopes  []string
	log     *slog.Logger
}

var _ output.Sink = (*Sink)(nil)

// ref is what Ref.Data holds: the ticket and comment, once one is posted,
// and the Seq it shows.
type ref struct {
	issue   Issue
	comment string
	seq     int64
	// generation counts the comments posted for the investigation, so the
	// comment posted after one was deleted has an idempotency key of its
	// own.
	generation int
}

// Accepts reports whether the sink shows updates of phase: it shows only
// the final result.
func (s *Sink) Accepts(phase output.Phase) bool {
	return phase == output.PhaseFinal
}

// Publish posts the comment, or edits it when u.Previous names one. An
// update whose Seq is not newer than the one the comment shows is dropped.
// With no related ticket it posts nothing and returns a ref without a
// comment, so a later update looks for the ticket again.
func (s *Sink) Publish(ctx context.Context, u output.Update) (output.Ref, error) {
	prev := refOf(u.Previous)

	if prev.comment == "" {
		return s.post(ctx, u, prev.generation)
	}

	if u.Seq <= prev.seq {
		s.log.DebugContext(ctx, "update dropped: the comment shows a newer one", "investigation_id", u.InvestigationID, "seq", u.Seq, "shown_seq", prev.seq)

		return *u.Previous, nil
	}

	err := s.tracker.UpdateComment(ctx, prev.issue, prev.comment, s.comment(u, prev.generation))
	if errors.Is(err, ErrNotFound) {
		s.log.InfoContext(ctx, "the comment was deleted; posting it again", "investigation_id", u.InvestigationID, "ticket", prev.issue.Key)

		return s.create(ctx, u, prev.issue, prev.generation+1)
	}

	if err != nil {
		return output.Ref{}, fmt.Errorf("edit the comment on %s: %w", prev.issue.Key, err)
	}

	prev.seq = u.Seq

	return s.outputRef(prev), nil
}

// post finds the related ticket and comments on it.
func (s *Sink) post(ctx context.Context, u output.Update, generation int) (output.Ref, error) {
	issue, ok, err := s.find(ctx, u)
	if err != nil {
		return output.Ref{}, fmt.Errorf("find the related ticket: %w", err)
	}

	if !ok {
		s.log.DebugContext(ctx, "no related ticket; nothing posted", "investigation_id", u.InvestigationID)

		return s.outputRef(ref{seq: u.Seq, generation: generation}), nil
	}

	return s.create(ctx, u, issue, generation)
}

// create posts the comment on issue.
func (s *Sink) create(ctx context.Context, u output.Update, issue Issue, generation int) (output.Ref, error) {
	id, err := s.tracker.CreateComment(ctx, issue, s.comment(u, generation))
	if err != nil {
		return output.Ref{}, fmt.Errorf("comment on %s: %w", issue.Key, err)
	}

	s.log.DebugContext(ctx, "commented on the related ticket", "investigation_id", u.InvestigationID, "ticket", issue.Key)

	return s.outputRef(ref{issue: issue, comment: id, seq: u.Seq, generation: generation}), nil
}

// find returns the first candidate ticket that exists and is in one of
// the sink's scopes. It looks up at most maxLookups candidates.
func (s *Sink) find(ctx context.Context, u output.Update) (Issue, bool, error) {
	lookups := 0

	for _, key := range s.candidates(u) {
		if !s.allowed(scopeOf(key)) {
			s.log.DebugContext(ctx, "related ticket skipped: not in the output's scopes", "investigation_id", u.InvestigationID, "ticket", key)

			continue
		}

		if lookups == maxLookups {
			break
		}

		lookups++

		issue, err := s.tracker.Issue(ctx, key)
		if errors.Is(err, ErrNotFound) {
			s.log.DebugContext(ctx, "related ticket not found", "investigation_id", u.InvestigationID, "ticket", key)

			continue
		}

		if err != nil {
			return Issue{}, false, fmt.Errorf("look up %s: %w", key, err)
		}

		if !s.allowed(issue.Scope) {
			s.log.DebugContext(ctx, "related ticket skipped: it moved out of the output's scopes", "investigation_id", u.InvestigationID, "ticket", key)

			continue
		}

		return issue, true, nil
	}

	return Issue{}, false, nil
}

// candidates are the keys of the tickets the update may be about, in
// order and without repeats: from the labels, the event's links, and the
// links of what succeeded actions created.
func (s *Sink) candidates(u output.Update) []string {
	var keys []string

	add := func(key string, ok bool) {
		if ok && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}

	for _, label := range s.labels {
		if value, ok := u.Event.Labels[label]; ok {
			add(s.keyOf(value))
		}
	}

	for _, l := range u.Event.Links {
		add(s.tracker.KeyFromURL(l.URL))
	}

	for _, p := range u.Proposals {
		if p.Status != action.StatusSucceeded || p.Result == nil {
			continue
		}

		for _, l := range p.Result.Links {
			add(s.tracker.KeyFromURL(l.URL))
		}
	}

	return keys
}

// keyOf reads a label value: a ticket key, or a link to a ticket.
func (s *Sink) keyOf(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if key, ok := Key(value); ok {
		return key, true
	}

	return s.tracker.KeyFromURL(value)
}

// allowed reports whether scope is one of the sink's scopes.
func (s *Sink) allowed(scope string) bool {
	return slices.ContainsFunc(s.scopes, func(allowed string) bool {
		return strings.EqualFold(allowed, scope)
	})
}

// comment returns the comment of u, with the idempotency key of the
// investigation's comment number generation in this output.
func (s *Sink) comment(u output.Update, generation int) Comment {
	return Comment{
		IdempotencyKey: "sidekick:" + s.name + ":" + u.InvestigationID + ":" + strconv.Itoa(generation),
		Doc:            Layout(u),
	}
}

// outputRef returns r as the ref the publish job stores. External is the
// ticket key and comment ID, or empty when nothing was posted.
func (s *Sink) outputRef(r ref) output.Ref {
	data := map[string]string{dataSeq: strconv.FormatInt(r.seq, 10)}
	if r.generation > 0 {
		data[dataGeneration] = strconv.Itoa(r.generation)
	}

	if r.comment == "" {
		return output.Ref{Sink: s.name, Data: data}
	}

	data[dataIssueID], data[dataIssueKey], data[dataComment] = r.issue.ID, r.issue.Key, r.comment

	return output.Ref{Sink: s.name, External: r.issue.Key + ":" + r.comment, Data: data}
}

// NewSink returns a Sink with opts.
func NewSink(opts Options) *Sink {
	return &Sink{
		name:    opts.Name,
		tracker: opts.Tracker,
		labels:  opts.Labels,
		scopes:  opts.Scopes,
		log:     cmp.Or(opts.Logger, slog.New(slog.DiscardHandler)).With("component", "output", "sink", opts.Name),
	}
}

// Key returns s as a ticket key, upper-cased, and false when it does not
// look like one.
func Key(s string) (string, bool) {
	key := strings.ToUpper(s)
	if !keyPattern.MatchString(key) {
		return "", false
	}

	return key, true
}

// refOf reads a stored ref. A nil or unreadable ref gives the zero ref,
// which names no comment.
func refOf(r *output.Ref) ref {
	if r == nil {
		return ref{}
	}

	out := ref{
		issue:   Issue{ID: r.Data[dataIssueID], Key: r.Data[dataIssueKey]},
		comment: r.Data[dataComment],
	}

	if seq, err := strconv.ParseInt(r.Data[dataSeq], 10, 64); err == nil {
		out.seq = seq
	}

	if g, err := strconv.Atoi(r.Data[dataGeneration]); err == nil && g > 0 {
		out.generation = g
	}

	if out.issue.ID == "" || out.issue.Key == "" {
		out.comment = ""
	}

	return out
}

// scopeOf returns the team or project part of a ticket key.
func scopeOf(key string) string {
	scope, _, _ := strings.Cut(key, "-")

	return scope
}

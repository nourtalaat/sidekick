package bench

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source/mock"
)

// ProposalOutcome says how one proposal of a run counted.
type ProposalOutcome string

// The outcomes of a proposal of a run.
const (
	// ProposalExpected: the proposal is paired with an expected one.
	ProposalExpected ProposalOutcome = "expected"
	// ProposalAcceptable: the proposal is an extra that the scenario lists
	// as acceptable.
	ProposalAcceptable ProposalOutcome = "acceptable"
	// ProposalUnexpected: the proposal is an extra that is wrong.
	ProposalUnexpected ProposalOutcome = "unexpected"
)

// ProposalResult is the proposal score of one run.
type ProposalResult struct {
	// Correct is true when every expected proposal was made and no wrong
	// extra was, or, for noProposal, when none was made. A run that ended
	// without a verdict is never correct.
	Correct bool
	// Expected has one entry per expected proposal, in the scenario's
	// order.
	Expected []ExpectedOutcome
	// Proposed has one entry per proposal of the run, oldest first.
	Proposed []ProposedOutcome
}

// ExpectedOutcome says whether a proposal of the run was paired with one
// expected proposal.
type ExpectedOutcome struct {
	Action  string
	Matched bool
}

// ProposedOutcome says how one proposal of the run counted.
type ProposedOutcome struct {
	Action  string
	Outcome ProposalOutcome
}

// scoreProposals scores the proposals of a run against the expected ones,
// or returns nil when the scenario does not score proposals. Each expected
// proposal is paired with at most one proposal of the run and each
// proposal with at most one expected one; a proposal left over counts
// only when an acceptable proposal fits it.
func scoreProposals(expected Expected, run RunResult) *ProposalResult {
	if !expected.scoresProposals() {
		return nil
	}

	params := make([]any, len(run.Proposals))
	for i, p := range run.Proposals {
		// A proposal whose parameters are not JSON fits nothing that
		// checks them.
		_ = json.Unmarshal(p.Params, &params[i])
	}

	fits := make([][]bool, len(expected.Proposals))
	for i, e := range expected.Proposals {
		fits[i] = make([]bool, len(run.Proposals))

		for j, p := range run.Proposals {
			fits[i][j] = e.fits(p.Action, params[j])
		}
	}

	pairedWith := pairProposals(fits, len(run.Proposals))

	out := &ProposalResult{
		Expected: make([]ExpectedOutcome, len(expected.Proposals)),
		Proposed: make([]ProposedOutcome, len(run.Proposals)),
	}

	for i, e := range expected.Proposals {
		out.Expected[i] = ExpectedOutcome{Action: e.Action, Matched: slices.Contains(pairedWith, i)}
	}

	correct := run.Report.Verdict != ""

	for j, p := range run.Proposals {
		outcome := proposalOutcome(pairedWith[j], expected.AcceptableProposals, p.Action, params[j])
		out.Proposed[j] = ProposedOutcome{Action: p.Action, Outcome: outcome}
		correct = correct && outcome != ProposalUnexpected
	}

	for _, e := range out.Expected {
		correct = correct && e.Matched
	}

	out.Correct = correct

	return out
}

// problem says in one line why the proposals of a run are not correct.
func (p ProposalResult) problem() string {
	var missing, unexpected []string

	for _, e := range p.Expected {
		if !e.Matched {
			missing = append(missing, e.Action)
		}
	}

	for _, o := range p.Proposed {
		if o.Outcome == ProposalUnexpected {
			unexpected = append(unexpected, o.Action)
		}
	}

	var parts []string

	if len(missing) > 0 {
		parts = append(parts, "missing "+strings.Join(missing, ", "))
	}

	if len(unexpected) > 0 {
		parts = append(parts, "unexpected "+strings.Join(unexpected, ", "))
	}

	if len(parts) == 0 {
		return "the proposals are not correct: the run ended without a verdict"
	}

	return "the proposals are not correct: " + strings.Join(parts, "; ")
}

// proposalOutcome returns how a proposal counted, given the index of the
// expected proposal it was paired with, or -1 for none.
func proposalOutcome(pairedWith int, acceptable []ExpectedProposal, action string, params any) ProposalOutcome {
	switch {
	case pairedWith >= 0:
		return ProposalExpected
	case slices.ContainsFunc(acceptable, func(a ExpectedProposal) bool { return a.fits(action, params) }):
		return ProposalAcceptable
	default:
		return ProposalUnexpected
	}
}

// fits reports whether a proposal of action with the decoded params is
// this proposal: the same action, and every check holds.
func (e ExpectedProposal) fits(action string, params any) bool {
	if e.Action != action {
		return false
	}

	for _, path := range slices.Sorted(maps.Keys(e.Params)) {
		steps, err := parsePath(path)
		if err != nil {
			return false
		}

		if !checkHolds(steps, e.Params[path], params) {
			return false
		}
	}

	return true
}

// pairProposals pairs expected proposals with the proposals of a run, one
// to one, so that as many expected proposals as possible are paired,
// whatever their order. fits[i][j] says whether expected proposal i fits
// run proposal j, and proposed is the number of run proposals. It returns,
// for each run proposal, the index of the expected proposal it is paired
// with, or -1. It is a maximum bipartite matching by augmenting paths; the
// sets are as small as the proposal limit of an investigation.
func pairProposals(fits [][]bool, proposed int) []int {
	pairedWith := make([]int, proposed)
	for j := range pairedWith {
		pairedWith[j] = -1
	}

	var pair func(i int, seen []bool) bool

	pair = func(i int, seen []bool) bool {
		for j := range proposed {
			if !fits[i][j] || seen[j] {
				continue
			}

			seen[j] = true

			if pairedWith[j] == -1 || pair(pairedWith[j], seen) {
				pairedWith[j] = i

				return true
			}
		}

		return false
	}

	for i := range fits {
		pair(i, make([]bool, proposed))
	}

	return pairedWith
}

// checkHolds reports whether the matcher holds for the values the path
// reaches in params. With [*] in the path, one value that holds is enough,
// and none reached means it does not hold. Without it, a path that
// reaches nothing is a missing value, which only exists: false accepts.
func checkHolds(steps []pathStep, m mock.Matcher, params any) bool {
	values := resolvePath(steps, params)
	if len(values) == 0 {
		return !hasWildcard(steps) && m.Matches(nil, false, time.Time{})
	}

	return slices.ContainsFunc(values, func(v any) bool { return m.Matches(v, true, time.Time{}) })
}

// resolvePath returns the values the path reaches in value, a decoded JSON
// document. Steps that find nothing drop out.
func resolvePath(steps []pathStep, value any) []any {
	current := []any{value}

	for _, step := range steps {
		var next []any

		for _, v := range current {
			object, ok := v.(map[string]any)
			if !ok {
				continue
			}

			child, ok := object[step.key]
			if !ok {
				continue
			}

			next = append(next, elementsAt(child, step.indexes)...)
		}

		current = next
	}

	return current
}

// elementsAt returns the values the indexes reach in value: an index
// picks one element of a list, and anyElement every element.
func elementsAt(value any, indexes []int) []any {
	values := []any{value}

	for _, index := range indexes {
		var next []any

		for _, v := range values {
			list, ok := v.([]any)
			if !ok {
				continue
			}

			switch {
			case index == anyElement:
				next = append(next, list...)
			case index < len(list):
				next = append(next, list[index])
			}
		}

		values = next
	}

	return values
}

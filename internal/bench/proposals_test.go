package bench

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source/mock"
)

// prHead is the params of a pull request proposal that the cases change.
const prHead = `{"repo": "acme/monitors", "baseBranch": "main", "title": "Add a traffic floor",
	"files": [{"path": "prometheus/rules/search.yml", "content": "alert: SearchLatency"}]}`

// proposed returns a run that gave a verdict and made the proposals.
func proposed(proposals ...Proposal) RunResult {
	run := verdictRun(report.FalsePositive)
	run.Proposals = proposals

	return run
}

// made returns a proposal of action with the params JSON.
func made(action, params string) Proposal {
	return Proposal{Action: action, Params: json.RawMessage(params)}
}

// want is a monitor-pr expected proposal with checks.
func want(checks map[string]mock.Matcher) ExpectedProposal {
	return ExpectedProposal{Action: "monitor-pr", Params: checks}
}

func TestScoreProposals(t *testing.T) {
	t.Parallel()

	prChecks := map[string]mock.Matcher{
		"repo":          {Equals: "acme/monitors"},
		"files[*].path": {Regex: "^prometheus/rules/"},
	}

	tests := []struct {
		name     string
		expected Expected
		run      RunResult
		// want is nil when the scenario does not score proposals.
		want *ProposalResult
	}{
		{
			name: "the scenario does not score proposals",
			run:  proposed(made("monitor-pr", prHead)),
		},
		{
			name:     "the expected proposal is made",
			expected: Expected{Proposals: []ExpectedProposal{want(prChecks)}},
			run:      proposed(made("monitor-pr", prHead)),
			want: &ProposalResult{
				Correct:  true,
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}},
			},
		},
		{
			name:     "another action is proposed",
			expected: Expected{Proposals: []ExpectedProposal{want(prChecks)}},
			run:      proposed(made("restart", prHead)),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{{Action: "monitor-pr"}},
				Proposed: []ProposedOutcome{{Action: "restart", Outcome: ProposalUnexpected}},
			},
		},
		{
			name:     "the right action with other parameters",
			expected: Expected{Proposals: []ExpectedProposal{want(prChecks)}},
			run:      proposed(made("monitor-pr", `{"repo": "acme/other", "files": [{"path": "prometheus/rules/a.yml"}]}`)),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{{Action: "monitor-pr"}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalUnexpected}},
			},
		},
		{
			name:     "nothing is proposed",
			expected: Expected{Proposals: []ExpectedProposal{want(nil)}},
			run:      proposed(),
			want:     &ProposalResult{Expected: []ExpectedOutcome{{Action: "monitor-pr"}}, Proposed: []ProposedOutcome{}},
		},
		{
			name:     "an extra proposal is wrong",
			expected: Expected{Proposals: []ExpectedProposal{want(prChecks)}},
			run:      proposed(made("monitor-pr", prHead), made("restart", `{}`)),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}, {Action: "restart", Outcome: ProposalUnexpected}},
			},
		},
		{
			name: "an acceptable extra proposal is fine",
			expected: Expected{
				Proposals:           []ExpectedProposal{want(prChecks)},
				AcceptableProposals: []ExpectedProposal{{Action: "restart"}},
			},
			run: proposed(made("monitor-pr", prHead), made("restart", `{"service": "search"}`)),
			want: &ProposalResult{
				Correct:  true,
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}, {Action: "restart", Outcome: ProposalAcceptable}},
			},
		},
		{
			name:     "the same proposal twice matches one expected proposal once",
			expected: Expected{Proposals: []ExpectedProposal{want(prChecks)}},
			run:      proposed(made("monitor-pr", prHead), made("monitor-pr", prHead)),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}, {Action: "monitor-pr", Outcome: ProposalUnexpected}},
			},
		},
		{
			name: "two expected proposals need two proposals",
			expected: Expected{Proposals: []ExpectedProposal{
				want(map[string]mock.Matcher{"repo": {Equals: "acme/monitors"}}),
				want(map[string]mock.Matcher{"repo": {Equals: "acme/dashboards"}}),
			}},
			run: proposed(made("monitor-pr", `{"repo": "acme/monitors"}`)),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}, {Action: "monitor-pr"}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}},
			},
		},
		{
			name: "pairing succeeds where taking the first fit would fail",
			expected: Expected{Proposals: []ExpectedProposal{
				want(nil),
				want(map[string]mock.Matcher{"repo": {Equals: "acme/monitors"}}),
			}},
			run: proposed(made("monitor-pr", `{"repo": "acme/monitors"}`), made("monitor-pr", `{"repo": "acme/dashboards"}`)),
			want: &ProposalResult{
				Correct:  true,
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}, {Action: "monitor-pr", Matched: true}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}, {Action: "monitor-pr", Outcome: ProposalExpected}},
			},
		},
		{
			name:     "noProposal and nothing proposed",
			expected: Expected{NoProposal: true},
			run:      proposed(),
			want:     &ProposalResult{Correct: true, Expected: []ExpectedOutcome{}, Proposed: []ProposedOutcome{}},
		},
		{
			name:     "noProposal and something proposed",
			expected: Expected{NoProposal: true},
			run:      proposed(made("monitor-pr", prHead)),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalUnexpected}},
			},
		},
		{
			name:     "a run that ended without a verdict is never correct",
			expected: Expected{Proposals: []ExpectedProposal{want(nil)}},
			run:      withVerdict(proposed(made("monitor-pr", prHead)), ""),
			want: &ProposalResult{
				Expected: []ExpectedOutcome{{Action: "monitor-pr", Matched: true}},
				Proposed: []ProposedOutcome{{Action: "monitor-pr", Outcome: ProposalExpected}},
			},
		},
		{
			name:     "noProposal and no verdict is not correct either",
			expected: Expected{NoProposal: true},
			run:      withVerdict(proposed(), ""),
			want:     &ProposalResult{Expected: []ExpectedOutcome{}, Proposed: []ProposedOutcome{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, scoreProposals(tt.expected, tt.run))
		})
	}
}

func TestParamChecks(t *testing.T) {
	t.Parallel()

	no := false

	tests := []struct {
		name  string
		path  string
		check mock.Matcher
		want  bool
	}{
		{name: "equals a key", path: "repo", check: mock.Matcher{Equals: "acme/monitors"}, want: true},
		{name: "equals, another value", path: "repo", check: mock.Matcher{Equals: "acme/other"}, want: false},
		{name: "regex", path: "title", check: mock.Matcher{Regex: "(?i)traffic"}, want: true},
		{name: "contains in a string", path: "title", check: mock.Matcher{Contains: "floor"}, want: true},
		{name: "oneOf", path: "baseBranch", check: mock.Matcher{OneOf: []any{"main", "master"}}, want: true},
		{name: "an element", path: "files[0].path", check: mock.Matcher{Regex: "search.yml$"}, want: true},
		{name: "an element out of range", path: "files[1].path", check: mock.Matcher{Regex: "."}, want: false},
		{name: "any element", path: "files[*].path", check: mock.Matcher{Regex: "^prometheus/"}, want: true},
		{name: "any element, none holds", path: "files[*].path", check: mock.Matcher{Regex: "^datadog/"}, want: false},
		{name: "any element of a key that is not a list", path: "title[*]", check: mock.Matcher{Regex: "."}, want: false},
		{name: "any element of a missing key", path: "nope[*].path", check: mock.Matcher{Regex: "."}, want: false},
		{name: "a missing key", path: "nope", check: mock.Matcher{Equals: "x"}, want: false},
		{name: "a missing key and exists false", path: "nope", check: mock.Matcher{Exists: &no}, want: true},
		{name: "a missing key inside an element and exists false", path: "files[0].delete", check: mock.Matcher{Exists: &no}, want: true},
		{name: "a key of a value that is not an object", path: "title.x", check: mock.Matcher{Equals: "x"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := ExpectedProposal{Action: "monitor-pr", Params: map[string]mock.Matcher{tt.path: tt.check}}

			var params any
			require.NoError(t, json.Unmarshal([]byte(prHead), &params))
			require.Equal(t, tt.want, e.fits("monitor-pr", params))
		})
	}
}

func TestPairProposals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fits     [][]bool
		proposed int
		want     []int
	}{
		{name: "nothing to pair", fits: nil, proposed: 2, want: []int{-1, -1}},
		{name: "one to one", fits: [][]bool{{false, true}}, proposed: 2, want: []int{-1, 0}},
		{
			name:     "an earlier choice is moved to make room",
			fits:     [][]bool{{true, true}, {true, false}},
			proposed: 2,
			want:     []int{1, 0},
		},
		{
			name:     "more expected proposals than proposals",
			fits:     [][]bool{{true}, {true}},
			proposed: 1,
			want:     []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, pairProposals(tt.fits, tt.proposed))
		})
	}
}

func TestScoreRunSetsProposal(t *testing.T) {
	t.Parallel()

	results := &Results{Scenarios: []ScenarioResult{{
		ID:       "proposals",
		Expected: Expected{Verdict: report.FalsePositive, Proposals: []ExpectedProposal{want(nil)}},
		Runs:     []RunResult{proposed(made("monitor-pr", prHead)), proposed()},
	}, {
		ID:       "no-proposals",
		Expected: Expected{Verdict: report.FalsePositive},
		Runs:     []RunResult{proposed(made("monitor-pr", prHead))},
	}}}

	require.NoError(t, Score(t.Context(), results, ScoreOptions{}))

	require.True(t, results.Scenarios[0].Runs[0].Proposal.Correct)
	require.False(t, results.Scenarios[0].Runs[1].Proposal.Correct)
	require.Nil(t, results.Scenarios[1].Runs[0].Proposal, "a scenario that does not score proposals has no proposal score")
}

package report

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfidenceRank(t *testing.T) {
	t.Parallel()

	tests := []struct {
		confidence Confidence
		want       int
	}{
		{confidence: "", want: 0},
		{confidence: "certain", want: 0},
		{confidence: Low, want: 1},
		{confidence: Medium, want: 2},
		{confidence: High, want: 3},
	}

	for _, tt := range tests {
		t.Run(string(tt.confidence), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.confidence.Rank())
		})
	}
}

func TestFindingHypothesis(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		finding Finding
		want    bool
	}{
		{name: "no evidence", finding: Finding{Claim: "the deploy broke it"}, want: true},
		{
			name:    "with evidence",
			finding: Finding{Claim: "the deploy broke it", Evidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "deploy 42"}}},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.finding.Hypothesis())
		})
	}
}

func TestReportValidate(t *testing.T) {
	t.Parallel()

	truePositive := Report{
		Verdict:    TruePositive,
		Confidence: High,
		Reason:     "error rate rose right after deploy 42",
		Cause:      "deploy 42 removed a cache",
		Impact:     "5% of checkouts fail",
		Fixes:      []Fix{{Title: "Roll back deploy 42"}},
	}
	fpDetail := &FalsePositiveDetail{
		WhyFired:   "the threshold counts retries as errors",
		Prevention: []Prevention{{Kind: PreventThreshold, Detail: "raise to 5%"}},
	}
	falsePositive := Report{Verdict: FalsePositive, Confidence: High, Reason: "only retries", FalsePositive: fpDetail}
	minMedium := Rules{MinFalsePositiveConfidence: Medium}

	tests := []struct {
		name     string
		report   Report
		rules    Rules
		wantErrs []string
	}{
		{name: "complete true positive", report: truePositive, rules: minMedium},
		{
			name:     "true positive without cause",
			report:   with(truePositive, func(r *Report) { r.Cause = "" }),
			wantErrs: []string{"true_positive needs cause, impact and at least one fix"},
		},
		{
			name:     "true positive without impact",
			report:   with(truePositive, func(r *Report) { r.Impact = "" }),
			wantErrs: []string{"true_positive needs cause, impact and at least one fix"},
		},
		{
			name:     "true positive without a fix",
			report:   with(truePositive, func(r *Report) { r.Fixes = nil }),
			wantErrs: []string{"true_positive needs cause, impact and at least one fix"},
		},
		{name: "complete false positive", report: falsePositive, rules: minMedium},
		{
			name:     "false positive without detail",
			report:   with(falsePositive, func(r *Report) { r.FalsePositive = nil }),
			wantErrs: []string{"false_positive needs falsePositive.whyFired and at least one prevention"},
		},
		{
			name: "false positive without why it fired",
			report: with(falsePositive, func(r *Report) {
				r.FalsePositive = &FalsePositiveDetail{Prevention: fpDetail.Prevention}
			}),
			wantErrs: []string{"false_positive needs falsePositive.whyFired"},
		},
		{
			name: "false positive without a prevention",
			report: with(falsePositive, func(r *Report) {
				r.FalsePositive = &FalsePositiveDetail{WhyFired: fpDetail.WhyFired}
			}),
			wantErrs: []string{"at least one prevention"},
		},
		{
			name:     "false positive below the minimum confidence",
			report:   with(falsePositive, func(r *Report) { r.Confidence = Low }),
			rules:    minMedium,
			wantErrs: []string{"false_positive needs confidence medium or higher"},
		},
		{
			name:   "false positive at the minimum confidence",
			report: with(falsePositive, func(r *Report) { r.Confidence = Medium }),
			rules:  minMedium,
		},
		{
			name:     "empty rules require high confidence",
			report:   with(falsePositive, func(r *Report) { r.Confidence = Medium }),
			rules:    Rules{},
			wantErrs: []string{"false_positive needs confidence high or higher"},
		},
		{
			name:     "empty rules reject low confidence",
			report:   with(falsePositive, func(r *Report) { r.Confidence = Low }),
			rules:    Rules{},
			wantErrs: []string{"false_positive needs confidence high or higher"},
		},
		{
			name:   "empty rules accept high confidence",
			report: falsePositive,
			rules:  Rules{},
		},
		{
			name:     "an unknown minimum means high",
			report:   with(falsePositive, func(r *Report) { r.Confidence = Medium }),
			rules:    Rules{MinFalsePositiveConfidence: "certain"},
			wantErrs: []string{"false_positive needs confidence high or higher"},
		},
		{
			name:   "an explicit low minimum allows low confidence",
			report: with(falsePositive, func(r *Report) { r.Confidence = Low }),
			rules:  Rules{MinFalsePositiveConfidence: Low},
		},
		{
			name: "true positive with low confidence ignores the false positive minimum",
			report: with(truePositive, func(r *Report) {
				r.Confidence = Low
			}),
			rules: Rules{MinFalsePositiveConfidence: High},
		},
		{name: "inconclusive needs only the common fields", report: Report{Verdict: Inconclusive, Confidence: Low, Reason: "no data"}},
		{name: "duplicate", report: Report{Verdict: Duplicate, Confidence: High, Reason: "same as INC-1"}},
		{name: "known issue", report: Report{Verdict: KnownIssue, Confidence: Medium, Reason: "tracked in SK-9"}},
		{name: "flapping", report: Report{Verdict: Flapping, Confidence: Medium, Reason: "fired 9 times in an hour"}},
		{
			name:     "unknown verdict",
			report:   Report{Verdict: "maybe", Confidence: Low, Reason: "unsure"},
			wantErrs: []string{`unknown verdict "maybe"`},
		},
		{
			name:     "unknown confidence",
			report:   Report{Verdict: Inconclusive, Confidence: "certain", Reason: "unsure"},
			wantErrs: []string{`unknown confidence "certain"`},
		},
		{
			name:     "missing reason",
			report:   Report{Verdict: Inconclusive, Confidence: Low},
			wantErrs: []string{"reason is required"},
		},
		{
			name:     "every problem is reported at once",
			report:   Report{Verdict: TruePositive},
			wantErrs: []string{"true_positive needs", "unknown confidence", "reason is required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.report.Validate(tt.rules)
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalid)

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

// with returns a copy of r changed by edit.
func with(r Report, edit func(*Report)) Report {
	edit(&r)

	return r
}

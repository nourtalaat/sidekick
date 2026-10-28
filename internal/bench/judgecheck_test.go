package bench

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// judgeCheckDir holds the hand-graded reports that ship with Sidekick.
const judgeCheckDir = "../../bench/judge-check"

// judgeCase is a case file with ID and GRADES to replace.
const judgeCase = `{
  "id": "ID",
  "rootCause": {"summary": "The pool ran out.", "rubric": [
    {"id": "pool", "text": "Names the pool", "weight": 2, "required": true},
    {"id": "fix", "text": "Proposes a fix", "weight": 1}
  ]},
  "report": {"verdict": "true_positive", "confidence": "high", "reason": "r", "summary": "s", "findings": [], "checked": [], "stopReason": "submitted"},
  "grades": GRADES
}`

func TestLoadJudgeCasesShipped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantIDs []string
	}{
		{name: "the shipped cases load", wantIDs: []string{"pool-full", "pool-no-commit", "pool-no-fix", "pool-wrong-cause"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cases, err := LoadJudgeCases(judgeCheckDir)
			require.NoError(t, err)

			ids := make([]string, len(cases))
			for i, c := range cases {
				ids[i] = c.ID
				require.Len(t, c.Grades, len(c.RootCause.Rubric))
			}

			require.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestLoadJudgeCasesProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		wantErr string
	}{
		{
			name:    "a rubric item without a hand grade",
			file:    strings.NewReplacer("ID", "a", "GRADES", `{"pool": true}`).Replace(judgeCase),
			wantErr: `grades: "fix" has no hand grade`,
		},
		{
			name:    "a hand grade of no rubric item",
			file:    strings.NewReplacer("ID", "a", "GRADES", `{"pool": true, "fix": true, "extra": false}`).Replace(judgeCase),
			wantErr: `grades: "extra" is not a rubric item`,
		},
		{
			name:    "a bad ID",
			file:    strings.NewReplacer("ID", "Not OK", "GRADES", `{"pool": true, "fix": true}`).Replace(judgeCase),
			wantErr: `id: "Not OK" must be lower-case letters`,
		},
		{
			name:    "an unknown field",
			file:    strings.NewReplacer("ID", "a", `"grades"`, `"colour": 1, "grades"`, "GRADES", `{"pool": true, "fix": true}`).Replace(judgeCase),
			wantErr: `invalid JSON: json: unknown field "colour"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			file := filepath.Join(dir, "case.json")
			require.NoError(t, os.WriteFile(file, []byte(tt.file), 0o600))

			_, err := LoadJudgeCases(dir)
			require.ErrorIs(t, err, ErrInvalidScenario)
			require.ErrorContains(t, err, file+": "+tt.wantErr)
		})
	}
}

func TestCheckJudge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMocks func(g *MockGrader)
		wantErr    error
		want       func(t *testing.T, check *JudgeCheck)
	}{
		{
			name: "agreement with the hand grades",
			setupMocks: func(g *MockGrader) {
				gomock.InOrder(
					// Agrees on both items of the first case.
					g.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "fix", Met: true}}}, nil),
					// Grades fix as met where the person did not.
					g.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "fix", Met: true}}}, nil),
				)
			},
			want: func(t *testing.T, check *JudgeCheck) {
				t.Helper()

				require.Equal(t, 4, check.Items)
				require.Equal(t, 3, check.Agreed)
				requireShare(t, new(0.75), check.Agreement)
				// The second case scores 2/3 by hand and 1 by the judge.
				requireShare(t, new((0+1.0/3)/2), check.MeanScoreDifference)
				require.Equal(t, []string{"fix"}, check.Cases[1].Disagreed)

				var out bytes.Buffer
				require.NoError(t, check.WriteTable(&out))
				require.Contains(t, out.String(), "agreement: 0.75 (3 of 4 items)")
				require.Contains(t, out.String(), "b     2      1       0.67        1.00         fix\n")
			},
		},
		{
			name: "a judge that gives no grades disagrees on every item",
			setupMocks: func(g *MockGrader) {
				g.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).Return(Grade{}, errors.New("no grades")).Times(2)
			},
			want: func(t *testing.T, check *JudgeCheck) {
				t.Helper()

				require.Zero(t, check.Agreed)
				require.Equal(t, "no grades", check.Cases[0].Error)
				require.Equal(t, []string{"pool", "fix"}, check.Cases[0].Disagreed)
			},
		},
		{
			name: "an unreachable judge stops the check",
			setupMocks: func(g *MockGrader) {
				g.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).Return(Grade{}, ErrProviderUnreachable)
			},
			wantErr: ErrInfrastructure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grader := NewMockGrader(gomock.NewController(t))
			tt.setupMocks(grader)

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"),
				[]byte(strings.NewReplacer("ID", "a", "GRADES", `{"pool": true, "fix": true}`).Replace(judgeCase)), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"),
				[]byte(strings.NewReplacer("ID", "b", "GRADES", `{"pool": true, "fix": false}`).Replace(judgeCase)), 0o600))

			cases, err := LoadJudgeCases(dir)
			require.NoError(t, err)

			check, err := CheckJudge(t.Context(), grader, cases)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			tt.want(t, check)
		})
	}
}

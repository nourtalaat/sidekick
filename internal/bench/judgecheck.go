package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/nourtalaat/sidekick/internal/report"
)

// judgeCaseExtension is the file extension of the cases LoadJudgeCases
// reads in a directory.
const judgeCaseExtension = ".json"

// JudgeCase is one hand-graded report, a file of bench/judge-check:
// an expected root cause with its rubric, a report, and the grade a person
// gave each rubric item.
type JudgeCase struct {
	ID        string        `json:"id"`
	RootCause RootCause     `json:"rootCause"`
	Report    report.Report `json:"report"`
	// Grades are the hand grades, by rubric item ID: true for met.
	Grades map[string]bool `json:"grades"`

	// Path is the file the case was read from.
	Path string `json:"-"`
}

// JudgeCheck is how far the judge's grades of the cases agree with the
// hand grades.
type JudgeCheck struct {
	Cases []JudgeCaseResult
	// Items counts the rubric items of every case, and Agreed those the
	// judge graded as the person did.
	Items  int
	Agreed int
	// Agreement is Agreed over Items.
	Agreement *float64
	// MeanScoreDifference is the mean, over the cases, of how far the
	// judge's root-cause score is from the hand-graded one.
	MeanScoreDifference *float64
}

// JudgeCaseResult is the judge's grading of one case.
type JudgeCaseResult struct {
	ID         string
	Items      int
	Agreed     int
	HandScore  float64
	JudgeScore float64
	// Disagreed are the IDs of the items the judge graded otherwise.
	Disagreed []string
	// Error says why the judge gave no grades; every item then counts as
	// disagreed.
	Error string
}

// WriteTable writes one line per case: its items, how many the judge
// graded as the person did, both root-cause scores and the items graded
// otherwise, then the totals.
func (c *JudgeCheck) WriteTable(w io.Writer) error {
	tw := tabwriter.NewWriter(w, tableMinWidth, tableTabWidth, tablePadding, tablePadChar, 0)

	if _, err := fmt.Fprintln(tw, "CASE\tITEMS\tAGREED\tHAND SCORE\tJUDGE SCORE\tGRADED OTHERWISE"); err != nil {
		return fmt.Errorf("write the table header: %w", err)
	}

	for _, r := range c.Cases {
		otherwise := strings.Join(r.Disagreed, ", ")
		if r.Error != "" {
			otherwise = "no grades: " + r.Error
		}

		_, err := fmt.Fprintf(tw, "%s\t%d\t%d\t%.2f\t%.2f\t%s\n", r.ID, r.Items, r.Agreed, r.HandScore, r.JudgeScore, cmpOrDash(otherwise))
		if err != nil {
			return fmt.Errorf("write the row of %s: %w", r.ID, err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write the table: %w", err)
	}

	_, err := fmt.Fprintf(w, "\nagreement: %s (%d of %d items)\nmean root-cause score difference: %s\n",
		shareCell(c.Agreement), c.Agreed, c.Items, shareCell(c.MeanScoreDifference))
	if err != nil {
		return fmt.Errorf("write the totals: %w", err)
	}

	return nil
}

// LoadJudgeCases reads the case file at path, or every .json file under
// the directory at path, in name order, and checks each one. Problems
// give one error that wraps ErrInvalidScenario and lists each one as
// "<file>: <field>: <problem>".
func LoadJudgeCases(path string) ([]JudgeCase, error) {
	files, err := findFiles(path, []string{judgeCaseExtension})
	if err != nil {
		return nil, fmt.Errorf("find the judge-check cases: %w", err)
	}

	var (
		cases    []JudgeCase
		problems []string
	)

	for _, file := range files {
		c, errs := loadJudgeCase(file)
		for _, e := range errs {
			problems = append(problems, file+": "+e)
		}

		if len(errs) == 0 {
			cases = append(cases, c)
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w:\n%s", ErrInvalidScenario, strings.Join(problems, "\n"))
	}

	return cases, nil
}

// CheckJudge has grader grade the report of each case, one at a time, and
// compares its grades with the hand grades. A grading that fails because
// of the model provider, or because ctx ended, stops it with its error;
// any other failure is kept on the case.
func CheckJudge(ctx context.Context, grader Grader, cases []JudgeCase) (*JudgeCheck, error) {
	check := &JudgeCheck{}

	var diffs float64

	for _, c := range cases {
		res, err := checkCase(ctx, grader, c)
		if err != nil {
			return nil, fmt.Errorf("judge-check case %s: %w", c.ID, err)
		}

		check.Cases = append(check.Cases, res)
		check.Items += res.Items
		check.Agreed += res.Agreed

		diff := res.JudgeScore - res.HandScore
		if diff < 0 {
			diff = -diff
		}

		diffs += diff
	}

	check.Agreement = ratio(check.Agreed, check.Items)
	check.MeanScoreDifference = mean(diffs, len(check.Cases))

	return check, nil
}

// checkCase grades one case and compares the grades with the hand ones.
func checkCase(ctx context.Context, grader Grader, c JudgeCase) (JudgeCaseResult, error) {
	hand := rootCauseResult(c.RootCause.Rubric, Grade{Items: c.handGrades()})
	res := JudgeCaseResult{ID: c.ID, Items: len(c.RootCause.Rubric), HandScore: hand.Score}

	grade, err := grader.Grade(ctx, c.RootCause, c.Report)
	if err != nil {
		if errors.Is(err, ErrInfrastructure) || ctx.Err() != nil {
			return JudgeCaseResult{}, fmt.Errorf("grade the report: %w", err)
		}

		res.Error = err.Error()
		for _, item := range c.RootCause.Rubric {
			res.Disagreed = append(res.Disagreed, item.ID)
		}

		return res, nil
	}

	judged := rootCauseResult(c.RootCause.Rubric, grade)
	res.JudgeScore = judged.Score

	for i, item := range c.RootCause.Rubric {
		if judged.Items[i].Met == c.Grades[item.ID] {
			res.Agreed++
		} else {
			res.Disagreed = append(res.Disagreed, item.ID)
		}
	}

	return res, nil
}

// handGrades returns the hand grades as item grades, in rubric order.
func (c JudgeCase) handGrades() []ItemGrade {
	items := make([]ItemGrade, 0, len(c.RootCause.Rubric))
	for _, item := range c.RootCause.Rubric {
		items = append(items, ItemGrade{ID: item.ID, Met: c.Grades[item.ID]})
	}

	return items
}

// loadJudgeCase reads and checks one case file, decoding it strictly: an
// unknown key is a problem.
func loadJudgeCase(file string) (JudgeCase, []string) {
	raw, err := os.ReadFile(file) //nolint:gosec // The user chooses the case files.
	if err != nil {
		return JudgeCase{}, []string{err.Error()}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var c JudgeCase
	if err := dec.Decode(&c); err != nil {
		return JudgeCase{}, []string{"invalid JSON: " + err.Error()}
	}

	if problems := c.validate(); len(problems) > 0 {
		return JudgeCase{}, problems
	}

	c.Path = file

	return c, nil
}

// validate returns the problems of the case as "<field>: <problem>".
func (c JudgeCase) validate() []string {
	var problems []string

	switch {
	case c.ID == "":
		problems = append(problems, "id: required")
	case !idPattern.MatchString(c.ID):
		problems = append(problems, fmt.Sprintf("id: %q must be lower-case letters, digits, '.', '_' and '-', at most 100 of them", c.ID))
	}

	if len(c.RootCause.Rubric) == 0 {
		problems = append(problems, "rootCause.rubric: required")
	}

	problems = append(problems, validateRubric("rootCause.rubric", c.RootCause.Rubric)...)

	if c.Report.Verdict == "" {
		problems = append(problems, "report.verdict: required")
	}

	for _, item := range c.RootCause.Rubric {
		if _, ok := c.Grades[item.ID]; !ok && item.ID != "" {
			problems = append(problems, fmt.Sprintf("grades: %q has no hand grade", item.ID))
		}
	}

	for _, id := range slices.Sorted(maps.Keys(c.Grades)) {
		if !slices.ContainsFunc(c.RootCause.Rubric, func(item RubricItem) bool { return item.ID == id }) {
			problems = append(problems, fmt.Sprintf("grades: %q is not a rubric item", id))
		}
	}

	return problems
}

// cmpOrDash returns s, or - when it is empty.
func cmpOrDash(s string) string {
	if s == "" {
		return noValue
	}

	return s
}

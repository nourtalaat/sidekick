package bench

import (
	"context"
	_ "embed" // The judge prompt is embedded.
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/report"
)

// JudgePromptVersion is the version of the judge prompt: judge.md and the
// layout of the grading request. Each benchmark report records it, so
// grades always refer to an exact prompt. Change it whenever the prompt
// changes; a test fails until judge.md and this version agree.
const JudgePromptVersion = "1"

// SubmitGradesTool is the tool the judge answers with.
const SubmitGradesTool = "submit_grades"

const (
	// maxJudgeAttempts is how many model turns the judge gets to give
	// valid grades for one report.
	maxJudgeAttempts = 3
	// reportSource names the report in its untrusted data block.
	reportSource = "investigation"
)

const (
	submitGradesDescription = "Submit one grade for every rubric point of the expected root cause: whether the report meets it, and a one-line reason."
	// noGradesText is sent back when a turn did not call submit_grades.
	noGradesText = "Call submit_grades now, with one grade for every rubric point."
	// otherToolText answers a call to any tool other than submit_grades.
	otherToolText = "Only submit_grades is available."
)

// judgePrompt is the judge's system prompt.
//
//go:embed judge.md
var judgePrompt string

// submitGradesSchema is the input schema of submit_grades.
var submitGradesSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["grades"],
  "properties": {
    "grades": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "met", "reason"],
        "properties": {
          "id": {"type": "string"},
          "met": {"type": "boolean"},
          "reason": {"type": "string", "maxLength": 300}
        }
      }
    }
  }
}`)

// errNoGrades is a judge that gave no valid grades in its attempts.
var errNoGrades = fmt.Errorf("the judge gave no valid grades in %d attempts", maxJudgeAttempts)

// Judge grades root causes with the judge role's model. It sees the
// expected root cause, the rubric and the final report, never the
// transcript, and answers through submit_grades.
type Judge struct {
	conn    model.Connector
	retries int
	name    string
	modelID string
	prices  pricing.Table
	schema  *tools.InputSchema
}

var _ Grader = (*Judge)(nil)

// JudgeOptions set up a Judge.
type JudgeOptions struct {
	// Connector is the judge role's model connector, and Retries how many
	// times a call that failed with a retryable error is repeated.
	Connector model.Connector
	Retries   int
	// Name is the connector's name under model.connectors.
	Name string
	// Model is the connector's model ID, which prices its calls. Empty
	// means the model ID of each response.
	Model  string
	Prices pricing.Table
}

// graded is the part of a report the judge sees: what it concludes and
// why, without the checked sources and stop reason Sidekick adds.
type graded struct {
	Verdict       report.Verdict              `json:"verdict"`
	Confidence    report.Confidence           `json:"confidence"`
	Summary       string                      `json:"summary"`
	Reason        string                      `json:"reason"`
	Cause         string                      `json:"cause,omitempty"`
	Impact        string                      `json:"impact,omitempty"`
	Findings      []gradedFinding             `json:"findings"`
	Fixes         []report.Fix                `json:"fixes,omitempty"`
	FalsePositive *report.FalsePositiveDetail `json:"falsePositive,omitempty"`
}

// gradedFinding is a finding with the titles and URLs of its evidence.
type gradedFinding struct {
	Claim    string   `json:"claim"`
	Evidence []string `json:"evidence,omitempty"`
}

// Grade grades rep against the expected root cause. The judge gets
// maxJudgeAttempts turns to give one valid grade for every rubric item;
// each problem goes back to it to fix. A model call that fails after its
// retries gives an error that wraps ErrProviderUnreachable, and a request
// the provider refuses, or grades that are still invalid after the last
// attempt, an error that does not.
func (j *Judge) Grade(ctx context.Context, expected RootCause, rep report.Report) (Grade, error) {
	req, err := j.request(expected, rep)
	if err != nil {
		return Grade{}, fmt.Errorf("build the grading request: %w", err)
	}

	var grade Grade

	for range maxJudgeAttempts {
		resp, err := model.Generate(ctx, j.conn, req, j.retries)
		if err != nil {
			return Grade{}, j.callError(ctx, err)
		}

		j.count(&grade, resp)

		items, answers, ok := j.answer(resp.Message, expected.Rubric)
		if ok {
			grade.Items = items

			return grade, nil
		}

		req.Messages = append(req.Messages, resp.Message, model.Message{Role: model.User, Parts: answers})
	}

	return Grade{}, errNoGrades
}

// Name returns the name of the judge's connector.
func (j *Judge) Name() string {
	return j.name
}

// request returns the first request of a grading: the judge prompt, the
// expected root cause with its rubric, and the report as untrusted data.
// It forces submit_grades where the connector can force a tool, and sets
// temperature 0 where the model takes a temperature.
func (j *Judge) request(expected RootCause, rep report.Report) (model.Request, error) {
	body, err := json.MarshalIndent(gradedReport(rep), "", "  ")
	if err != nil {
		return model.Request{}, fmt.Errorf("encode the report: %w", err)
	}

	var b strings.Builder

	b.WriteString("Expected root cause:\n" + expected.Summary + "\n\nRubric:\n")

	for _, item := range expected.Rubric {
		required := ""
		if item.Required {
			required = ", required"
		}

		fmt.Fprintf(&b, "- %s (weight %g%s): %s\n", item.ID, item.Weight, required, item.Text)
	}

	b.WriteString("\nThe report to grade:\n" + prompt.WrapUntrusted(reportSource, "", "", string(body)))

	caps := j.conn.Capabilities()
	req := model.Request{
		System:   []model.Text{{Text: judgePrompt, Cache: caps.PromptCaching}},
		Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: b.String()}}}},
		Tools:    []model.ToolSpec{{Name: SubmitGradesTool, Description: submitGradesDescription, InputSchema: submitGradesSchema}},
		Metadata: map[string]string{model.MetadataRole: string(model.RoleJudge)},
	}

	if caps.ToolChoice {
		req.ToolChoice = model.ToolChoice{Mode: model.ToolChoiceRequired, Name: SubmitGradesTool}
	}

	if caps.Temperature {
		req.Temperature = new(0.0)
	}

	return req, nil
}

// callError returns the error of a model call that failed: ctx's when it
// ended, one that wraps ErrProviderUnreachable when the provider could not
// be reached after the retries, and a plain one when it refused the
// request.
func (j *Judge) callError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("judge model call: %w", context.Cause(ctx))
	}

	if merr, ok := errors.AsType[*model.Error](err); ok && (merr.Kind == model.ErrInvalid || merr.Kind == model.ErrContextLength) {
		return fmt.Errorf("the judge model refused the request (%s): %w", merr.Kind, err)
	}

	return fmt.Errorf("judge model call: %w: %w", ErrProviderUnreachable, err)
}

// count adds the tokens and cost of a judge turn to grade.
func (j *Judge) count(grade *Grade, resp model.Response) {
	grade.InputTokens += resp.Usage.PromptTokens()
	grade.OutputTokens += resp.Usage.OutputTokens

	id := j.modelID
	if id == "" {
		id = resp.Model
	}

	if cost, ok := j.prices.Cost(id, resp.Usage); ok {
		grade.CostMicros += cost
	}
}

// answer reads the grades from a judge turn. It returns them and true when
// the turn called submit_grades with one valid grade for each rubric item.
// Otherwise it returns the parts that tell the judge what to fix: a result
// for each of its tool calls, or a reminder when it made none.
func (j *Judge) answer(msg model.Message, rubric []RubricItem) ([]ItemGrade, []model.Part, bool) {
	var (
		answers []model.Part
		items   []ItemGrade
		valid   bool
	)

	for _, part := range msg.Parts {
		call, ok := part.(model.ToolCall)
		if !ok {
			continue
		}

		if call.Name != SubmitGradesTool || valid {
			answers = append(answers, model.ToolResult{CallID: call.ID, Content: otherToolText, IsError: true})

			continue
		}

		grades, problems := j.check(call.Input, rubric)
		if len(problems) > 0 {
			answers = append(answers, model.ToolResult{CallID: call.ID, Content: strings.Join(problems, "\n"), IsError: true})

			continue
		}

		items, valid = grades, true
	}

	if valid {
		return items, nil, true
	}

	if len(answers) == 0 {
		answers = []model.Part{model.Text{Text: noGradesText}}
	}

	return nil, answers, false
}

// check checks the input of a submit_grades call against its schema and
// the rubric: each item graded exactly once, and no other. It returns the
// grades in rubric order, or the problems the judge must fix.
func (j *Judge) check(input json.RawMessage, rubric []RubricItem) ([]ItemGrade, []string) {
	input, err := j.schema.Check(input)
	if err != nil {
		return nil, []string{err.Error()}
	}

	var submitted struct {
		Grades []ItemGrade `json:"grades"`
	}

	if err := json.Unmarshal(input, &submitted); err != nil {
		return nil, []string{"the input does not decode: " + err.Error()}
	}

	byID := make(map[string]ItemGrade, len(submitted.Grades))
	known := make(map[string]bool, len(rubric))

	for _, item := range rubric {
		known[item.ID] = true
	}

	var problems []string

	for _, g := range submitted.Grades {
		switch {
		case !known[g.ID]:
			problems = append(problems, fmt.Sprintf("%q is not a rubric point", g.ID))
		case byID[g.ID].ID != "":
			problems = append(problems, fmt.Sprintf("%q is graded twice", g.ID))
		default:
			byID[g.ID] = g
		}
	}

	grades := make([]ItemGrade, 0, len(rubric))

	for _, item := range rubric {
		g, ok := byID[item.ID]
		if !ok {
			problems = append(problems, fmt.Sprintf("%q has no grade", item.ID))

			continue
		}

		grades = append(grades, g)
	}

	if len(problems) > 0 {
		return nil, problems
	}

	return grades, nil
}

// NewJudge returns a Judge.
func NewJudge(opts JudgeOptions) (*Judge, error) {
	if opts.Connector == nil {
		return nil, errors.New("new judge: Connector is required")
	}

	schema, err := tools.ParseInputSchema(submitGradesSchema)
	if err != nil {
		return nil, fmt.Errorf("new judge: %w", err)
	}

	return &Judge{
		conn:    opts.Connector,
		retries: opts.Retries,
		name:    opts.Name,
		modelID: opts.Model,
		prices:  opts.Prices,
		schema:  schema,
	}, nil
}

// gradedReport returns the part of rep the judge sees.
func gradedReport(rep report.Report) graded {
	out := graded{
		Verdict:       rep.Verdict,
		Confidence:    rep.Confidence,
		Summary:       rep.Summary,
		Reason:        rep.Reason,
		Cause:         rep.Cause,
		Impact:        rep.Impact,
		Fixes:         rep.Fixes,
		FalsePositive: rep.FalsePositive,
		Findings:      make([]gradedFinding, 0, len(rep.Findings)),
	}

	for _, f := range rep.Findings {
		gf := gradedFinding{Claim: f.Claim}
		for _, e := range f.Evidence {
			gf.Evidence = append(gf.Evidence, strings.TrimSpace(e.Title+" "+e.URL))
		}

		out.Findings = append(out.Findings, gf)
	}

	return out
}

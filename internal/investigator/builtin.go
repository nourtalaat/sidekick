package investigator

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// RecordFindingTool is the name of the built-in tool that records one
// finding while the investigation runs.
const RecordFindingTool = "record_finding"

// maxReportTries is how many reports the model may submit that fail their
// checks before the run ends as inconclusive.
const maxReportTries = 3

// recordFindingSchema is the input schema of record_finding. Like the
// findings of submit_verdict, a finding cites the IDs of the tool calls
// that support it, and Sidekick resolves them to evidence.
var recordFindingSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["claim", "evidenceToolCallIds"],
  "properties": {
    "claim": {"type": "string", "maxLength": 500},
    "evidenceToolCallIds": {"type": "array", "items": {"type": "string"}}
  }
}`)

// Descriptions of the built-in tools.
const (
	submitVerdictDescription = "End the investigation with your report. Each finding cites the IDs of the tool calls " +
		"whose results support it; a finding that cites none is shown as a hypothesis. Never write URLs: Sidekick links " +
		"the evidence from the cited calls. If the report fails its checks you get the errors back and can try again."
	recordFindingDescription = "Record one finding as soon as you have it, so responders see it while you keep " +
		"investigating. Cite the IDs of the tool calls whose results support it. Recording a finding does not end the " +
		"investigation."
	answerFollowUpDescription = "Answer the follow-up questions in a few sentences, without changing your verdict. " +
		"Cite the IDs of the tool calls whose results support the answer, from this run or an earlier one. Never " +
		"write URLs. If the answer changes your verdict, call submit_verdict with a new report instead."
)

// notFollowUpText is the problem of an answer_follow_up call outside a
// follow-up run.
const notFollowUpText = "answer_follow_up is only available in a follow-up run; call submit_verdict"

// builtins are the built-in tools with their parsed input schemas.
type builtins struct {
	submitVerdict  *tools.InputSchema
	recordFinding  *tools.InputSchema
	answerFollowUp *tools.InputSchema
}

// followUpAnswer is the input of answer_follow_up.
type followUpAnswer struct {
	Answer    string   `json:"answer"`
	Citations []string `json:"evidenceToolCallIds"`
}

// submission is the input of submit_verdict.
type submission struct {
	Verdict       report.Verdict              `json:"verdict"`
	Confidence    report.Confidence           `json:"confidence"`
	Reason        string                      `json:"reason"`
	Summary       string                      `json:"summary"`
	Cause         string                      `json:"cause"`
	Impact        string                      `json:"impact"`
	Findings      []citedFinding              `json:"findings"`
	Fixes         []submittedFix              `json:"fixes"`
	FalsePositive *report.FalsePositiveDetail `json:"falsePositive"`
}

// citedFinding is a finding as the model writes it: a claim and the IDs of
// the tool calls that support it. It is also the input of record_finding.
type citedFinding struct {
	Claim     string   `json:"claim"`
	Citations []string `json:"evidenceToolCallIds"`
}

// submittedFix is a fix as the model writes it.
type submittedFix struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// specs returns the tool specs of the built-in tools.
func (b builtins) specs() []model.ToolSpec {
	return []model.ToolSpec{
		{Name: report.SubmitVerdictTool, Description: submitVerdictDescription, InputSchema: report.SubmitVerdictSchema},
		{Name: RecordFindingTool, Description: recordFindingDescription, InputSchema: recordFindingSchema},
	}
}

// answerSpec returns the tool spec of answer_follow_up, which only
// follow-up runs offer.
func (b builtins) answerSpec() model.ToolSpec {
	return model.ToolSpec{Name: report.AnswerFollowUpTool, Description: answerFollowUpDescription, InputSchema: report.AnswerFollowUpSchema}
}

// answer checks the input of an answer_follow_up call, and returns the
// answer with the evidence of the calls it cites, or the problems the
// model must fix.
func (b builtins) answer(h *history, call model.ToolCall) (text string, evidence []source.Evidence, problems []string) {
	input, err := b.answerFollowUp.Check(call.Input)
	if err != nil {
		return "", nil, []string{err.Error()}
	}

	var a followUpAnswer
	if err := json.Unmarshal(input, &a); err != nil {
		return "", nil, []string{"the input does not decode: " + err.Error()}
	}

	if strings.TrimSpace(a.Answer) == "" {
		problems = append(problems, "answer is empty")
	}

	f, bad := h.finding(0, citedFinding{Claim: a.Answer, Citations: a.Citations})

	return a.Answer, f.Evidence, append(problems, bad...)
}

// submitted checks the input of a submit_verdict call against its schema,
// the report rules and the saved steps, and returns the report, or the
// problems the model must fix. The report's Checked and StopReason are left
// for the caller.
func (b builtins) submitted(h *history, call model.ToolCall, rules report.Rules) (report.Report, []string) {
	input, err := b.submitVerdict.Check(call.Input)
	if err != nil {
		return report.Report{}, []string{err.Error()}
	}

	var s submission
	if err := json.Unmarshal(input, &s); err != nil {
		return report.Report{}, []string{"the input does not decode: " + err.Error()}
	}

	r := report.Report{
		Verdict:       s.Verdict,
		Confidence:    s.Confidence,
		Reason:        s.Reason,
		Summary:       s.Summary,
		Cause:         s.Cause,
		Impact:        s.Impact,
		FalsePositive: s.FalsePositive,
	}

	var problems []string

	for i, f := range s.Findings {
		finding, bad := h.finding(i, f)
		r.Findings = append(r.Findings, finding)
		problems = append(problems, bad...)
	}

	for i, f := range s.Fixes {
		r.Fixes = append(r.Fixes, report.Fix{ID: fmt.Sprintf("x%d", i+1), Title: f.Title, Detail: f.Detail})
	}

	if err := r.Validate(rules); err != nil {
		problems = append(problems, err.Error())
	}

	return r, problems
}

// recorded checks the input of a record_finding call, and returns the
// finding, or the problems the model must fix. n is the number of findings
// recorded before it.
func (b builtins) recorded(h *history, call model.ToolCall, n int) (report.Finding, []string) {
	input, err := b.recordFinding.Check(call.Input)
	if err != nil {
		return report.Finding{}, []string{err.Error()}
	}

	var f citedFinding
	if err := json.Unmarshal(input, &f); err != nil {
		return report.Finding{}, []string{"the input does not decode: " + err.Error()}
	}

	return h.finding(n, f)
}

// finding resolves the citations of finding number i (from 0) to evidence:
// the links the source returned for each cited call, or a reference to the
// call's step when it returned none. It returns the problems for citations
// that are not a source call that returned data.
func (h *history) finding(i int, f citedFinding) (report.Finding, []string) {
	out := report.Finding{ID: fmt.Sprintf("f%d", i+1), Claim: f.Claim}

	var problems []string

	for _, id := range f.Citations {
		step, ok := h.results[id]
		if !ok || !citable(step) {
			problems = append(problems, fmt.Sprintf("finding %d cites %q, which is not the ID of an earlier tool call that returned data", i+1, id))

			continue
		}

		out.Evidence = append(out.Evidence, evidenceOf(step)...)
	}

	return out, problems
}

// recordedFindings returns the findings that record_finding accepted, in
// order.
func (h *history) recordedFindings() []report.Finding {
	var out []report.Finding

	for _, s := range h.steps {
		if !isBuiltin(s, RecordFindingTool) || s.Status != statusOK {
			continue
		}

		f, ok := decodeFinding(h.calls[*s.ToolCallID])
		if !ok {
			continue
		}

		finding, _ := h.finding(len(out), f)
		out = append(out, finding)
	}

	return out
}

// decodeFinding returns the input of a record_finding call that was
// accepted, so it decodes.
func decodeFinding(call model.ToolCall) (citedFinding, bool) {
	var f citedFinding
	if err := json.Unmarshal(call.Input, &f); err != nil {
		return citedFinding{}, false
	}

	return f, true
}

// citable reports whether a tool result step can be cited: a source call
// that returned data or found none.
func citable(s db.Step) bool {
	return s.Source != nil && (s.Status == statusOK || s.Status == statusNoData)
}

// evidenceOf returns the evidence of a cited step.
func evidenceOf(s db.Step) []source.Evidence {
	var links []source.Evidence
	if len(s.Evidence) > 0 && json.Unmarshal(s.Evidence, &links) == nil && len(links) > 0 {
		return links
	}

	return []source.Evidence{{Kind: source.EvidenceOther, Title: deref(s.Source) + " " + deref(s.Tool), ToolCallID: deref(s.ToolCallID)}}
}

// isBuiltin reports whether s is the result of a call to the built-in tool
// name.
func isBuiltin(s db.Step, name string) bool {
	return s.Kind == kindToolResult && s.Source == nil && s.Tool != nil && *s.Tool == name && s.ToolCallID != nil
}

// isBuiltinName reports whether name is a built-in tool.
func isBuiltinName(name string) bool {
	return slices.Contains([]string{report.SubmitVerdictTool, RecordFindingTool, watch.StartWatcherTool, report.AnswerFollowUpTool}, name)
}

// specNames returns the names of specs.
func specNames(specs []model.ToolSpec) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}

	return names
}

// rejectedText is what the model sees for a built-in call that failed its
// checks.
func rejectedText(what string, problems []string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "The %s did not pass its checks. Fix these and call the tool again:", what)

	for _, p := range problems {
		b.WriteString("\n- " + p)
	}

	return b.String()
}

// deref returns *p, or the zero value for nil.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}

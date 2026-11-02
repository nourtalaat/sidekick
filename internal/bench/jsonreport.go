package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
)

// ReportSchemaVersion is the version of the report.json schema in
// docs/bench-report.schema.json. Change it, and the schema, whenever the
// report changes in a way an older reader would misread.
const ReportSchemaVersion = 1

// jsonIndent is the indent of report.json.
const jsonIndent = "  "

// ErrInvalidReport is wrapped by every error about a report.json that
// cannot be read, so the command can exit with its bad input code.
var ErrInvalidReport = errors.New("invalid benchmark report")

// errNotScored is a report asked for before Score ran.
var errNotScored = errors.New("the results are not scored yet")

// RunInfo is what a benchmark run records about how it ran, so runs of
// different models, prompts, strategies and versions can be compared.
type RunInfo struct {
	// Label is a name the user gave the run.
	Label           string `json:"label,omitempty"`
	SidekickVersion string `json:"sidekickVersion"`
	GitSHA          string `json:"gitSHA"`
	// ConfigHash is a hash of the config file.
	ConfigHash string `json:"configHash"`
	// ScenarioSet is the --scenarios path.
	ScenarioSet string `json:"scenarioSet"`
	// TriggeredBy is cli, or ci when the run is in CI.
	TriggeredBy string `json:"triggeredBy"`
	// Connectors are the model connectors of the roles, by role.
	Connectors map[model.Role]ConnectorInfo `json:"connectors"`
	// Strategies are the versions of the strategies the scenarios use, by
	// name.
	Strategies         map[string]string `json:"strategies"`
	PromptVersion      string            `json:"promptVersion"`
	JudgePromptVersion string            `json:"judgePromptVersion"`
}

// ConnectorInfo is the connector of one role and its model settings.
type ConnectorInfo struct {
	Connector   string             `json:"connector"`
	Provider    string             `json:"provider"`
	Model       string             `json:"model,omitempty"`
	MaxTokens   int                `json:"maxTokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    model.ThinkingMode `json:"thinking,omitempty"`
	Effort      model.Effort       `json:"effort,omitempty"`
}

// JSONReport is report.json: the run's settings, its totals, and each
// scenario with its spread and runs.
type JSONReport struct {
	SchemaVersion int `json:"schemaVersion"`
	RunInfo
	StartedAt       time.Time           `json:"startedAt"`
	FinishedAt      time.Time           `json:"finishedAt"`
	Repeat          int                 `json:"repeat"`
	Seed            uint64              `json:"seed"`
	Resamples       int                 `json:"resamples"`
	ConfidenceLevel float64             `json:"confidenceLevel"`
	Weights         Weights             `json:"weights"`
	Metrics         Metrics             `json:"metrics"`
	Intervals       map[string]Interval `json:"intervals"`
	// Thresholds are the checks of the thresholds of bench.yaml.
	Thresholds []ThresholdResult `json:"thresholds"`
	Scenarios  []ScenarioReport  `json:"scenarios"`
}

// ScenarioReport is one scenario in report.json.
type ScenarioReport struct {
	ID              string           `json:"id"`
	Title           string           `json:"title,omitempty"`
	Path            string           `json:"path,omitempty"`
	ExpectedVerdict report.Verdict   `json:"expectedVerdict"`
	Acceptable      []report.Verdict `json:"acceptable,omitempty"`
	Spread          Spread           `json:"spread"`
	Runs            []RunReport      `json:"runs"`
}

// RunReport is one run in report.json.
type RunReport struct {
	Run             int               `json:"run"`
	InvestigationID uuid.UUID         `json:"investigationId"`
	Status          string            `json:"status"`
	Verdict         report.Verdict    `json:"verdict"`
	Confidence      report.Confidence `json:"confidence,omitempty"`
	StopReason      report.StopReason `json:"stopReason"`
	VerdictCorrect  bool              `json:"verdictCorrect"`
	RootCause       *RootCauseReport  `json:"rootCause"`
	Evidence        []EvidenceReport  `json:"evidence,omitempty"`
	EvidenceScore   *float64          `json:"evidenceScore"`
	SourcesReported *bool             `json:"sourcesReported"`
	MissingSources  []string          `json:"missingSources,omitempty"`
	ToolCalls       int               `json:"toolCalls"`
	InputTokens     int64             `json:"inputTokens"`
	OutputTokens    int64             `json:"outputTokens"`
	CostMicros      int64             `json:"costMicros"`
	DurationMs      int64             `json:"durationMs"`

	// Reason is the reason the run gave for its verdict. StopDetail says
	// why a run that ended without a verdict stopped, such as the last
	// check its report failed.
	Reason     string `json:"reason,omitempty"`
	StopDetail string `json:"stopDetail,omitempty"`

	// Tools are the source tools the run called, in call order. Each holds
	// only the tool's name and how the call ended; never the arguments or
	// the result text, which can carry customer data.
	Tools []ToolCallReport `json:"tools,omitempty"`
}

// ToolCallReport is one source tool call of a run in report.json: the tool
// as the model names it, for example github__list_commits, and the status
// of the call, such as ok, no_data or unavailable.
type ToolCallReport struct {
	Tool   string `json:"tool"`
	Status string `json:"status"`
}

// RootCauseReport is the graded root cause of a run in report.json.
type RootCauseReport struct {
	Score       float64     `json:"score"`
	RequiredMet bool        `json:"requiredMet"`
	Match       bool        `json:"match"`
	Items       []ItemGrade `json:"items"`
	Error       string      `json:"error,omitempty"`
	CostMicros  int64       `json:"costMicros"`
}

// EvidenceReport is one evidence check of a run in report.json.
type EvidenceReport struct {
	CalledTool string `json:"calledTool,omitempty"`
	CitedURL   string `json:"citedURL,omitempty"`
	Passed     bool   `json:"passed"`
}

// Report returns report.json for the scored results, with info and the
// checks of the thresholds.
func (r *Results) Report(info RunInfo, thresholds []ThresholdResult) (*JSONReport, error) {
	if r.Summary == nil {
		return nil, errNotScored
	}

	out := &JSONReport{
		SchemaVersion:   ReportSchemaVersion,
		RunInfo:         info,
		StartedAt:       r.StartedAt.UTC(),
		FinishedAt:      r.FinishedAt.UTC(),
		Repeat:          r.Repeat,
		Seed:            r.Summary.Seed,
		Resamples:       r.Summary.Resamples,
		ConfidenceLevel: ConfidenceLevel,
		Weights:         r.Summary.Weights,
		Metrics:         r.Summary.Metrics,
		Intervals:       r.Summary.Intervals,
		Thresholds:      thresholds,
		Scenarios:       make([]ScenarioReport, 0, len(r.Scenarios)),
	}

	if out.Intervals == nil {
		out.Intervals = map[string]Interval{}
	}

	if out.Thresholds == nil {
		out.Thresholds = []ThresholdResult{}
	}

	for _, sc := range r.Scenarios {
		out.Scenarios = append(out.Scenarios, sc.report())
	}

	return out, nil
}

// WriteJSON writes the report as indented JSON.
func (r *JSONReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", jsonIndent)

	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("write the report: %w", err)
	}

	return nil
}

// report returns the scenario as it is in report.json.
func (s ScenarioResult) report() ScenarioReport {
	out := ScenarioReport{
		ID:              s.ID,
		Title:           s.Title,
		Path:            s.Path,
		ExpectedVerdict: s.Expected.Verdict,
		Acceptable:      s.Expected.Acceptable,
		Runs:            make([]RunReport, 0, len(s.Runs)),
	}

	if s.Spread != nil {
		out.Spread = *s.Spread
	}

	for _, run := range s.Runs {
		out.Runs = append(out.Runs, run.report())
	}

	return out
}

// report returns the run as it is in report.json.
func (r RunResult) report() RunReport {
	out := RunReport{
		Run:             r.Run,
		InvestigationID: r.InvestigationID,
		Status:          r.Status,
		Verdict:         r.Report.Verdict,
		Confidence:      r.Report.Confidence,
		StopReason:      r.Report.StopReason,
		VerdictCorrect:  r.VerdictCorrect,
		EvidenceScore:   r.EvidenceScore,
		SourcesReported: r.SourcesReported,
		MissingSources:  r.MissingSources,
		ToolCalls:       r.ToolCalls,
		InputTokens:     r.InputTokens,
		OutputTokens:    r.OutputTokens,
		CostMicros:      r.CostMicros,
		DurationMs:      r.Duration.Milliseconds(),
		Reason:          r.Report.Reason,
		StopDetail:      r.Report.StopDetail,
	}

	if rc := r.RootCause; rc != nil {
		out.RootCause = &RootCauseReport{
			Score: rc.Score, RequiredMet: rc.RequiredMet, Match: rc.Match,
			Items: rc.Items, Error: rc.Error, CostMicros: rc.CostMicros,
		}
	}

	for _, e := range r.Evidence {
		out.Evidence = append(out.Evidence, EvidenceReport{CalledTool: e.Check.CalledTool, CitedURL: e.Check.CitedURL, Passed: e.Passed})
	}

	for _, t := range r.ToolResults {
		out.Tools = append(out.Tools, ToolCallReport{Tool: t.Tool, Status: t.Status})
	}

	return out
}

// ReadReport reads the report.json at path. A file that cannot be read or
// decoded, or has another schema version, gives an error that wraps
// ErrInvalidReport.
func ReadReport(path string) (*JSONReport, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // The user chooses the report files.
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReport, err)
	}

	var r JSONReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidReport, path, err)
	}

	if r.SchemaVersion != ReportSchemaVersion {
		return nil, fmt.Errorf("%w: %s has schema version %d; this Sidekick reads version %d", ErrInvalidReport, path, r.SchemaVersion, ReportSchemaVersion)
	}

	return &r, nil
}

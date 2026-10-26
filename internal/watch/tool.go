package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
)

// NoData says how a tool check ends when the tool finds no data.
type NoData string

// NoData values.
const (
	// NoDataFail fails the check. It is the default.
	NoDataFail NoData = "fail"
	// NoDataPass passes it, for a tool that finds nothing when all is
	// well, such as a search for error logs.
	NoDataPass NoData = "pass"
)

// toolCallPrefix starts the ID of the tool call of a check.
const toolCallPrefix = "watch-"

// ToolCheck calls a source tool with fixed arguments and passes when the
// result meets the condition. The call goes through the guard like an
// investigation's, so the source's tool filter and read-only rule, the
// tool's input schema, the timeout, the concurrency limit, the circuit
// breaker and redaction all apply.
type ToolCheck struct {
	// Source is the source's name in config.
	Source string `yaml:"source" json:"source"`
	// Tool is the tool's name as the source names it, before tools.rename.
	Tool string `yaml:"tool" json:"tool"`
	// Args is the tool's input: a JSON object.
	Args      map[string]any `yaml:"args" json:"args,omitempty"`
	Condition Condition      `yaml:"condition" json:"condition"`
	// NoData says how a result without data ends: fail (the default) or
	// pass.
	NoData NoData `yaml:"noData" json:"noData,omitempty"`
}

// Type returns TypeTool.
func (ToolCheck) Type() Type {
	return TypeTool
}

// Validate checks that the source and tool are named, that the arguments
// encode as JSON, the condition and noData.
func (t ToolCheck) Validate() error {
	var errs []error

	if t.Source == "" {
		errs = append(errs, errors.New("source: required"))
	}

	if t.Tool == "" {
		errs = append(errs, errors.New("tool: required"))
	}

	if _, err := json.Marshal(t.Args); err != nil {
		errs = append(errs, fmt.Errorf("args: %w", err))
	}

	if err := t.Condition.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("condition: %w", err))
	}

	if t.NoData != "" && !slices.Contains([]NoData{NoDataFail, NoDataPass}, t.NoData) {
		errs = append(errs, fmt.Errorf("noData: expected fail or pass, got %q", t.NoData))
	}

	return errors.Join(errs...)
}

// run calls the tool through the guard and tests the condition.
func (t ToolCheck) run(ctx context.Context, c *Checker, target Target) Outcome {
	cond, err := t.Condition.compile()
	if err != nil {
		return errorOutcome("the condition is not valid: " + err.Error())
	}

	input, err := json.Marshal(t.Args)
	if err != nil {
		return errorOutcome("the arguments do not encode as JSON")
	}

	if t.Args == nil {
		input = json.RawMessage("{}")
	}

	run := c.opts.Guard.Start(ctx, target.InvestigationID, []string{t.Source})

	name, ok := run.QualifiedName(t.Source, t.Tool)
	if !ok {
		return errorOutcome(t.notExposed(run.Checked()))
	}

	res := run.Call(ctx, model.ToolCall{ID: toolCallPrefix + ids.New().String(), Name: name, Input: input})
	if res.Err != nil {
		return errorOutcome(fmt.Sprintf("%s %s: %s", res.Err.Kind, name, res.Err.Message))
	}

	if res.NoData {
		if t.NoData == NoDataPass {
			return Outcome{Result: Pass, Detail: "the tool found no data, which noData: pass accepts"}
		}

		return Outcome{Result: Fail, Detail: "the tool found no data"}
	}

	pass, detail, err := cond.evaluate(res.Text, res.Structured)
	if err != nil {
		return errorOutcome(err.Error())
	}

	if pass {
		return Outcome{Result: Pass, Detail: detail}
	}

	return Outcome{Result: Fail, Detail: detail}
}

// notExposed says why the run has no such tool: the source failed or is
// unknown, or it does not expose the tool.
func (t ToolCheck) notExposed(checked []report.Check) string {
	for _, rec := range checked {
		if rec.Source == t.Source && rec.Error != "" {
			return fmt.Sprintf("the source %s cannot be used: %s", t.Source, rec.Error)
		}
	}

	return fmt.Sprintf("the source %s has no tool %s that checks may call: it is unknown, filtered out, or can change something", t.Source, t.Tool)
}

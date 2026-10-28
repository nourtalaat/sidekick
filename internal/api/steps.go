package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The page sizes of GET /investigations/{id}/steps.
const (
	defaultStepsPage = 100
	maxStepsPage     = 500
)

// maxPartBytes is the most bytes of a step's text, tool input or tool
// result that the API sends. The step in the database keeps all of it.
const maxPartBytes = 32 << 10

// beforeFirstStep is the value of after that starts at the first step.
const beforeFirstStep = -1

// ListInvestigationSteps returns a page of the steps of an investigation,
// in order.
func (s *server) ListInvestigationSteps(ctx context.Context, req apigen.ListInvestigationStepsRequestObject) (apigen.ListInvestigationStepsResponseObject, error) {
	limit := defaultStepsPage
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	if limit < 1 || limit > maxStepsPage {
		return nil, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxStepsPage)
	}

	after := int32(beforeFirstStep)
	if req.Params.After != nil {
		after = *req.Params.After
	}

	if after < beforeFirstStep {
		return nil, problemf(http.StatusBadRequest, "after must be %d or more.", beforeFirstStep)
	}

	queries := db.New(s.pool)

	if _, err := queries.GetInvestigationGroup(ctx, req.Id); errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoInvestigation
	} else if err != nil {
		return nil, fmt.Errorf("read investigation %s: %w", req.Id, err)
	}

	var run *int32
	if req.Params.Run != nil {
		run = new(int32(*req.Params.Run)) //nolint:gosec // A run number that does not fit matches no step.
	}

	rows, err := queries.ListStepsPage(ctx, db.ListStepsPageParams{
		InvestigationID: req.Id, After: after, Run: run, PageSize: int32(limit + 1), //nolint:gosec // limit is at most maxStepsPage.
	})
	if err != nil {
		return nil, fmt.Errorf("list the steps of investigation %s: %w", req.Id, err)
	}

	list := apigen.ListInvestigationSteps200JSONResponse{Steps: make([]apigen.Step, 0, min(len(rows), limit))}

	for i, row := range rows {
		if i == limit {
			list.NextAfter = &rows[i-1].Seq

			break
		}

		step, err := stepOf(row)
		if err != nil {
			return nil, fmt.Errorf("investigation %s: %w", req.Id, err)
		}

		list.Steps = append(list.Steps, step)
	}

	return list, nil
}

// stepOf is the API's view of a saved step.
func stepOf(row db.Step) (apigen.Step, error) {
	parts, err := investigator.StepParts(row)
	if err != nil {
		return apigen.Step{}, fmt.Errorf("read step %d: %w", row.Seq, err)
	}

	step := apigen.Step{
		Seq:        row.Seq,
		Run:        int(row.Run),
		Kind:       apigen.StepKind(row.Kind),
		Status:     apigen.StepStatus(row.Status),
		Source:     row.Source,
		Tool:       row.Tool,
		CallId:     row.ToolCallID,
		Error:      row.Error,
		Connector:  row.Connector,
		Model:      row.Model,
		CostMicros: row.CostMicros,
		StartedAt:  row.StartedAt,
		DurationMs: row.DurationMs,
		Parts:      partsOf(parts),
	}

	if row.InputTokens != nil || row.OutputTokens != nil {
		step.Tokens = &apigen.StepTokens{
			Input: deref(row.InputTokens), Output: deref(row.OutputTokens),
			CacheRead: row.CacheReadTokens, CacheWrite: row.CacheWriteTokens,
		}
	}

	return step, nil
}

// partsOf is the API's view of a step's parts. A reasoning part sends only
// its text, never the signature or the opaque content the provider checks;
// one with no text is left out.
func partsOf(parts []model.Part) []apigen.StepPart {
	out := make([]apigen.StepPart, 0, len(parts))

	for _, part := range parts {
		switch p := part.(type) {
		case model.Text:
			text, cut := cutPart(p.Text)
			out = append(out, apigen.StepPart{Type: apigen.StepPartTypeText, Text: &text, Truncated: truncatedFlag(cut)})
		case model.Reasoning:
			if p.Text == "" {
				continue
			}

			text, cut := cutPart(p.Text)
			out = append(out, apigen.StepPart{Type: apigen.StepPartTypeReasoning, Text: &text, Truncated: truncatedFlag(cut)})
		case model.ToolCall:
			out = append(out, toolCallPart(p))
		case model.ToolResult:
			content, cut := cutPart(p.Content)
			out = append(out, apigen.StepPart{
				Type: apigen.StepPartTypeToolResult, CallId: &p.CallID, Content: &content, IsError: &p.IsError, Truncated: truncatedFlag(cut),
			})
		}
	}

	return out
}

// toolCallPart is the API's view of a tool call. Arguments over
// maxPartBytes are sent as cut text instead of JSON.
func toolCallPart(p model.ToolCall) apigen.StepPart {
	part := apigen.StepPart{Type: apigen.StepPartTypeToolCall, Id: &p.ID, Name: &p.Name}

	if len(p.Input) <= maxPartBytes {
		part.Input = p.Input

		return part
	}

	text, _ := cutPart(string(p.Input))
	part.Input, part.Truncated = text, truncatedFlag(true)

	return part
}

// cutPart cuts text to maxPartBytes, and says whether it did.
func cutPart(text string) (string, bool) {
	cut := source.CutText(text, maxPartBytes)

	return cut, len(cut) < len(text)
}

// truncatedFlag is the truncated field of a part: set when it was cut,
// absent otherwise.
func truncatedFlag(cut bool) *bool {
	if !cut {
		return nil
	}

	return &cut
}

// deref returns the value s points to, or 0.
func deref(s *int64) int64 {
	if s == nil {
		return 0
	}

	return *s
}

package investigator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestChecked(t *testing.T) {
	t.Parallel()

	h := citedHistory(t, source.Evidence{})

	k8s := resultStep(t, model.ToolResult{CallID: "k1"})
	k8s.Source, k8s.Status, k8s.Error = new("k8s"), statusUnavailable, new("the source is not connected")
	require.NoError(t, h.add(withSeq(k8s, int32(len(h.steps))))) //nolint:gosec // A handful of steps.

	tests := []struct {
		name  string
		start []report.Check
		want  []report.Check
	}{
		{
			name: "from the saved results",
			want: []report.Check{
				{Source: "k8s", Status: report.CheckUnavailable, ToolCalls: 1, Error: "the source is not connected"},
				{Source: "signoz", Status: report.CheckOK, ToolCalls: 3},
			},
		},
		{
			name: "with sources that failed at the start of the run",
			start: []report.Check{
				{Source: "github", Status: report.CheckUnavailable, Error: "down"},
				{Source: "signoz", Status: report.CheckOK, ToolCalls: 9},
			},
			want: []report.Check{
				{Source: "github", Status: report.CheckUnavailable, Error: "down"},
				{Source: "k8s", Status: report.CheckUnavailable, ToolCalls: 1, Error: "the source is not connected"},
				{Source: "signoz", Status: report.CheckOK, ToolCalls: 3},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, h.checked(tt.start))
		})
	}
}

func TestProgress(t *testing.T) {
	t.Parallel()

	h := citedHistory(t, source.Evidence{})

	require.Equal(t, []output.Progress{
		{At: testStart, Text: "Checked signoz (search_logs)"},
		{At: testStart, Text: "Checked signoz (search_logs)"},
		{At: testStart, Text: "signoz (search_logs) did not answer in time"},
		{At: testStart, Text: "Hypothesis: seen"},
	}, h.progress())
}

func TestCallLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   string
	}{
		{status: statusOK, want: "Checked signoz (search_logs)"},
		{status: statusNoData, want: "Checked signoz (search_logs): no data"},
		{status: statusTimeout, want: "signoz (search_logs) did not answer in time"},
		{status: statusUnavailable, want: "signoz (search_logs) is unavailable"},
		{status: statusError, want: "signoz (search_logs) failed"},
		{status: statusRejected},
		{status: statusOverBudget},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, callLine("signoz", "search_logs", tt.status))
		})
	}
}

func TestStepStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		res  tools.Result
		want string
	}{
		{name: "ok", want: statusOK},
		{name: "no data", res: tools.Result{NoData: true}, want: statusNoData},
		{name: "timeout", res: tools.Result{Err: &tools.ToolError{Kind: tools.KindTimeout}}, want: statusTimeout},
		{name: "unavailable", res: tools.Result{Err: &tools.ToolError{Kind: tools.KindUnavailable}}, want: statusUnavailable},
		{name: "invalid input", res: tools.Result{Err: &tools.ToolError{Kind: tools.KindInvalidInput}}, want: statusRejected},
		{name: "unknown tool", res: tools.Result{Err: &tools.ToolError{Kind: tools.KindUnknownTool}}, want: statusRejected},
		{name: "tool error", res: tools.Result{Err: &tools.ToolError{Kind: tools.KindToolError}}, want: statusError},
		{name: "failed", res: tools.Result{Err: &tools.ToolError{Kind: tools.KindFailed}}, want: statusError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, stepStatus(tt.res))
		})
	}
}

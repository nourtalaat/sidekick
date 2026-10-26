//go:build integration

package investigator

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// TestOnDead checks that a dead investigate job fails its investigation
// with what was recorded, and publishes it so the outputs show "Could not
// finish".
func TestOnDead(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{
		turn(search("c1"), modeltest.Call("f1", RecordFindingTool, finding)),
		modeltest.Fail(&model.Error{Kind: model.ErrAuth, Err: errors.New("bad key")}),
	}})

	job := h.claim(t)
	require.Error(t, h.handler.Handle(t.Context(), job), "an auth error retries the job")

	require.NoError(t, h.handler.OnDead(t.Context(), job))

	inv := h.investigation(t)
	require.Equal(t, StatusFailed, inv.Status)
	require.Equal(t, "error", deref(inv.StopReason))

	r := h.report(t)
	require.Equal(t, report.StopError, r.StopReason)
	require.Equal(t, deadReason, r.Reason)
	require.Len(t, r.Findings, 1)
	require.Equal(t, []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 1}}, r.Checked)

	seq, phase, body := h.publishState(t)
	require.Equal(t, "final", phase)
	require.EqualValues(t, inv.NextSeq+1, seq)
	require.Equal(t, report.StopError, body.Report.StopReason)

	require.NoError(t, h.handler.OnDead(t.Context(), job), "a second call changes nothing")
	require.Equal(t, inv.FinishedAt, h.investigation(t).FinishedAt)
}

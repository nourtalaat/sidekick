//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// propRollback is the rollback of setupRollbackProposal.
const propRollback = "0192d3a4-0000-7000-8000-0000000000f8"

// revisionBody is a revision as the API returns it.
type revisionBody struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Version    string `json:"version"`
	DeployedAt string `json:"deployedAt"`
	DeployedBy string `json:"deployedBy"`
	Notes      string `json:"notes"`
}

// rollbackProposalBody is the part of a proposal the revision tests read.
type rollbackProposalBody struct {
	ID        string `json:"id"`
	Revisions *struct {
		Running    revisionBody `json:"running"`
		GoesBackTo revisionBody `json:"goesBackTo"`
	} `json:"revisions"`
	Changes []undoChange `json:"changes"`
}

// setupRollbackProposal returns a server with the succeeded proposal
// propRollback, whose snapshot holds two revisions. The running one has a
// hidden character in its version and the other has no deploy time and no
// deployer.
func setupRollbackProposal(t *testing.T) server {
	t.Helper()

	s := setupWith(t, nil, "fixture/proposals.sql")

	deployed := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	running := action.Revision{Kind: "ReplicaSet revision", ID: "7", Version: "checkout:1.4.7" + string(rune(0x200b)), DeployedAt: &deployed, DeployedBy: "ana"}
	back := action.Revision{Kind: "ReplicaSet revision", ID: "6", Version: "checkout:1.4.6"}

	snap, err := action.RevisionSnapshot("fake:workload/checkout", "7", running, back)
	require.NoError(t, err)

	raw, err := json.Marshal(snap)
	require.NoError(t, err)

	_, err = s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_by, decided_at, executed_at, result, snapshot, undoable)
		VALUES ($1::uuid, '0192d3a4-0000-7000-8000-0000000000e2', 'rollback', '{"back": 1}', 'The errors began with revision 7.', 'approved', 'succeeded',
		        'sidekick/' || $1::uuid::text, now() - interval '2 hours', now() + interval '22 hours',
		        '01920000-0000-7000-8000-000000000070', now() - interval '110 minutes', now() - interval '100 minutes',
		        '{"summary": "Rolled back."}', $2::jsonb, true)`, propRollback, raw)
	require.NoError(t, err)

	return s
}

func TestProposalShowsTheRevisionsOfARollback(t *testing.T) {
	t.Parallel()

	s := setupRollbackProposal(t)

	rec := s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?status=succeeded", approverToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list struct {
		Proposals []rollbackProposalBody `json:"proposals"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))

	byID := map[string]rollbackProposalBody{}
	for _, p := range list.Proposals {
		byID[p.ID] = p
	}

	rollback := byID[propRollback]
	require.NotNil(t, rollback.Revisions)

	running, back := rollback.Revisions.Running, rollback.Revisions.GoesBackTo
	require.Equal(t, revisionBody{
		Kind: "ReplicaSet revision", ID: "7", Version: "checkout:1.4.7\\u200b", DeployedAt: "2026-11-03T09:30:00Z", DeployedBy: "ana",
	}, running, "a hidden character is spelled out")
	require.Equal(t, revisionBody{Kind: "ReplicaSet revision", ID: "6", Version: "checkout:1.4.6"}, back, "what the platform does not say is left out")

	require.NotEmpty(t, rollback.Changes, "the field-by-field difference is still sent")

	require.Nil(t, byID[propSucceeded].Revisions, "a proposal that is not a rollback has none")
}

func TestInvestigationShowsTheRevisionsOfItsRollback(t *testing.T) {
	t.Parallel()

	s := setupRollbackProposal(t)

	rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+propInvestgn, viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var inv struct {
		Proposals []rollbackProposalBody `json:"proposals"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &inv))

	var found bool

	for _, p := range inv.Proposals {
		if p.ID == propRollback {
			found = true

			require.NotNil(t, p.Revisions)
			require.Equal(t, "7", p.Revisions.Running.ID)
		}
	}

	require.True(t, found)
}

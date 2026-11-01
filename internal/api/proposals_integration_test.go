//go:build integration

package api_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	apispec "github.com/nourtalaat/sidekick/api"
	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
	"github.com/nourtalaat/sidekick/strategies"
)

const proposalsPath = "/api/v1/strategy-proposals"

// The proposals of fixture/proposals_api.sql.
const (
	proposalPassed   = "01920000-0000-7000-8000-000000000301"
	proposalFailed   = "01920000-0000-7000-8000-000000000302"
	proposalPending  = "01920000-0000-7000-8000-000000000303"
	proposalDrafting = "01920000-0000-7000-8000-000000000304"
	proposalDraftBad = "01920000-0000-7000-8000-000000000305"
	proposalFile     = "01920000-0000-7000-8000-000000000306"
	proposalStale    = "01920000-0000-7000-8000-000000000307"
	proposalUnknown  = "01920000-0000-7000-8000-000000000399"
)

// proposalItem is a proposal, with the fields the tests look at.
type proposalItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BaseVersion   string `json:"baseVersion"`
	BaseOrigin    string `json:"baseOrigin"`
	Rationale     string `json:"rationale"`
	Status        string `json:"status"`
	StatusDetail  string `json:"statusDetail"`
	GateResult    string `json:"gateResult"`
	BenchRunID    string `json:"benchRunId"`
	BaselineRunID string `json:"baselineRunId"`
	Reviewer      string `json:"reviewer"`
	BaseBody      string `json:"baseBody"`
	Body          string `json:"body"`
	Stale         bool   `json:"stale"`
	Approvable    bool   `json:"approvable"`
	Feedback      []struct {
		TargetKind string `json:"targetKind"`
		Rating     string `json:"rating"`
		Reason     string `json:"reason"`
	} `json:"feedback"`
	Gate *struct {
		Result   string  `json:"result"`
		Mean     float64 `json:"mean"`
		Interval struct {
			Lower float64 `json:"lower"`
			Upper float64 `json:"upper"`
		} `json:"interval"`
		MaxDrop   float64 `json:"maxDrop"`
		Resamples int     `json:"resamples"`
		Seed      string  `json:"seed"`
		Repeats   int     `json:"repeats"`
		Scenarios []struct {
			ID         string  `json:"id"`
			Difference float64 `json:"difference"`
		} `json:"scenarios"`
	} `json:"gate"`
}

// setupProposals is setup with the strategies of setupStrategies, the
// proposal reviewer and the proposals of fixture/proposals_api.sql.
func setupProposals(t *testing.T) server {
	t.Helper()

	base, err := strategy.Load(strategies.FS, filepath.Join("fixture", "strategies"), nil)
	require.NoError(t, err)

	return setupWith(t, func(o *api.Options) {
		store := strategystore.NewStore(o.Pool)
		o.Strategies = store
		o.StrategyBase = base
		o.ProposalReviewer = proposals.NewService(o.Pool, store)
	}, "fixture/rules_api.sql", "fixture/strategies_api.sql", "fixture/proposals_api.sql")
}

func TestListStrategyProposals(t *testing.T) {
	t.Parallel()

	s := setupProposals(t)

	type page struct {
		Proposals  []proposalItem `json:"proposals"`
		NextCursor string         `json:"nextCursor"`
	}

	get := func(t *testing.T, c ruleCaller, query string) (page, int) {
		t.Helper()

		rec := s.sendRule(t, c, http.MethodGet, proposalsPath+query, nil)

		var got page
		if rec.Code == http.StatusOK {
			decodeRule(t, rec, &got)
		}

		return got, rec.Code
	}

	ids := func(p page) []string {
		out := make([]string, len(p.Proposals))
		for i, it := range p.Proposals {
			out[i] = it.ID
		}

		return out
	}

	t.Run("newest first, without the drafts", func(t *testing.T) {
		t.Parallel()

		for _, c := range []ruleCaller{adminCaller, adminTokenCaller} {
			got, status := get(t, c, "")
			require.Equal(t, http.StatusOK, status, c.name)
			require.Equal(t, []string{proposalPassed, proposalFailed, proposalPending, proposalFile, proposalStale}, ids(got), c.name)
			require.Empty(t, got.NextCursor)
			require.Empty(t, got.Proposals[0].Body, "a list holds no YAML")
		}
	})

	t.Run("the summary of a passed proposal", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, adminCaller, "")
		p := got.Proposals[0]
		require.Equal(t, "checkout-latency", p.Name)
		require.Equal(t, "runtime:2", p.BaseVersion)
		require.Equal(t, "runtime", p.BaseOrigin)
		require.Equal(t, "passed", p.Status)
		require.Equal(t, "passed", p.GateResult)
		require.Equal(t, "Planned deploys were reported as incidents.", p.Rationale)
		require.NotEmpty(t, p.BenchRunID)
		require.NotEmpty(t, p.BaselineRunID)
	})

	t.Run("by status, which lists the drafts when asked", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, adminCaller, "?status=drafting")
		require.Equal(t, []string{proposalDrafting}, ids(got))

		got, _ = get(t, adminCaller, "?status=draft_failed")
		require.Equal(t, []string{proposalDraftBad}, ids(got))
		require.Equal(t, "The model did not return a valid strategy.", got.Proposals[0].StatusDetail)

		got, _ = get(t, adminCaller, "?status=passed")
		require.Equal(t, []string{proposalPassed, proposalFile, proposalStale}, ids(got))
	})

	t.Run("by strategy name", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, adminCaller, "?name=file-latency")
		require.Equal(t, []string{proposalFile}, ids(got))
	})

	t.Run("a page at a time", func(t *testing.T) {
		t.Parallel()

		first, _ := get(t, adminCaller, "?limit=2")
		require.Equal(t, []string{proposalPassed, proposalFailed}, ids(first))
		require.NotEmpty(t, first.NextCursor)

		second, _ := get(t, adminCaller, "?limit=2&cursor="+first.NextCursor)
		require.Equal(t, []string{proposalPending, proposalFile}, ids(second))

		third, _ := get(t, adminCaller, "?limit=2&cursor="+second.NextCursor)
		require.Equal(t, []string{proposalStale}, ids(third))
		require.Empty(t, third.NextCursor)
	})

	t.Run("bad parameters", func(t *testing.T) {
		t.Parallel()

		for _, query := range []string{"?limit=0", "?limit=201", "?status=nonsense", "?cursor=nonsense"} {
			_, status := get(t, adminCaller, query)
			require.Equal(t, http.StatusBadRequest, status, query)
		}
	})

	t.Run("only an admin may look", func(t *testing.T) {
		t.Parallel()

		for _, c := range []ruleCaller{viewerCaller, responderCaller, viewerTokenCaller, responderTokenCaller} {
			_, status := get(t, c, "")
			require.Equal(t, http.StatusForbidden, status, c.name)
		}

		_, status := get(t, ruleCaller{}, "")
		require.Equal(t, http.StatusUnauthorized, status)
	})
}

func TestGetStrategyProposal(t *testing.T) {
	t.Parallel()

	s := setupProposals(t)

	get := func(t *testing.T, id string) (proposalItem, int) {
		t.Helper()

		rec := s.sendRule(t, adminCaller, http.MethodGet, proposalsPath+"/"+id, nil)

		var got proposalItem
		if rec.Code == http.StatusOK {
			decodeRule(t, rec, &got)
		}

		return got, rec.Code
	}

	t.Run("a passed proposal with its YAML, feedback and gate", func(t *testing.T) {
		t.Parallel()

		got, status := get(t, proposalPassed)
		require.Equal(t, http.StatusOK, status)
		require.Contains(t, got.BaseBody, "Check what changed.")
		require.Contains(t, got.Body, "Ask whether a deploy was planned.")
		require.True(t, got.Approvable)
		require.False(t, got.Stale, "the strategy is at the version the draft started from")

		require.Len(t, got.Feedback, 2)
		reasons := []string{got.Feedback[0].Reason, got.Feedback[1].Reason}
		require.ElementsMatch(t, []string{"It was a planned deploy.", ""}, reasons)

		require.NotNil(t, got.Gate)
		require.Equal(t, "passed", got.Gate.Result)
		require.InDelta(t, 0.01, got.Gate.Mean, 1e-9)
		require.InDelta(t, -0.005, got.Gate.Interval.Lower, 1e-9)
		require.InDelta(t, 0.03, got.Gate.Interval.Upper, 1e-9)
		require.InDelta(t, 0.02, got.Gate.MaxDrop, 1e-9)
		require.Equal(t, 10000, got.Gate.Resamples)
		require.Equal(t, "42", got.Gate.Seed)
		require.Equal(t, 5, got.Gate.Repeats)
		require.Len(t, got.Gate.Scenarios, 2)
		require.Equal(t, "s1", got.Gate.Scenarios[0].ID)
		require.InDelta(t, 0.02, got.Gate.Scenarios[0].Difference, 1e-9)
	})

	t.Run("a failed proposal cannot be approved", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, proposalFailed)
		require.False(t, got.Approvable)
		require.Equal(t, "failed", got.Gate.Result)
	})

	t.Run("a proposal with no gate result yet", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, proposalPending)
		require.False(t, got.Approvable)
		require.Nil(t, got.Gate)
	})

	t.Run("a proposal drafted from an older version is stale", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, proposalStale)
		require.True(t, got.Stale)
	})

	t.Run("an unknown proposal", func(t *testing.T) {
		t.Parallel()

		_, status := get(t, proposalUnknown)
		require.Equal(t, http.StatusNotFound, status)
	})

	t.Run("a viewer may not look", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, viewerCaller, http.MethodGet, proposalsPath+"/"+proposalPassed, nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestApproveStrategyProposal(t *testing.T) {
	t.Parallel()

	t.Run("a passed proposal becomes the next version", func(t *testing.T) {
		t.Parallel()

		s := setupProposals(t)

		rec := s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalPassed+"/approve", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got struct {
			Proposal proposalItem `json:"proposal"`
			Version  int          `json:"version"`
			YAML     string       `json:"yaml"`
		}

		decodeRule(t, rec, &got)
		require.Equal(t, "applied", got.Proposal.Status)
		require.Equal(t, 3, got.Version)
		require.Empty(t, got.YAML)

		rec = s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/checkout-latency/versions/3", nil)
		require.Equal(t, http.StatusOK, rec.Code)

		var version struct {
			ProposalID string `json:"proposalId"`
			BenchRunID string `json:"benchRunId"`
			Body       string `json:"body"`
			Current    bool   `json:"current"`
		}

		decodeRule(t, rec, &version)
		require.Equal(t, proposalPassed, version.ProposalID)
		require.Equal(t, "01920000-0000-7000-8000-000000000501", version.BenchRunID)
		require.Contains(t, version.Body, "Ask whether a deploy was planned.")
		require.True(t, version.Current)

		// A second approval is refused.
		rec = s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalPassed+"/approve", nil)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "not_passed", ruleProblemOf(t, rec).Code)
	})

	t.Run("a proposal for a strategy in a file gives the YAML", func(t *testing.T) {
		t.Parallel()

		s := setupProposals(t)

		rec := s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalFile+"/approve", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got struct {
			Proposal proposalItem `json:"proposal"`
			Version  *int         `json:"version"`
			YAML     string       `json:"yaml"`
		}

		decodeRule(t, rec, &got)
		require.Equal(t, "approved", got.Proposal.Status)
		require.Nil(t, got.Version)
		require.Equal(t, "name: file-latency\ndescription: A strategy from a file, changed.\n", got.YAML)
	})

	t.Run("nothing but a passed proposal can be applied", func(t *testing.T) {
		t.Parallel()

		s := setupProposals(t)

		for _, id := range []string{proposalFailed, proposalPending, proposalDrafting, proposalDraftBad} {
			rec := s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+id+"/approve", nil)
			require.Equal(t, http.StatusConflict, rec.Code, id)
			require.Equal(t, "not_passed", ruleProblemOf(t, rec).Code, id)
		}

		rec := s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/checkout-latency/versions/3", nil)
		require.Equal(t, http.StatusNotFound, rec.Code, "no version was saved")
	})

	t.Run("a proposal drafted from an older version is stale", func(t *testing.T) {
		t.Parallel()

		s := setupProposals(t)

		rec := s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalStale+"/approve", nil)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "stale", ruleProblemOf(t, rec).Code)
	})

	t.Run("an unknown proposal", func(t *testing.T) {
		t.Parallel()

		s := setupProposals(t)

		rec := s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalUnknown+"/approve", nil)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, "not_found", ruleProblemOf(t, rec).Code)
	})

	t.Run("only an admin may approve", func(t *testing.T) {
		t.Parallel()

		s := setupProposals(t)

		for _, c := range []ruleCaller{viewerCaller, responderCaller, responderTokenCaller} {
			rec := s.sendRule(t, c, http.MethodPost, proposalsPath+"/"+proposalPassed+"/approve", nil)
			require.Equal(t, http.StatusForbidden, rec.Code, c.name)
		}

		rec := s.sendRule(t, adminTokenCaller, http.MethodPost, proposalsPath+"/"+proposalPassed+"/approve", nil)
		require.Equal(t, http.StatusOK, rec.Code, "an admin token may")
	})
}

func TestRejectStrategyProposal(t *testing.T) {
	t.Parallel()

	s := setupProposals(t)

	tests := []struct {
		name       string
		id         string
		body       any
		wantStatus int
		wantCode   string
	}{
		{name: "a pending proposal", id: proposalPending, wantStatus: http.StatusOK},
		{name: "a passed proposal, with a reason", id: proposalPassed, body: map[string]string{"reason": "Not now"}, wantStatus: http.StatusOK},
		{name: "a failed proposal", id: proposalFailed, wantStatus: http.StatusOK},
		{name: "a draft that is not made yet", id: proposalDrafting, wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "a draft that failed", id: proposalDraftBad, wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "a reason that is too long", id: proposalFile, body: map[string]string{"reason": strings.Repeat("x", 501)}, wantStatus: http.StatusUnprocessableEntity, wantCode: "validation"},
		{name: "an unknown proposal", id: proposalUnknown, wantStatus: http.StatusNotFound, wantCode: "not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+tt.id+"/reject", tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, tt.wantCode, ruleProblemOf(t, rec).Code)

				return
			}

			var got proposalItem

			decodeRule(t, rec, &got)
			require.Equal(t, "rejected", got.Status)
			require.NotEmpty(t, got.Reviewer)
		})
	}

	t.Run("a rejected proposal cannot be approved", func(t *testing.T) {
		t.Parallel()

		own := setupProposals(t)

		rec := own.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalPassed+"/reject", nil)
		require.Equal(t, http.StatusOK, rec.Code)

		rec = own.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalPassed+"/approve", nil)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "not_passed", ruleProblemOf(t, rec).Code)
	})

	t.Run("only an admin may reject", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, responderCaller, http.MethodPost, proposalsPath+"/"+proposalStale+"/reject", nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestEditStrategyProposal(t *testing.T) {
	t.Parallel()

	const edited = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check the deploys.\n"

	s := setupProposals(t)

	t.Run("a failed proposal goes through the gate again", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, adminCaller, http.MethodPut, proposalsPath+"/"+proposalFailed, map[string]string{"body": edited})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got proposalItem

		decodeRule(t, rec, &got)
		require.Equal(t, "pending", got.Status)
		require.Equal(t, edited, got.Body)
		require.Empty(t, got.GateResult)
		require.Nil(t, got.Gate)

		var kind string
		require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT kind FROM jobs WHERE payload ->> 'proposalId' = $1", proposalFailed).Scan(&kind))
		require.Equal(t, queue.KindBenchGate, kind)
	})

	t.Run("a passed proposal that is edited can no longer be approved", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, adminCaller, http.MethodPut, proposalsPath+"/"+proposalPassed, map[string]string{"body": edited})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rec = s.sendRule(t, adminCaller, http.MethodPost, proposalsPath+"/"+proposalPassed+"/approve", nil)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "not_passed", ruleProblemOf(t, rec).Code)
	})

	t.Run("a body that fails the strategy checks", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, adminCaller, http.MethodPut, proposalsPath+"/"+proposalFile, map[string]string{"body": "name: file-latency\nnope: 1\n"})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

		p := ruleProblemOf(t, rec)
		require.Equal(t, "validation", p.Code)
		require.NotEmpty(t, p.Errors)
	})

	t.Run("a body for another strategy", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, adminCaller, http.MethodPut, proposalsPath+"/"+proposalStale, map[string]string{"body": "name: disk-full\ndescription: Disk.\n"})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		require.Contains(t, ruleProblemOf(t, rec).fieldMessages(), "body.name")
	})

	t.Run("a proposal that is not ready to edit", func(t *testing.T) {
		t.Parallel()

		for _, id := range []string{proposalPending, proposalDrafting} {
			rec := s.sendRule(t, adminCaller, http.MethodPut, proposalsPath+"/"+id, map[string]string{"body": edited})
			require.Equal(t, http.StatusConflict, rec.Code, id)
			require.Equal(t, "conflict", ruleProblemOf(t, rec).Code, id)
		}
	})

	t.Run("an unknown proposal", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, adminCaller, http.MethodPut, proposalsPath+"/"+proposalUnknown, map[string]string{"body": edited})
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("only an admin may edit", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, responderCaller, http.MethodPut, proposalsPath+"/"+proposalStale, map[string]string{"body": edited})
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
}

// TestTheSpecHasNoWayToApplyBesidesApprove lists the operations under
// /strategy-proposals and checks that the one that applies a proposal is the
// approval, which only a passed proposal gets through, and that a version
// saved by hand cannot name a proposal.
func TestTheSpecHasNoWayToApplyBesidesApprove(t *testing.T) {
	t.Parallel()

	type operation struct {
		OperationID string `yaml:"operationId"`
		Role        string `yaml:"x-sidekick-role"`
	}

	var spec struct {
		Paths      map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]yaml.Node `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}

	require.NoError(t, yaml.Unmarshal(apispec.Spec, &spec))

	got := map[string]string{}

	for path, node := range spec.Paths {
		if !strings.HasPrefix(path, "/strategy-proposals") {
			continue
		}

		var methods map[string]operation

		require.NoError(t, node.Decode(&methods))

		for method, op := range methods {
			got[strings.ToUpper(method)+" "+path] = op.OperationID + " " + op.Role
		}
	}

	require.Equal(t, map[string]string{
		"GET /strategy-proposals":               "ListStrategyProposals admin",
		"GET /strategy-proposals/{id}":          "GetStrategyProposal admin",
		"PUT /strategy-proposals/{id}":          "EditStrategyProposal admin",
		"POST /strategy-proposals/{id}/approve": "ApproveStrategyProposal admin",
		"POST /strategy-proposals/{id}/reject":  "RejectStrategyProposal admin",
	}, got)

	saved, ok := spec.Components.Schemas["NewStrategyVersion"]
	require.True(t, ok)
	require.NotContains(t, saved.Properties, "proposalId")
	require.NotContains(t, saved.Properties, "benchRunId")
}

//go:build integration

package gitlabmr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// statusBed is a status job on a database, with a fake GitLab whose pull
// requests the test sets by branch.
type statusBed struct {
	*bed

	pool    *pgxpool.Pool
	checker *gitchange.StatusChecker
	now     time.Time

	mu    sync.Mutex
	pulls map[string]fakehttp.Response
	calls map[string]int
}

// newStatusBed returns a status job whose clock the test sets, over the
// fixture investigation.
func newStatusBed(t *testing.T) *statusBed {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	s := &statusBed{bed: newBedWithDB(t, nil, pool), pool: pool, now: time.Now().UTC().Truncate(time.Second), pulls: map[string]fakehttp.Response{}, calls: map[string]int{}}

	s.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, r *http.Request) {
		head := r.URL.Query().Get("source_branch")

		s.mu.Lock()
		resp, ok := s.pulls[head]
		s.calls[head]++
		s.mu.Unlock()

		if !ok {
			resp = reply("[]")
		}

		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}

		w.WriteHeader(max(resp.Status, http.StatusOK))
		_, _ = w.Write(resp.Body)
	})

	s.checker = gitchange.NewStatusChecker(gitchange.StatusOptions{
		Pool:    pool,
		Actions: map[string]action.Action{"monitor-mr": s.mr},
		Now:     func() time.Time { return s.now },
	})

	return s
}

// proposal inserts a succeeded proposal of action name that executed at
// the given time, for the project, and returns its ID.
func (s *statusBed) proposal(t *testing.T, name, project string, executed time.Time) uuid.UUID {
	t.Helper()

	id := uuid.New()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at, executed_at, result)
		VALUES ($1, $2, $3, jsonb_build_object('project', $4::text), '', 'approved', 'succeeded', $5, $6::timestamptz, $6::timestamptz + interval '1 day', $6::timestamptz, '{}')`,
		id, checkoutInvestigation, name, project, action.IdempotencyKey(id.String()), executed)
	require.NoError(t, err)

	return id
}

// pull sets the merge request GitLab reports for the proposal's branch.
func (s *statusBed) pull(id uuid.UUID, number int, state string, mergedAt string) {
	merged := "null"
	if mergedAt != "" {
		merged = `"` + mergedAt + `"`
	}

	body := fmt.Sprintf(`[{"iid":%d,"state":%q,"web_url":"%s%d","merged_at":%s,"closed_at":%s}]`,
		number, state, testWebURL, number, merged, merged)

	s.set(id, reply(body))
}

// set sets the answer GitLab gives for the proposal's branch.
func (s *statusBed) set(id uuid.UUID, resp fakehttp.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pulls[action.IdempotencyKey(id.String())] = resp
}

// called returns how many times the proposal's branch was looked up.
func (s *statusBed) called(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls[action.IdempotencyKey(id.String())]
}

// claim queues a status job and claims it, as a worker does.
func (s *statusBed) claim(t *testing.T) db.Job {
	t.Helper()

	_, err := queue.Enqueue(t.Context(), s.pool, queue.NewJob{Kind: queue.KindCheckPullRequests, Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)

	jobs, err := db.New(s.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindCheckPullRequests}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	return jobs[0]
}

// state returns the recorded state of a proposal's merge request, or nil.
func (s *statusBed) state(t *testing.T, id uuid.UUID) *db.PullRequestState {
	t.Helper()

	rows, err := s.pool.Query(t.Context(), "SELECT * FROM pull_request_states WHERE proposal_id = $1", id)
	require.NoError(t, err)

	defer rows.Close()

	if !rows.Next() {
		require.NoError(t, rows.Err())

		return nil
	}

	var out db.PullRequestState

	require.NoError(t, rows.Scan(&out.ProposalID, &out.Repo, &out.Number, &out.Url, &out.State, &out.MergedAt, &out.ClosedAt, &out.CheckedAt))

	return &out
}

func TestStatusJobRecordsEachState(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)

	open := s.proposal(t, "monitor-mr", testProject, s.now.Add(-3*time.Hour))
	mergedID := s.proposal(t, "monitor-mr", testProject, s.now.Add(-2*time.Hour))
	closedID := s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour))

	s.pull(open, 1, "opened", "")
	s.pull(mergedID, 2, "merged", "2026-11-03T10:00:00Z")
	s.pull(closedID, 3, "closed", "")

	require.NoError(t, s.checker.Handle(t.Context(), s.claim(t)))

	o := s.state(t, open)
	require.NotNil(t, o)
	require.Equal(t, gitchange.StateOpen, o.State)
	require.Equal(t, int64(1), o.Number)
	require.Equal(t, testWebURL+"1", o.Url)
	require.Nil(t, o.MergedAt)
	require.WithinDuration(t, s.now, o.CheckedAt, time.Second)

	m := s.state(t, mergedID)
	require.NotNil(t, m)
	require.Equal(t, gitchange.StateMerged, m.State)
	require.NotNil(t, m.MergedAt)
	require.Equal(t, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), m.MergedAt.UTC())

	c := s.state(t, closedID)
	require.NotNil(t, c)
	require.Equal(t, gitchange.StateClosed, c.State)
}

func TestStatusJobChecksOnlyWhatCanChange(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)

	stillOpen := s.proposal(t, "monitor-mr", testProject, s.now.Add(-3*time.Hour))
	done := s.proposal(t, "monitor-mr", testProject, s.now.Add(-2*time.Hour))
	old := s.proposal(t, "monitor-mr", testProject, s.now.Add(-gitchange.TrackedFor-time.Hour))
	other := s.proposal(t, "monitor-note", testProject, s.now.Add(-time.Hour))

	s.pull(stillOpen, 1, "opened", "")
	s.pull(done, 2, "merged", "2026-11-03T10:00:00Z")
	s.pull(old, 3, "opened", "")
	s.pull(other, 4, "opened", "")

	require.NoError(t, s.checker.Handle(t.Context(), s.claim(t)))
	require.Nil(t, s.state(t, old), "a proposal older than the tracking window is not checked")
	require.Nil(t, s.state(t, other), "a proposal of another action is not checked")

	s.now = s.now.Add(time.Hour)
	s.pull(stillOpen, 1, "merged", "2026-11-03T12:00:00Z")

	require.NoError(t, s.checker.Handle(t.Context(), s.claim(t)))
	require.Equal(t, gitchange.StateMerged, s.state(t, stillOpen).State, "an open merge request is checked again")
	require.Equal(t, 2, s.called(stillOpen))
	require.Equal(t, 1, s.called(done), "a merged merge request is not checked again")
	require.Zero(t, s.called(old))
	require.Zero(t, s.called(other))

	s.pull(stillOpen, 1, "opened", "")
	s.now = s.now.Add(time.Hour)

	require.NoError(t, s.checker.Handle(t.Context(), s.claim(t)))
	require.Equal(t, gitchange.StateMerged, s.state(t, stillOpen).State, "a recorded merge does not change")
	require.Equal(t, 2, s.called(stillOpen))
}

func TestStatusJobLeavesWhatGitLabDoesNotKnow(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)

	unknown := s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour))

	require.NoError(t, s.checker.Handle(t.Context(), s.claim(t)))
	require.Nil(t, s.state(t, unknown), "a proposal with no merge request on GitLab has no state")
	require.Equal(t, 1, s.called(unknown))
}

func TestStatusJobErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		answer     fakehttp.Response
		repo       string
		wantErr    string
		wantStored bool
	}{
		{name: "a project the token cannot read", answer: notFound, wantStored: false},
		{name: "a project name that is not a full path", repo: "acme", wantStored: false},
		{name: "a project the config does not list", repo: "acme/platform/other", wantStored: false},
		{name: "GitLab fails", answer: replyStatus(http.StatusBadGateway, `{"message":"Server Error"}`), wantErr: "1 of 2 pull requests could not be checked", wantStored: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newStatusBed(t)

			repo := tt.repo
			if repo == "" {
				repo = testProject
			}

			bad := s.proposal(t, "monitor-mr", repo, s.now.Add(-2*time.Hour))
			good := s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour))

			s.set(bad, tt.answer)
			s.pull(good, 9, "merged", "2026-11-03T10:00:00Z")

			err := s.checker.Handle(t.Context(), s.claim(t))
			if tt.wantErr == "" {
				require.NoError(t, err, "a failure that a repeat cannot fix is skipped")
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}

			require.Nil(t, s.state(t, bad))

			g := s.state(t, good)
			require.NotNil(t, g, "the other merge requests are still recorded")
			require.Equal(t, gitchange.StateMerged, g.State)
		})
	}
}

func TestStatusJobWritesNothingAfterLosingItsClaim(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)

	id := s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour))
	s.pull(id, 1, "opened", "")

	job := s.claim(t)

	taken := *job.ClaimToken + 1000
	job.ClaimToken = &taken

	require.NoError(t, s.checker.Handle(t.Context(), job))
	require.Nil(t, s.state(t, id), "a worker that lost its lease writes nothing")
}

package awsecs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestSnapshotChoosesDeployedRevisions(t *testing.T) {
	t.Parallel()

	threeBack := func(c *Config) { c.MaxRevisionsBack = new(3) }

	tests := []struct {
		name    string
		edit    func(*Config)
		history []string
		fake    func(*fakeECS)
		params  string
		want    int
	}{
		{name: "an ACTIVE revision the service never deployed is skipped", history: []string{"42", "40", "37"}, params: paramsBack1, want: 40},
		{name: "two back skips revisions that were never deployed", edit: threeBack, history: []string{"42", "40", "37"}, params: paramsBack2, want: 37},
		{name: "a redeploy of the same revision counts once", edit: threeBack, history: []string{"42", "41", "41", "40"}, params: paramsBack2, want: 40},
		{name: "a deployment that was stopped does not count", history: []string{"42", "41/STOPPED", "40"}, params: paramsBack1, want: 40},
		{name: "a deployment that was rolled back does not count", history: []string{"42", "41/ROLLBACK_SUCCESSFUL", "40"}, params: paramsBack1, want: 40},
		{name: "a deployment still in progress does not count", history: []string{"42", "41/IN_PROGRESS", "40"}, params: paramsBack1, want: 40},
		{name: "a newer revision that was deployed and rolled back is not a way back", history: []string{"42", "43", "42", "41"}, params: paramsBack1, want: 41},
		{name: "an INACTIVE revision that was deployed is skipped", history: []string{"42", "39", "38"}, params: paramsBack1, want: 38},
		{
			name: "deployments of another family are ignored", params: paramsBack1, want: 41,
			fake: func(f *fakeECS) {
				f.deployments = append([]fakeDeployment{{family: "checkout-worker", revision: 6, status: "SUCCESSFUL"}}, f.deployments...)
			},
		},
		{name: "a deployment ECS cannot describe is skipped", history: []string{"42", "41", "40"}, params: paramsBack1, want: 40, fake: func(f *fakeECS) { f.deployments[1].unreadable = true }},
		{name: "the revision deployed long ago, after a long run of redeploys", history: slices.Concat([]string{"42"}, repeat("42", 150), []string{"41"}), params: paramsBack1, want: 41},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)
			if tt.history != nil {
				f.deploy(t, tt.history...)
			}

			if tt.fake != nil {
				tt.fake(f)
			}

			snap, err := r.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.NoError(t, err)

			revs, err := snap.Revisions()
			require.NoError(t, err)

			finished := time.Date(2026, 5, 4, 9, 40, 0, 0, time.UTC)
			require.Equal(t, action.Revision{
				Kind: "task definition", ID: "checkout:" + strconv.Itoa(tt.want), Version: imageFor(tt.want),
				DeployedAt: &finished, DeployedBy: "registered by arn:aws:iam::111122223333:user/ana",
				Notes: fmt.Sprintf("The time is when deployment sd-%d of this service, which succeeded, finished.", f.deploymentIndex(t, tt.want)),
			}, revs.GoesBackTo)
		})
	}
}

func TestSnapshotPagesTheHistory(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	f.deploy(t, slices.Concat([]string{"42"}, repeat("42", 150), []string{"41"})...)

	_, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	require.Len(t, f.deploymentLists, 2)
	require.JSONEq(t, `{"cluster":"shop","service":"checkout","status":["SUCCESSFUL"],"maxResults":100}`, string(f.deploymentLists[0]))
	require.JSONEq(t, `{"cluster":"shop","service":"checkout","status":["SUCCESSFUL"],"maxResults":100,"nextToken":"100"}`, string(f.deploymentLists[1]))
	require.Equal(t, []int{20, 20, 20, 20, 20, 20, 20, 12}, f.revisionDescribes, "the service revisions are described 20 at a time, each once")
}

func TestSnapshotStopsReadingTheHistoryWhenItHasEnough(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)

	_, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)
	require.Equal(t, 1, f.count("ListServiceDeployments"))
	require.Equal(t, 1, f.count("DescribeServiceRevisions"))
}

func TestSnapshotWithoutUsableHistory(t *testing.T) {
	t.Parallel()

	registered := time.Date(2026, 5, 4, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		fake func(*fakeECS)
		// wantWarning ends the notes of the revision.
		wantWarning string
		wantCalls   map[string]int
	}{
		{
			name: "ECS has no deployments", fake: func(f *fakeECS) { f.deployments = nil }, wantWarning: noneWarning,
			wantCalls: map[string]int{"ListServiceDeployments": 2, "DescribeServiceRevisions": 0},
		},
		{
			name: "Sidekick may not list the deployments", fake: func(f *fakeECS) { f.failWith("ListServiceDeployments", 400, "AccessDeniedException") },
			wantWarning: deniedWarning, wantCalls: map[string]int{"ListServiceDeployments": 1, "DescribeServiceRevisions": 0},
		},
		{
			name: "Sidekick may not describe the service revisions", fake: func(f *fakeECS) { f.failWith("DescribeServiceRevisions", 400, "AccessDeniedException") },
			wantWarning: deniedWarning, wantCalls: map[string]int{"ListServiceDeployments": 1, "DescribeServiceRevisions": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, nil)
			tt.fake(f)

			snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
			require.NoError(t, err)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, action.Revision{
				Kind: "task definition", ID: "checkout:41", Version: imageFor(41),
				DeployedAt: &registered, DeployedBy: "registered by arn:aws:iam::111122223333:user/ana", Notes: registeredNote + " " + tt.wantWarning,
			}, revs.GoesBackTo, "the revision is the highest ACTIVE one below the running one, and the warning says the history was not used")

			for op, n := range tt.wantCalls {
				require.Equal(t, n, f.count(op), op)
			}
		})
	}
}

func TestSnapshotFallbackGoesBackInRegistrationOrder(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, func(c *Config) { c.MaxRevisionsBack = new(3) })
	f.failWith("ListServiceDeployments", 400, "AccessDeniedException")

	snap, err := r.Snapshot(t.Context(), json.RawMessage(`{"cluster":"shop","service":"checkout","revisionsBack":3}`))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "checkout:38", revs.GoesBackTo.ID)
}

func TestSnapshotRetriesAHistoryThatFailsForNow(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"ListServiceDeployments", "DescribeServiceRevisions"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, nil)
			f.failWith(op, 400, "ThrottlingException")

			_, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
			require.Error(t, err)
			require.NotErrorIs(t, err, action.ErrPermanent, "Sidekick tries again")
			require.NotErrorIs(t, err, action.ErrLimit)
		})
	}
}

func TestSnapshotRefusesRevisionsTheHistoryDoesNotShow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		edit    func(*Config)
		history []string
		fake    func(*fakeECS)
		params  string
		// wantErr lists parts of the message.
		wantErr []string
		// wantNoHistory is true when the refusal comes before the history is read.
		wantNoHistory bool
	}{
		{
			name: "fewer deployed than asked, with INACTIVE and never deployed ones", edit: func(c *Config) { c.MaxRevisionsBack = new(2) }, history: []string{"42", "40", "39"}, params: paramsBack2,
			wantErr: []string{
				"service checkout deployed only 1 ACTIVE task definition of family checkout below checkout:42 successfully in the deployment history ECS keeps (about 90 days), so it cannot go 2 back",
				"Earlier task definitions it did deploy are INACTIVE, and ECS cannot deploy an INACTIVE task definition: checkout:39",
				"These ACTIVE revisions are not in its history of successful deployments, so Sidekick does not choose them: checkout:41, checkout:38, checkout:37, checkout:36",
			},
		},
		{
			name: "only an INACTIVE revision was deployed", history: []string{"42", "39"}, params: paramsBack1,
			wantErr: []string{
				"service checkout has no ACTIVE task definition of family checkout below checkout:42 that it deployed successfully in the deployment history ECS keeps (about 90 days), so there is nothing to go back to",
				"ECS cannot deploy an INACTIVE task definition: checkout:39",
				"checkout:41, checkout:40, checkout:38, checkout:37, checkout:36",
			},
		},
		{
			name: "no deployment succeeded, but there are deployments", history: []string{"42/STOPPED", "41/STOPPED"}, params: paramsBack1,
			wantErr: []string{"service checkout has no ACTIVE task definition of family checkout below checkout:42 that it deployed successfully", "checkout:41, checkout:40, checkout:38, checkout:37, checkout:36"},
		},
		{
			name: "only the running revision was deployed", history: []string{"42"}, params: paramsBack1,
			wantErr: []string{"service checkout has no ACTIVE task definition of family checkout below checkout:42 that it deployed successfully"},
		},
		{
			name: "a long list of revisions is cut", history: []string{"42"}, params: paramsBack1,
			fake: func(f *fakeECS) {
				for rev := 33; rev <= 35; rev++ {
					f.definitions = append(f.definitions, fakeTaskDefinition{family: "checkout", revision: rev, status: "ACTIVE", images: []string{imageFor(rev)}})
				}
			},
			wantErr: []string{"checkout:41, checkout:40, checkout:38, checkout:37, checkout:36, and 3 more"},
		},
		{
			name: "a history longer than the search reads", history: slices.Concat([]string{"42"}, repeat("42", 400), []string{"41"}), params: paramsBack1,
			wantErr: []string{"read the newest 300 successful deployments of service checkout and found 0 ACTIVE task definitions of family checkout below checkout:42, not 1; ask for fewer revisions back, or roll back in the ECS console"},
		},
		{
			name: "no ACTIVE revision below the running one", params: paramsBack1, fake: func(f *fakeECS) { f.service.primary = "checkout:36" }, wantNoHistory: true,
			wantErr: []string{"task definition family checkout has no ACTIVE revision below checkout:36, so there is nothing to go back to"},
		},
		{
			name: "fewer ACTIVE revisions than asked", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: `{"cluster":"shop","service":"checkout","revisionsBack":3}`,
			fake: func(f *fakeECS) { f.service.primary = "checkout:38" }, wantNoHistory: true,
			wantErr: []string{"task definition family checkout has only 2 ACTIVE revisions below checkout:38, so the service cannot go 3 back"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)
			if tt.history != nil {
				f.deploy(t, tt.history...)
			}

			if tt.fake != nil {
				tt.fake(f)
			}

			_, err := r.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.Error(t, err)
			require.ErrorIs(t, err, action.ErrLimit, "the model is shown the reason as a refusal")
			require.ErrorIs(t, err, action.ErrPermanent)

			for _, part := range tt.wantErr {
				require.ErrorContains(t, err, part)
			}

			if tt.wantNoHistory {
				require.Zero(t, f.count("ListServiceDeployments"), "the history is not read when the ACTIVE revisions alone rule it out")
			}
		})
	}
}

//go:build integration

package queue

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// releaseKey is the unique key of the jobs in the ReleaseUniqueKey tests.
const releaseKey = "publish:i1:slack"

func TestReleaseUniqueKeyLetsANewJobInWhileTheOldOneRuns(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	firstID := mustEnqueue(t, pool, NewJob{Kind: KindPublish, UniqueKey: releaseKey})
	running := mustClaimOne(t, pool, KindPublish)

	_, err := Enqueue(t.Context(), pool, NewJob{Kind: KindPublish, Payload: []byte(`{}`), UniqueKey: releaseKey})
	require.ErrorIs(t, err, ErrDuplicate, "the running job holds the key")

	require.NoError(t, ReleaseUniqueKey(t.Context(), pool, running))

	released := getJob(t, pool, firstID)
	require.Equal(t, StatusRunning, released.Status, "the job keeps running")
	require.Nil(t, released.UniqueKey)

	secondID := mustEnqueue(t, pool, NewJob{Kind: KindPublish, UniqueKey: releaseKey})
	require.Equal(t, new(releaseKey), getJob(t, pool, secondID).UniqueKey)

	require.NoError(t, complete(t.Context(), pool, running), "the released job completes as usual")
	require.Equal(t, StatusDone, getJob(t, pool, firstID).Status)
	require.Equal(t, StatusPending, getJob(t, pool, secondID).Status)
}

func TestReleaseUniqueKeyChecksTheToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// staleToken makes the call with a token that no longer owns the job.
		staleToken bool
		// finish completes the job before the call.
		finish bool
	}{
		{name: "a token that no longer owns the job", staleToken: true},
		{name: "a job that is no longer running", finish: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			id := mustEnqueue(t, pool, NewJob{Kind: KindPublish, UniqueKey: releaseKey})
			job := mustClaimOne(t, pool, KindPublish)

			if tt.finish {
				require.NoError(t, complete(t.Context(), pool, job))
			}

			call := job
			if tt.staleToken {
				call.ClaimToken = new(*job.ClaimToken - 1)
			}

			require.ErrorIs(t, ReleaseUniqueKey(t.Context(), pool, call), ErrLeaseLost)
			require.Equal(t, new(releaseKey), getJob(t, pool, id).UniqueKey, "the key is kept")
		})
	}
}

-- name: ListPullRequestsToCheck :many
-- ListPullRequestsToCheck returns up to batch succeeded proposals of the
-- given actions, executed since the given time, whose pull request has no
-- state yet or was last checked as open before the given time. The ones
-- never checked come first, then those checked longest ago. The repository
-- is read from the proposal's parameters.
SELECT p.id, p.action, (p.params ->> 'repo')::text AS repo
FROM action_proposals p
LEFT JOIN pull_request_states s ON s.proposal_id = p.id
WHERE p.status = 'succeeded'
  AND p.action = ANY (@actions::text[])
  AND p.executed_at >= @since::timestamptz
  AND (s.proposal_id IS NULL OR (s.state = 'open' AND s.checked_at < @before::timestamptz))
ORDER BY s.checked_at NULLS FIRST, p.executed_at, p.id
LIMIT @batch;

-- name: UpsertPullRequestState :execrows
-- UpsertPullRequestState records what GitHub said about the pull request of
-- a proposal. It changes a row only while the row says open, so a pull
-- request that was merged or closed stays so, and it writes nothing unless
-- the job still holds its claim. It returns how many rows it wrote.
INSERT INTO pull_request_states (proposal_id, repo, number, url, state, merged_at, closed_at, checked_at)
SELECT @proposal_id::uuid, @repo::text, @number::bigint, @url::text, @state::text,
       sqlc.narg(merged_at)::timestamptz, sqlc.narg(closed_at)::timestamptz, @now::timestamptz
WHERE EXISTS (
  SELECT 1 FROM jobs WHERE id = @job_id AND claim_token = @claim_token::bigint AND status = 'running'
)
ON CONFLICT (proposal_id) DO UPDATE
SET number = EXCLUDED.number,
    url = EXCLUDED.url,
    state = EXCLUDED.state,
    merged_at = EXCLUDED.merged_at,
    closed_at = EXCLUDED.closed_at,
    checked_at = EXCLUDED.checked_at
WHERE pull_request_states.state = 'open';

-- name: GetPullRequestState :one
SELECT * FROM pull_request_states WHERE proposal_id = @proposal_id;

-- name: PullRequestJobExists :one
-- PullRequestJobExists reports whether a check_pull_requests job with the
-- unique key exists in any status: pending, running, done or dead.
SELECT EXISTS (
  SELECT 1 FROM jobs WHERE kind = 'check_pull_requests' AND unique_key = @unique_key::text
) AS found;

-- name: InsertScenario :one
-- InsertScenario stores a new draft. It inserts nothing and returns no row
-- when the investigation already has a scenario.
INSERT INTO scenarios (
  id, from_investigation, title, status, body, revision, provenance, notes, created_by, created_at, updated_at
)
VALUES (
  @id, @from_investigation, @title, 'draft', @body, 1, @provenance, @notes, sqlc.narg(created_by), @now, @now
)
ON CONFLICT (from_investigation) DO NOTHING
RETURNING *;

-- name: GetScenario :one
-- GetScenario returns a scenario in any status. It is for the review: use
-- GetReviewedScenario to run or download one.
SELECT * FROM scenarios WHERE id = @id;

-- name: GetScenarioByInvestigation :one
-- GetScenarioByInvestigation returns the scenario built from an
-- investigation, in any status.
SELECT * FROM scenarios WHERE from_investigation = @investigation_id;

-- name: GetReviewedScenario :one
-- GetReviewedScenario returns a scenario only when it is reviewed. It is
-- the only way to read a scenario's file to run it or to download it.
SELECT * FROM scenarios WHERE id = @id AND status = 'reviewed';

-- name: ListReviewedScenarios :many
-- ListReviewedScenarios returns every reviewed scenario, by id. It is the
-- only way to read the files of many scenarios to run them.
SELECT * FROM scenarios WHERE status = 'reviewed' ORDER BY id;

-- name: ListScenarios :many
-- ListScenarios returns a page of scenarios of any status, newest first,
-- without their files. A page starts after the scenario with cursor_id that
-- was updated at cursor_at.
SELECT id, from_investigation, title, status, revision, created_at, reviewed_at, updated_at
FROM scenarios
WHERE sqlc.narg(cursor_at)::timestamptz IS NULL
   OR (updated_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::text)
ORDER BY updated_at DESC, id DESC
LIMIT @page_size;

-- name: ListScenariosByStatus :many
-- ListScenariosByStatus is ListScenarios for the scenarios of one status.
SELECT id, from_investigation, title, status, revision, created_at, reviewed_at, updated_at
FROM scenarios
WHERE status = @status::text
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (updated_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::text))
ORDER BY updated_at DESC, id DESC
LIMIT @page_size;

-- name: UpdateScenarioBody :one
-- UpdateScenarioBody replaces the file of the scenario at revision, and puts
-- it back to draft: an edit always needs a new review. It returns no row
-- when the scenario is not at that revision.
UPDATE scenarios
SET title = @title, body = @body, status = 'draft', reviewed_by = NULL, reviewed_at = NULL,
    revision = revision + 1, updated_at = @now
WHERE id = @id AND revision = @revision
RETURNING *;

-- name: ReviewScenario :one
-- ReviewScenario marks a draft at revision as reviewed by a user. It
-- returns no row when the scenario is not a draft at that revision.
UPDATE scenarios
SET status = 'reviewed', reviewed_by = @reviewed_by, reviewed_at = @now::timestamptz, updated_at = @now::timestamptz
WHERE id = @id AND revision = @revision AND status = 'draft'
RETURNING *;

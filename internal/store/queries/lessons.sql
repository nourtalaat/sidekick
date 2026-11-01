-- name: ListLessonCandidates :many
-- ListLessonCandidates returns the active lessons that apply to an
-- investigation, best first, with the number of lessons that apply in all.
-- A lesson applies when every scope field it sets equals the
-- investigation's value; a lesson with no scope field applies to all.
--
-- The lessons come in five groups, by their first scope field: service,
-- else alert, else route, else strategy, else none. The groups do not
-- overlap, and each is read through the index of its field.
--
-- The order is: lessons with a scope before global ones, more scope fields
-- before fewer, the higher helpful count minus unhelpful count, the better
-- text-search rank against the event's text (any one word is enough, unlike
-- websearch_to_tsquery, where every word must match), then the newest.
WITH q AS (
  SELECT replace(plainto_tsquery('english', @text::text)::text, ' & ', ' | ')::tsquery AS tsq
), applies AS (
  SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy, helpful, unhelpful, search, created_at
  FROM lessons
  WHERE status = 'active' AND scope_service = @service::text
    AND (scope_alert IS NULL OR scope_alert = @alert_name::text)
    AND (scope_route IS NULL OR scope_route = @route::text)
    AND (scope_strategy IS NULL OR scope_strategy = @strategy::text)
  UNION ALL
  SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy, helpful, unhelpful, search, created_at
  FROM lessons
  WHERE status = 'active' AND scope_service IS NULL AND scope_alert = @alert_name::text
    AND (scope_route IS NULL OR scope_route = @route::text)
    AND (scope_strategy IS NULL OR scope_strategy = @strategy::text)
  UNION ALL
  SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy, helpful, unhelpful, search, created_at
  FROM lessons
  WHERE status = 'active' AND scope_service IS NULL AND scope_alert IS NULL AND scope_route = @route::text
    AND (scope_strategy IS NULL OR scope_strategy = @strategy::text)
  UNION ALL
  SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy, helpful, unhelpful, search, created_at
  FROM lessons
  WHERE status = 'active' AND scope_service IS NULL AND scope_alert IS NULL AND scope_route IS NULL
    AND scope_strategy = @strategy::text
  UNION ALL
  SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy, helpful, unhelpful, search, created_at
  FROM lessons
  WHERE status = 'active' AND scope_service IS NULL AND scope_alert IS NULL AND scope_route IS NULL
    AND scope_strategy IS NULL
)
SELECT a.id, a.text, a.scope_service, a.scope_alert, a.scope_route, a.scope_strategy, a.helpful, a.unhelpful,
       count(*) OVER ()::int AS total
FROM applies AS a, q
ORDER BY num_nonnulls(a.scope_service, a.scope_alert, a.scope_route, a.scope_strategy) = 0,
         num_nonnulls(a.scope_service, a.scope_alert, a.scope_route, a.scope_strategy) DESC,
         a.helpful - a.unhelpful DESC,
         ts_rank(a.search, q.tsq) DESC,
         a.created_at DESC,
         a.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: InsertLessonUses :exec
-- InsertLessonUses records that an investigation used lessons. A lesson it
-- used already is left as it is. Run it in the transaction that saves the
-- prompt.
INSERT INTO lesson_uses (lesson_id, investigation_id)
SELECT unnest(@lesson_ids::uuid[]), @investigation_id::uuid
ON CONFLICT (lesson_id, investigation_id) DO NOTHING;

-- name: TouchLessonsUsed :exec
-- TouchLessonsUsed sets the time lessons were last used. A lesson used in
-- the last hour is left alone, so the busiest lessons are not written to by
-- every investigation.
UPDATE lessons
SET last_used_at = now()
WHERE id = ANY (@lesson_ids::uuid[])
  AND (last_used_at IS NULL OR last_used_at < now() - interval '1 hour');

-- name: GetFeedbackToDraft :one
-- GetFeedbackToDraft returns what the draft_lesson job needs of a feedback
-- row. It returns no row for feedback that is gone.
SELECT id, investigation_id, target_kind, target_ref, rating, reason, correct_verdict, correct_cause
FROM feedback
WHERE id = @id;

-- name: LessonExistsForFeedback :one
-- LessonExistsForFeedback reports whether a lesson was drafted from the
-- feedback row, whatever became of the lesson since.
SELECT EXISTS (SELECT 1 FROM lessons WHERE feedback_ids @> ARRAY[@feedback_id::uuid]);

-- name: GetToolCallStep :one
-- GetToolCallStep returns what the result of a tool call says of the call.
-- It returns no row when the call has no result yet.
SELECT source, tool, status
FROM steps
WHERE investigation_id = @investigation_id AND tool_call_id = @tool_call_id AND kind = 'tool_result'
LIMIT 1;

-- name: InsertDraftedLesson :one
-- InsertDraftedLesson stores a lesson drafted from a feedback row, and
-- returns its ID. It stores nothing, and returns no row, when a lesson was
-- drafted from the feedback already, so a repeated job adds no second one.
INSERT INTO lessons (id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin, feedback_ids)
SELECT @id::uuid, @text::text, sqlc.narg(scope_service)::text, sqlc.narg(scope_alert)::text,
       sqlc.narg(scope_route)::text, sqlc.narg(scope_strategy)::text, @status::text, 'feedback',
       ARRAY[@feedback_id::uuid]
WHERE NOT EXISTS (SELECT 1 FROM lessons WHERE feedback_ids @> ARRAY[@feedback_id::uuid])
RETURNING id;

-- name: SearchLessons :many
-- SearchLessons returns the active lessons that match a web-search style
-- query (words, "quoted phrases", or, -word), best rank first. With a
-- service, it leaves out the lessons scoped to another service.
SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy
FROM lessons
WHERE status = 'active'
  AND search @@ websearch_to_tsquery('english', @query)
  AND (sqlc.narg(service)::text IS NULL OR scope_service IS NULL OR scope_service = sqlc.narg(service)::text)
ORDER BY ts_rank(search, websearch_to_tsquery('english', @query)) DESC, created_at DESC, id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: ListLessons :many
-- ListLessons returns lessons, newest first, a page at a time. Every filter
-- is optional: a status, the service a lesson is scoped to, words its text
-- matches (a web-search style query), and the place after which the page
-- starts (the time and ID of the last lesson of the page before). Each
-- lesson comes with the feedback it was drafted from.
SELECT l.id, l.text, l.scope_service, l.scope_alert, l.scope_route, l.scope_strategy, l.status, l.origin, l.created_by,
       l.approved_by, l.created_at, l.updated_at, l.helpful, l.unhelpful, l.last_used_at, l.version,
       coalesce((
         SELECT jsonb_agg(jsonb_build_object('feedbackId', f.id, 'investigationId', f.investigation_id) ORDER BY f.created_at, f.id)
         FROM feedback AS f
         WHERE f.id = ANY (l.feedback_ids)
       ), '[]'::jsonb)::jsonb AS feedback_links
FROM lessons AS l
WHERE (sqlc.narg(status)::text IS NULL OR l.status = sqlc.narg(status)::text)
  AND (sqlc.narg(service)::text IS NULL OR l.scope_service = sqlc.narg(service)::text)
  AND (sqlc.narg(search)::text IS NULL OR l.search @@ websearch_to_tsquery('english', sqlc.narg(search)::text))
  AND (sqlc.narg(after_at)::timestamptz IS NULL OR (l.created_at, l.id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY l.created_at DESC, l.id DESC
LIMIT sqlc.arg(page_size)::int;

-- name: GetLesson :one
-- GetLesson returns one lesson with the feedback it was drafted from. It
-- returns no row for a lesson that does not exist.
SELECT l.id, l.text, l.scope_service, l.scope_alert, l.scope_route, l.scope_strategy, l.status, l.origin, l.created_by,
       l.approved_by, l.created_at, l.updated_at, l.helpful, l.unhelpful, l.last_used_at, l.version,
       coalesce((
         SELECT jsonb_agg(jsonb_build_object('feedbackId', f.id, 'investigationId', f.investigation_id) ORDER BY f.created_at, f.id)
         FROM feedback AS f
         WHERE f.id = ANY (l.feedback_ids)
       ), '[]'::jsonb)::jsonb AS feedback_links
FROM lessons AS l
WHERE l.id = @id;

-- name: CountLessonsByStatus :many
-- CountLessonsByStatus returns how many lessons there are with each status.
SELECT status, count(*)::int AS n FROM lessons GROUP BY status;

-- name: LockLesson :one
-- LockLesson returns the fields of a lesson that a change checks and holds a
-- lock on its row until the transaction ends, so two admins who change the
-- same lesson at once are applied in turn.
SELECT id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin, version
FROM lessons
WHERE id = @id
FOR UPDATE;

-- name: InsertLesson :one
-- InsertLesson stores a lesson that an admin wrote, and returns its ID.
INSERT INTO lessons (id, text, scope_service, scope_alert, scope_route, scope_strategy, status, origin, created_by)
VALUES (@id, @text, sqlc.narg(scope_service), sqlc.narg(scope_alert), sqlc.narg(scope_route), sqlc.narg(scope_strategy),
        @status, @origin, sqlc.narg(created_by))
RETURNING id;

-- name: UpdateLesson :execrows
-- UpdateLesson replaces the text, scope and status of a lesson and raises its
-- version. It changes nothing, and returns 0 rows, when the lesson is not at
-- the version the caller read.
UPDATE lessons
SET text = @text,
    scope_service = sqlc.narg(scope_service), scope_alert = sqlc.narg(scope_alert),
    scope_route = sqlc.narg(scope_route), scope_strategy = sqlc.narg(scope_strategy),
    status = @status, version = version + 1, updated_at = now()
WHERE id = @id AND version = @version;

-- name: ApproveLesson :execrows
-- ApproveLesson makes a lesson that waits for review active, notes who
-- approved it and raises its version. It changes nothing, and returns 0
-- rows, when the lesson is not at the version the caller read, or does not
-- wait for review.
UPDATE lessons
SET status = 'active', approved_by = sqlc.narg(approved_by), version = version + 1, updated_at = now()
WHERE id = @id AND version = @version AND status = 'proposed';

-- name: DeleteLesson :execrows
-- DeleteLesson deletes a lesson, and with it the record of the
-- investigations that used it.
DELETE FROM lessons WHERE id = @id;

-- name: ListLessonsUsedBy :many
-- ListLessonsUsedBy returns the lessons an investigation used, in the order
-- they were written.
SELECT l.id, l.text, l.scope_service, l.scope_alert, l.scope_route, l.scope_strategy, l.status
FROM lesson_uses AS u
JOIN lessons AS l ON l.id = u.lesson_id
WHERE u.investigation_id = @investigation_id
ORDER BY l.created_at, l.id
LIMIT sqlc.arg(max_rows)::int;

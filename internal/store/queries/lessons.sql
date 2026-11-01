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
ON CONFLICT DO NOTHING;

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

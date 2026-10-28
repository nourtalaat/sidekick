-- name: AnyUnlistedInvestigation :one
-- AnyUnlistedInvestigation reports whether an investigation still waits
-- for the backfill of the list's columns.
SELECT EXISTS (SELECT 1 FROM investigations WHERE alert_title IS NULL) AS unlisted;

-- name: BackfillInvestigationList :execrows
-- BackfillInvestigationList fills the list's columns of up to batch
-- investigations that were created before they existed, oldest first,
-- and counts the rows it filled. The title is empty when the group's
-- first event has none, which marks the row as filled. The rows are
-- picked through investigations_unlisted into an array of their ctids
-- (their places in the table) and updated by a TID scan of that array,
-- so the plan never joins the table to itself and cannot turn into a scan
-- of the whole table. A row updated in between, such as one that a
-- running investigation writes to, is left for the next run.
UPDATE investigations AS i
SET alert_title = coalesce((
      SELECT left(coalesce(nullif(e.title, ''), nullif(e.alert_name, ''), e.body, ''), 200)
      FROM events e
      WHERE e.group_id = i.group_id
      ORDER BY e.received_at, e.id
      LIMIT 1
    ), ''),
    service = coalesce(i.service, (SELECT g.service FROM event_groups g WHERE g.id = i.group_id)),
    model = coalesce(i.model, (
      SELECT s.model FROM steps s
      WHERE s.investigation_id = i.id AND s.kind = 'model_turn'
      ORDER BY s.seq
      LIMIT 1
    ))
WHERE i.ctid = ANY (ARRAY(
  SELECT u.ctid FROM investigations u
  WHERE u.alert_title IS NULL
  ORDER BY u.created_at
  LIMIT @batch
));

-- name: ListInvestigations :many
-- ListInvestigations returns a page of investigations, newest first, that
-- match every filter given, with the ratings of the verdict of the current
-- run and the newest watcher of the group, whose name and status are
-- empty when it has none. A page starts after the
-- investigation with cursor_id created at cursor_at. The page is found
-- first, through the index of the filter, and only its rows are joined.
-- The text filter matches the words of the alert title, through the
-- expression of investigations_list_title. Run it with a plan made for
-- the values it gets, so a filter that is not given costs nothing.
SELECT i.id, i.alert_title, i.service, i.route, i.verdict, i.confidence, i.status, i.created_at, i.started_at,
       i.finished_at, i.cost_micros, fb.correct, fb.wrong, coalesce(w.name, '')::text AS watcher_name, coalesce(w.status, '')::text AS watcher_status
FROM (
  SELECT p.id, p.group_id, p.run, p.alert_title, p.service, p.route, p.verdict, p.confidence, p.status,
         p.created_at, p.started_at, p.finished_at, p.cost_micros
  FROM investigations p
  WHERE (sqlc.narg(created_from)::timestamptz IS NULL OR p.created_at >= sqlc.narg(created_from)::timestamptz)
    AND (sqlc.narg(created_to)::timestamptz IS NULL OR p.created_at < sqlc.narg(created_to)::timestamptz)
    AND (sqlc.narg(route)::text IS NULL OR p.route = sqlc.narg(route)::text)
    AND (sqlc.narg(service)::text IS NULL OR p.service = sqlc.narg(service)::text)
    AND (sqlc.narg(model)::text IS NULL OR p.model = sqlc.narg(model)::text)
    AND (sqlc.narg(verdict)::text IS NULL OR p.verdict = sqlc.narg(verdict)::text)
    AND (sqlc.narg(status)::text IS NULL OR p.status = sqlc.narg(status)::text)
    AND (sqlc.narg(search)::text IS NULL
         OR to_tsvector('english', coalesce(p.alert_title, '')) @@ websearch_to_tsquery('english', sqlc.narg(search)::text))
    AND (sqlc.narg(cursor_at)::timestamptz IS NULL
         OR (p.created_at, p.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
  ORDER BY p.created_at DESC, p.id DESC
  LIMIT @page_size
) AS i
CROSS JOIN LATERAL (
  SELECT count(*) FILTER (WHERE f.rating = 'correct') AS correct, count(*) FILTER (WHERE f.rating = 'wrong') AS wrong
  FROM feedback f
  WHERE f.investigation_id = i.id AND f.target_kind = 'verdict' AND f.target_ref = 'run:' || i.run::text || '/verdict'
) AS fb
LEFT JOIN LATERAL (
  SELECT x.name, x.status FROM watchers x
  WHERE x.group_id = i.group_id
  ORDER BY x.started_at DESC, x.id DESC
  LIMIT 1
) AS w ON true
ORDER BY i.created_at DESC, i.id DESC;

-- name: GetInvestigationGroup :one
-- GetInvestigationGroup returns the ID of the group of an investigation.
-- It returns no row for an investigation that does not exist.
SELECT group_id FROM investigations WHERE id = @id;

-- name: GetInvestigationStatus :one
-- GetInvestigationStatus returns what the event stream shows of an
-- investigation's status, without its report.
SELECT status, run, run_kind, verdict, confidence, stop_reason, finished_at, cancel_requested_at,
       confirmed_verdict, cost_micros, input_tokens, output_tokens, tool_calls
FROM investigations
WHERE id = @id;

-- name: ListenInvestigationUpdates :exec
LISTEN investigation_updates;

-- name: GetInvestigationDetail :one
-- GetInvestigationDetail returns an investigation with the number of
-- events in its group, and the group's status and closing reason.
SELECT sqlc.embed(investigations), event_groups.event_count,
       event_groups.status AS group_status, event_groups.closed_reason AS group_closed_reason
FROM investigations
JOIN event_groups ON event_groups.id = investigations.group_id
WHERE investigations.id = @id;

-- name: ListGroupEvents :many
-- ListGroupEvents returns the first events of a group, in the order they
-- arrived.
SELECT * FROM events WHERE group_id = @group_id ORDER BY received_at, id LIMIT @page_size;

-- name: ListPendingFollowUps :many
-- ListPendingFollowUps returns the follow-up questions of an investigation
-- that no run has taken yet, oldest first.
SELECT id, body, created_at FROM investigation_inbox
WHERE investigation_id = @investigation_id AND kind = 'follow_up' AND delivered_seq IS NULL
ORDER BY id
LIMIT @page_size;

-- name: ListStepsPage :many
-- ListStepsPage returns the steps of an investigation with a seq above
-- after, in order, of one run when run is given.
SELECT * FROM steps
WHERE investigation_id = @investigation_id AND seq > @after::int
  AND (sqlc.narg(run)::int IS NULL OR run = sqlc.narg(run)::int)
ORDER BY seq
LIMIT @page_size;

-- name: ListRecentWatcherChecks :many
-- ListRecentWatcherChecks returns the last per_watcher checks of each of
-- the watchers with watcher_ids, newest first.
SELECT c.watcher_id, c.seq, c.at, c.result, c.detail, c.duration_ms
FROM unnest(@watcher_ids::uuid[]) AS w(id)
CROSS JOIN LATERAL (
  SELECT k.watcher_id, k.seq, k.at, k.result, k.detail, k.duration_ms
  FROM watcher_checks k
  WHERE k.watcher_id = w.id
  ORDER BY k.seq DESC
  LIMIT @per_watcher
) AS c
ORDER BY c.watcher_id, c.seq DESC;

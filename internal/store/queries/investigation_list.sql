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

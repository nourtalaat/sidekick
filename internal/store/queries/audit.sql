-- name: InsertAuditEntry :exec
-- InsertAuditEntry adds one row to the audit log. Run it in the
-- transaction that makes the change it records.
INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, investigation_id, details)
VALUES (@at, @actor_kind, @actor_id, @action, @target_kind, @target_id, @investigation_id, @details);

-- name: ListAuditEntries :many
-- ListAuditEntries returns a page of audit entries, newest first, that
-- match every filter given, with the name of the user or API token that
-- made the change when it is still known. A page starts after the entry
-- with cursor_id made at cursor_at. The page is found first, through the
-- index of the filter, and only its rows are joined. Run it with a plan
-- made for the values it gets, so a filter that is not given costs nothing.
-- An actor_id that is not a UUID, such as a Slack user ID, has no name.
SELECT a.id, a.at, a.actor_kind, a.actor_id, a.action, a.target_kind, a.target_id, a.investigation_id, a.details,
       coalesce(u.name, t.name, '')::text AS actor_name
FROM (
  SELECT p.id, p.at, p.actor_kind, p.actor_id, p.action, p.target_kind, p.target_id, p.investigation_id, p.details
  FROM audit_log p
  WHERE (sqlc.narg(at_from)::timestamptz IS NULL OR p.at >= sqlc.narg(at_from)::timestamptz)
    AND (sqlc.narg(at_to)::timestamptz IS NULL OR p.at < sqlc.narg(at_to)::timestamptz)
    AND (sqlc.narg(actor_id)::text IS NULL OR p.actor_id = sqlc.narg(actor_id)::text)
    AND (sqlc.narg(actor_kind)::text IS NULL OR p.actor_kind = sqlc.narg(actor_kind)::text)
    AND (sqlc.narg(action)::text IS NULL OR p.action = sqlc.narg(action)::text)
    AND (sqlc.narg(investigation_id)::uuid IS NULL OR p.investigation_id = sqlc.narg(investigation_id)::uuid)
    AND (sqlc.narg(cursor_at)::timestamptz IS NULL
         OR (p.at, p.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::bigint))
  ORDER BY p.at DESC, p.id DESC
  LIMIT @page_size
) AS a
CROSS JOIN LATERAL (
  SELECT CASE WHEN a.actor_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN a.actor_id::uuid END AS id
) AS who
LEFT JOIN users u ON a.actor_kind = 'user' AND u.id = who.id
LEFT JOIN api_tokens t ON a.actor_kind = 'token' AND t.id = who.id
ORDER BY a.at DESC, a.id DESC;

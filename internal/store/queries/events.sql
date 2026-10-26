-- name: InsertEvent :one
-- InsertEvent stores an event and returns its ID. When the intake already
-- has an event with the same key, it inserts nothing and returns no row.
INSERT INTO events (
  id, intake, key, kind, sender_kind, sender_id, sender_name, trusted,
  alert_name, title, body, service, severity, labels, fingerprint, slack,
  raw, status, skip_reason, starts_at, received_at
)
VALUES (
  @id,
  @intake,
  @key,
  @kind,
  sqlc.narg(sender_kind),
  sqlc.narg(sender_id),
  sqlc.narg(sender_name),
  @trusted,
  sqlc.narg(alert_name),
  sqlc.narg(title),
  sqlc.narg(body),
  sqlc.narg(service),
  sqlc.narg(severity),
  @labels,
  @fingerprint,
  sqlc.narg(slack),
  sqlc.narg(raw),
  @status,
  sqlc.narg(skip_reason),
  sqlc.narg(starts_at),
  @received_at
)
ON CONFLICT (intake, key) DO NOTHING
RETURNING id;

-- name: GetEvent :one
SELECT * FROM events WHERE id = @id;

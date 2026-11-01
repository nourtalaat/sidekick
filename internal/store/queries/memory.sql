-- name: UpsertMemoryEntry :execrows
-- UpsertMemoryEntry writes the memory entry of a confirmed investigation
-- from its confirmed verdict and cause, its report's summary (or reason,
-- when the summary is empty), and the service, alert name and fingerprint
-- of the event that opened its group. An existing entry is updated and
-- keeps its disabled flag. When the group's events are gone, for example
-- deleted by retention, an existing entry keeps the service, alert name
-- and fingerprint it has, and a new one gets none, with an empty
-- fingerprint, so it can still match by its words. It writes a row
-- exactly when the investigation exists and is confirmed.
INSERT INTO memory_entries (
  investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at
)
SELECT i.id, first_event.service, first_event.alert_name, coalesce(first_event.fingerprint, ''), i.confirmed_verdict,
       coalesce(nullif(i.report->>'summary', ''), i.report->>'reason', ''), i.confirmed_cause, i.confirmed_at
FROM investigations AS i
LEFT JOIN LATERAL (
  SELECT e.service, e.alert_name, e.fingerprint
  FROM events AS e
  WHERE e.group_id = i.group_id
  ORDER BY e.received_at, e.id
  LIMIT 1
) AS first_event ON true
WHERE i.id = @investigation_id
  AND i.confirmed_verdict IS NOT NULL
  AND i.confirmed_at IS NOT NULL
ON CONFLICT (investigation_id) DO UPDATE
SET service = coalesce(excluded.service, memory_entries.service),
    alert_name = coalesce(excluded.alert_name, memory_entries.alert_name),
    fingerprint = coalesce(nullif(excluded.fingerprint, ''), memory_entries.fingerprint),
    verdict = excluded.verdict,
    summary = excluded.summary,
    cause = excluded.cause,
    confirmed_at = excluded.confirmed_at;

-- name: DeleteUnconfirmedMemoryEntry :exec
-- DeleteUnconfirmedMemoryEntry removes the memory entry of an
-- investigation that is not confirmed, or no longer exists. The entry of a
-- confirmed investigation stays.
DELETE FROM memory_entries AS m
WHERE m.investigation_id = @investigation_id
  AND NOT EXISTS (
    SELECT 1 FROM investigations AS i
    WHERE i.id = m.investigation_id AND i.confirmed_verdict IS NOT NULL AND i.confirmed_at IS NOT NULL
  );

-- name: ListMemoryByFingerprint :many
-- ListMemoryByFingerprint returns enabled entries with the fingerprint,
-- newest confirmation first, leaving out the investigations in exclude.
SELECT investigation_id, service, alert_name, verdict, summary, cause, confirmed_at
FROM memory_entries
WHERE fingerprint = @fingerprint
  AND NOT disabled
  AND investigation_id <> ALL (@exclude::uuid[])
ORDER BY confirmed_at DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: ListMemoryByServiceAlert :many
-- ListMemoryByServiceAlert returns enabled entries with the service and
-- alert name, newest confirmation first, leaving out the investigations in
-- exclude.
SELECT investigation_id, service, alert_name, verdict, summary, cause, confirmed_at
FROM memory_entries
WHERE service = @service::text
  AND alert_name = @alert_name::text
  AND NOT disabled
  AND investigation_id <> ALL (@exclude::uuid[])
ORDER BY confirmed_at DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: ListSimilarMemory :many
-- ListSimilarMemory returns enabled entries that share at least one word
-- with text, best text-search rank first, leaving out the investigations
-- in exclude. Any word is enough, unlike websearch_to_tsquery, where every
-- word must match: the text of an alert has many words that a short
-- summary never repeats.
SELECT investigation_id, service, alert_name, verdict, summary, cause, confirmed_at
FROM memory_entries
WHERE search @@ replace(plainto_tsquery('english', @text)::text, ' & ', ' | ')::tsquery
  AND NOT disabled
  AND investigation_id <> ALL (@exclude::uuid[])
ORDER BY ts_rank(search, replace(plainto_tsquery('english', @text)::text, ' & ', ' | ')::tsquery) DESC,
         confirmed_at DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: SearchMemory :many
-- SearchMemory returns enabled entries that match a web-search style query
-- (words, "quoted phrases", or, -word), best rank first, optionally only
-- those of one service or with one verdict.
SELECT investigation_id, service, alert_name, verdict, summary, cause, confirmed_at
FROM memory_entries
WHERE search @@ websearch_to_tsquery('english', @query)
  AND NOT disabled
  AND (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service))
  AND (sqlc.narg(verdict)::text IS NULL OR verdict = sqlc.narg(verdict))
ORDER BY ts_rank(search, websearch_to_tsquery('english', @query)) DESC, confirmed_at DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: GetServiceNotes :one
-- GetServiceNotes returns the notes about a service.
SELECT service, text, updated_at FROM service_notes WHERE service = @service;

-- name: ListMemoryEntries :many
-- ListMemoryEntries returns memory entries for the web UI, newest
-- confirmation first, a page at a time, disabled ones too. Every filter is
-- optional: the service, the verdict, words (a web-search style query on
-- the service, alert name, summary and cause), whether the entry is
-- disabled, and the place after which the page starts (the confirmation
-- time and investigation ID of the last entry of the page before).
SELECT m.investigation_id, m.service, m.alert_name, m.verdict, m.summary, m.cause, m.confirmed_at, m.disabled
FROM memory_entries AS m
WHERE (sqlc.narg(service)::text IS NULL OR m.service = sqlc.narg(service)::text)
  AND (sqlc.narg(verdict)::text IS NULL OR m.verdict = sqlc.narg(verdict)::text)
  AND (sqlc.narg(search)::text IS NULL OR m.search @@ websearch_to_tsquery('english', sqlc.narg(search)::text))
  AND (sqlc.narg(disabled)::boolean IS NULL OR m.disabled = sqlc.narg(disabled)::boolean)
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (m.confirmed_at, m.investigation_id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY m.confirmed_at DESC, m.investigation_id DESC
LIMIT sqlc.arg(page_size)::int;

-- name: GetMemoryEntry :one
-- GetMemoryEntry returns one memory entry. It returns no row for an entry
-- that does not exist.
SELECT investigation_id, service, alert_name, verdict, summary, cause, confirmed_at, disabled
FROM memory_entries
WHERE investigation_id = @investigation_id;

-- name: LockMemoryEntry :one
-- LockMemoryEntry returns one memory entry and holds a lock on its row
-- until the transaction ends, so two admins who change the same entry at
-- once, or an admin and a change of the confirmation, are applied in turn.
SELECT investigation_id, service, alert_name, verdict, summary, cause, confirmed_at, disabled
FROM memory_entries
WHERE investigation_id = @investigation_id
FOR UPDATE;

-- name: UpdateMemoryEntry :execrows
-- UpdateMemoryEntry replaces the summary, cause and disabled flag of a
-- memory entry. The next change of the investigation's confirmation
-- replaces the summary and cause again, and keeps the flag.
UPDATE memory_entries
SET summary = @summary, cause = sqlc.narg(cause), disabled = @disabled
WHERE investigation_id = @investigation_id;

-- name: DeleteMemoryEntry :execrows
-- DeleteMemoryEntry deletes a memory entry, and its embedding with it. A
-- later change of the investigation's confirmation writes a new entry.
DELETE FROM memory_entries WHERE investigation_id = @investigation_id;

-- name: GetServiceNotesDetail :one
-- GetServiceNotesDetail returns the notes about a service with the name of
-- the user who last changed them, which is empty when a token did. It
-- returns no row for a service without notes.
SELECT n.service, n.text, n.updated_at,
       coalesce((SELECT u.name FROM users AS u WHERE u.id = n.updated_by), '')::text AS updated_by_name
FROM service_notes AS n
WHERE n.service = @service;

-- name: ListServiceNotes :many
-- ListServiceNotes returns the services that have notes, by name, a page at
-- a time, starting after the service after, with the first characters of
-- the notes and the name of the user who last changed them.
SELECT n.service, left(n.text, 120)::text AS preview, n.updated_at,
       coalesce((SELECT u.name FROM users AS u WHERE u.id = n.updated_by), '')::text AS updated_by_name
FROM service_notes AS n
WHERE n.service > @after::text
ORDER BY n.service
LIMIT sqlc.arg(page_size)::int;

-- name: LockServiceNotes :exec
-- LockServiceNotes takes a lock on the notes of a service, that need not
-- exist yet, until the transaction ends, so a change reads the notes it
-- replaces even when another admin writes the first notes at the same time.
SELECT pg_advisory_xact_lock(hashtextextended('service_notes:' || @service::text, 0));

-- name: UpsertServiceNotes :exec
-- UpsertServiceNotes writes the notes about a service. updated_by is the
-- Sidekick user who wrote them, or no one when a token did.
INSERT INTO service_notes (service, text, updated_by, updated_at)
VALUES (@service, @text, sqlc.narg(updated_by), now())
ON CONFLICT (service) DO UPDATE
SET text = excluded.text, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: DeleteServiceNotes :execrows
-- DeleteServiceNotes deletes the notes about a service.
DELETE FROM service_notes WHERE service = @service;

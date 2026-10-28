-- name: UpsertMemoryEntry :execrows
-- UpsertMemoryEntry writes the memory entry of a confirmed investigation
-- from its confirmed verdict and cause, its report's summary (or reason,
-- when the summary is empty), and the event that opened its group. An
-- existing entry is updated and keeps its disabled flag. It writes nothing
-- and returns 0 rows for an investigation that is not confirmed.
INSERT INTO memory_entries (
  investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at
)
SELECT i.id, first_event.service, first_event.alert_name, first_event.fingerprint, i.confirmed_verdict,
       coalesce(nullif(i.report->>'summary', ''), i.report->>'reason', ''), i.confirmed_cause, i.confirmed_at
FROM investigations AS i
CROSS JOIN LATERAL (
  SELECT e.service, e.alert_name, e.fingerprint
  FROM events AS e
  WHERE e.group_id = i.group_id
  ORDER BY e.received_at, e.id
  LIMIT 1
) AS first_event
WHERE i.id = @investigation_id
  AND i.confirmed_verdict IS NOT NULL
  AND i.confirmed_at IS NOT NULL
ON CONFLICT (investigation_id) DO UPDATE
SET service = excluded.service,
    alert_name = excluded.alert_name,
    fingerprint = excluded.fingerprint,
    verdict = excluded.verdict,
    summary = excluded.summary,
    cause = excluded.cause,
    confirmed_at = excluded.confirmed_at;

-- name: DeleteMemoryEntry :exec
-- DeleteMemoryEntry removes the memory entry of an investigation, if it has
-- one.
DELETE FROM memory_entries WHERE investigation_id = @investigation_id;

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

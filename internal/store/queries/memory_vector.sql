-- Vector memory. These queries run only when memory.vector.enabled is true,
-- on a database that has the vector migrations.

-- name: GetMemoryEmbedText :one
-- GetMemoryEmbedText returns the text that is embedded for a memory entry,
-- its alert name, service, summary and cause on separate lines, and a hash
-- of it, which UpsertMemoryEmbedding checks, so an entry that changed while
-- its text was being embedded is not stored with the old vector.
SELECT concat_ws(E'\n', alert_name, service, summary, cause)::text AS text,
       md5(concat_ws(E'\n', alert_name, service, summary, cause))::text AS text_hash
FROM memory_entries
WHERE investigation_id = @investigation_id;

-- name: UpsertMemoryEmbedding :execrows
-- UpsertMemoryEmbedding stores the embedding of a memory entry when the
-- entry still has the text with the hash text_hash, and writes nothing when
-- it changed or is gone. The share lock on the entry makes a change to it
-- wait until this transaction ends, so a job that enqueues itself after
-- the change is not missed.
INSERT INTO memory_embeddings (investigation_id, model, dimensions, embedding, embedded_at)
SELECT m.investigation_id, @model::text, @dimensions::int, @embedding::vector, now()
FROM memory_entries AS m
WHERE m.investigation_id = @investigation_id
  AND md5(concat_ws(E'\n', m.alert_name, m.service, m.summary, m.cause)) = @text_hash::text
FOR SHARE OF m
ON CONFLICT (investigation_id) DO UPDATE
SET model = excluded.model,
    dimensions = excluded.dimensions,
    embedding = excluded.embedding,
    embedded_at = excluded.embedded_at;

-- name: ListMemoryNeedingEmbedding :many
-- ListMemoryNeedingEmbedding returns, in order of their ID, up to max_rows
-- entries that come after the ID after and have no embedding or one from
-- another model, for the start-up backfill.
SELECT m.investigation_id
FROM memory_entries AS m
LEFT JOIN memory_embeddings AS e ON e.investigation_id = m.investigation_id
WHERE m.investigation_id > @after
  AND (e.investigation_id IS NULL OR e.model <> @model::text)
ORDER BY m.investigation_id
LIMIT sqlc.arg(max_rows)::int;

-- name: ListNearestMemory :many
-- ListNearestMemory returns enabled entries nearest to the embedding query
-- by cosine distance, nearest first, leaving out the investigations in
-- exclude, and optionally only those of one service or with one verdict.
-- The nearest candidates are taken from the HNSW index first, then filtered,
-- so the index decides the order; an index scan returns at most
-- hnsw.ef_search rows, 40 by default, which candidates must not exceed.
WITH nearest AS (
  SELECT e.investigation_id, e.embedding <=> @query::vector AS distance
  FROM memory_embeddings AS e
  ORDER BY e.embedding <=> @query::vector
  LIMIT sqlc.arg(candidates)::int
)
SELECT m.investigation_id, m.service, m.alert_name, m.verdict, m.summary, m.cause, m.confirmed_at
FROM nearest AS n
JOIN memory_entries AS m ON m.investigation_id = n.investigation_id
WHERE NOT m.disabled
  AND m.investigation_id <> ALL (@exclude::uuid[])
  AND (sqlc.narg(service)::text IS NULL OR m.service = sqlc.narg(service))
  AND (sqlc.narg(verdict)::text IS NULL OR m.verdict = sqlc.narg(verdict))
ORDER BY n.distance, m.confirmed_at DESC
LIMIT sqlc.arg(max_rows)::int;

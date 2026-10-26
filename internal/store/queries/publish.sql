-- name: InsertPublishState :execrows
-- InsertPublishState writes the first state of an investigation, with the
-- sinks its updates go to. A second insert for the same investigation
-- changes nothing and counts no row.
INSERT INTO publish_states (investigation_id, sinks, seq, phase, body, updated_at)
VALUES (@investigation_id, @sinks::text[], @seq, @phase, @body, now())
ON CONFLICT (investigation_id) DO NOTHING;

-- name: UpdatePublishState :one
-- UpdatePublishState replaces the state of an investigation when seq is
-- newer than the stored one, and returns the sinks. It returns no row when
-- the stored state is as new or newer, or when there is none.
UPDATE publish_states
SET seq = @seq, phase = @phase, body = @body, updated_at = now()
WHERE investigation_id = @investigation_id AND seq < @seq
RETURNING sinks;

-- name: GetPublishState :one
-- GetPublishState returns the state of an investigation for one sink: the
-- newest update, the number of events in its group, and what the sink
-- published last, if anything.
SELECT
  publish_states.seq,
  publish_states.phase,
  publish_states.body,
  event_groups.event_count,
  output_refs.ref AS previous_ref,
  output_refs.last_seq AS previous_seq
FROM publish_states
JOIN investigations ON investigations.id = publish_states.investigation_id
JOIN event_groups ON event_groups.id = investigations.group_id
LEFT JOIN output_refs
  ON output_refs.investigation_id = publish_states.investigation_id AND output_refs.sink = @sink
WHERE publish_states.investigation_id = @investigation_id;

-- name: LockPublishSeq :one
-- LockPublishSeq returns the seq of an investigation's state and holds a
-- share lock on the row until the transaction ends, so no update can be
-- written in between.
SELECT seq FROM publish_states WHERE investigation_id = @investigation_id FOR SHARE;

-- name: SaveOutputRef :exec
-- SaveOutputRef stores what a sink published for an investigation, unless
-- a ref for a newer seq is stored already.
INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
VALUES (@investigation_id, @sink, @external, @ref, @last_seq, now())
ON CONFLICT (investigation_id, sink) DO UPDATE
SET external = excluded.external,
    ref = excluded.ref,
    last_seq = excluded.last_seq,
    published_at = excluded.published_at
WHERE output_refs.last_seq < excluded.last_seq;

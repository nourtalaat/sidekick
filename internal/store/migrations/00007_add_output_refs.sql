-- Output refs: what each output of an investigation published, so the next
-- update edits that message instead of sending a new one.

-- +goose Up
CREATE TABLE output_refs (
  investigation_id uuid REFERENCES investigations (id) ON DELETE CASCADE,
  sink             text,
  -- Where the message is, in the sink's own terms; for Slack, channel:ts.
  external         text NOT NULL,
  -- The output.Ref the sink returned, passed back to it on the next update.
  ref              jsonb NOT NULL,
  -- The seq of the publish state this ref shows.
  last_seq         bigint NOT NULL,
  published_at     timestamptz NOT NULL,
  PRIMARY KEY (investigation_id, sink)
);

-- For finding the investigation of a message, such as a reply in a Slack
-- thread.
CREATE INDEX output_refs_external ON output_refs (sink, external);

-- +goose Down
DROP TABLE output_refs;

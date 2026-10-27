-- The Seq of the newest update an investigation wrote to its publish state.
-- seq can now grow without such an update, when something else the outputs
-- show changes, such as the number of events in the group; update_seq keeps
-- the order of the investigation's own updates. NULL, on states written
-- before this column, means the same as seq.

-- +goose Up
ALTER TABLE publish_states ADD COLUMN update_seq bigint;

-- +goose Down
ALTER TABLE publish_states DROP COLUMN update_seq;

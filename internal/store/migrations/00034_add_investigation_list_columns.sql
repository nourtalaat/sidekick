-- What the investigation list filters and shows without joining: the
-- alert's title and service, from the first event of the group, and the
-- model of the first model turn. The router fills the title and the
-- service when it creates the investigation, and the first model turn
-- fills the model.
--
-- Rows from before this migration are filled by the backfill job, in
-- batches. alert_title is NULL until a row is filled, and an empty string
-- when the group's first event has no title, so the job knows which rows
-- are left. Until it finishes, an old row does not match the list's
-- service, model and text filters.
--
-- The columns are added without a default, so nothing is rewritten.

-- +goose Up
ALTER TABLE investigations
  ADD COLUMN alert_title text,
  ADD COLUMN service     text,
  ADD COLUMN model       text;

-- +goose Down
ALTER TABLE investigations
  DROP COLUMN model,
  DROP COLUMN service,
  DROP COLUMN alert_title;

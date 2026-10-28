-- Memory: one entry per confirmed investigation, so later investigations of
-- similar alerts can see what people agreed the answer was. An entry is
-- written when a responder confirms an investigation's verdict, changed
-- when the confirmation changes, and deleted when it is taken back, so an
-- investigation nobody confirmed is never in memory.
--
-- An entry keeps its own copy of what it shows, with no foreign key to the
-- investigation, because memory is kept longer than investigations are.
--
-- Service notes are what people wrote about a service, for the memory
-- source's service_notes tool.

-- +goose Up
CREATE TABLE memory_entries (
  investigation_id uuid PRIMARY KEY,
  -- From the event that opened the investigation's group.
  service          text,
  alert_name       text,
  fingerprint      text NOT NULL,
  -- The confirmed verdict and cause.
  verdict          text NOT NULL,
  -- The report's summary.
  summary          text NOT NULL,
  cause            text,
  confirmed_at     timestamptz NOT NULL,
  -- An admin can disable an entry so it never matches; a later change of
  -- the confirmation keeps it disabled.
  disabled         boolean NOT NULL DEFAULT false,
  search           tsvector GENERATED ALWAYS AS (
    to_tsvector('english', coalesce(service, '') || ' ' || coalesce(alert_name, '') || ' ' || summary || ' ' || coalesce(cause, ''))
  ) STORED
);

-- Examples with the same fingerprint, newest first.
CREATE INDEX memory_entries_fingerprint ON memory_entries (fingerprint, confirmed_at DESC) WHERE NOT disabled;
-- Examples with the same service and alert name, newest first.
CREATE INDEX memory_entries_service_alert ON memory_entries (service, alert_name, confirmed_at DESC) WHERE NOT disabled;
-- Full-text examples and memory__search. Entries are written rarely, once
-- per confirmation, and read at the start of every investigation, so new
-- entries go into the index at once rather than into its pending list.
CREATE INDEX memory_entries_search ON memory_entries USING gin (search) WITH (fastupdate = off) WHERE NOT disabled;
-- The retention job, oldest confirmation first, disabled entries too.
CREATE INDEX memory_entries_confirmed ON memory_entries (confirmed_at);

CREATE TABLE service_notes (
  service    text PRIMARY KEY,
  text       text NOT NULL,
  -- The Sidekick user who last changed the notes.
  updated_by uuid,
  updated_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE service_notes;
DROP TABLE memory_entries;

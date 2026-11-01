-- Lessons: short pieces of guidance that people reviewed and that prompts
-- show to the model. A lesson is drafted from feedback (origin feedback) or
-- written by an admin in the web UI (ui) or Slack (slack), and its scope
-- says which investigations it applies to: every non-null scope field must
-- equal the investigation's value. A lesson with no scope field is global.
--
-- The database refuses a lesson drafted from feedback that has no scope
-- field; only the hand-written origins may be global.
--
-- lesson_uses lists the lessons that the prompt of each investigation, or
-- its triage, showed. steps.lessons holds the record of one selection on the
-- step that carries the prompt: the lessons used and the ones left out, with
-- the reason.
--
-- helpful and unhelpful count how people rated the verdicts of the
-- investigations that used the lesson: a rating of correct is helpful, a
-- rating of wrong is unhelpful. They are kept by the trigger below, so a
-- prompt ranks lessons from two numbers without a join.
--
-- The trigger on feedback watches the ratings of verdicts:
--
--   feedback, on insert of a rating of a verdict, or on update of its rating
--     Adds the rating to the counts of every lesson in lesson_uses for the
--     feedback's investigation, and on an update takes the old rating off
--     first, so a person who changes their mind moves one count to the
--     other. Every code path that rates a verdict is covered: the Slack
--     buttons, the Details form and the web UI.
--
-- Only lessons that the investigation had used when the rating came are
-- counted, which is what "later feedback" means. Ratings of findings, fixes
-- and tool calls are left out on purpose; they say little about a lesson.
-- Deleting feedback or investigations does not take anything off the counts:
-- they are a history of what people said.

-- +goose Up
CREATE TABLE lessons (
  id            uuid        PRIMARY KEY,
  text          text        NOT NULL CHECK (char_length(text) BETWEEN 1 AND 400 AND btrim(text) <> ''),
  scope_service text,
  scope_alert   text,
  scope_route   text,
  scope_strategy text,
  -- proposed waits for an admin; active is used in prompts; disabled and
  -- rejected are not.
  status        text        NOT NULL CHECK (status IN ('proposed', 'active', 'disabled', 'rejected')),
  -- feedback, ui, slack or file.
  origin        text        NOT NULL CHECK (origin IN ('feedback', 'ui', 'slack', 'file')),
  -- The feedback a drafted lesson comes from.
  feedback_ids  uuid[]      NOT NULL DEFAULT '{}',
  created_by    uuid,
  approved_by   uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  helpful       int         NOT NULL DEFAULT 0,
  unhelpful     int         NOT NULL DEFAULT 0,
  last_used_at  timestamptz,
  -- Goes up with every change, so two admins who edit the same lesson at the
  -- same time cannot overwrite each other.
  version       int         NOT NULL DEFAULT 1,
  search        tsvector    GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
  -- A scope field is either not set or has a value.
  CHECK (
    (scope_service IS NULL OR btrim(scope_service) <> '') AND (scope_alert IS NULL OR btrim(scope_alert) <> '')
    AND (scope_route IS NULL OR btrim(scope_route) <> '') AND (scope_strategy IS NULL OR btrim(scope_strategy) <> '')
  ),
  CHECK (origin <> 'feedback' OR num_nonnulls(scope_service, scope_alert, scope_route, scope_strategy) >= 1)
);

-- Choosing the lessons of a prompt: one index for each scope field, and one
-- for the global lessons. The candidate query reads each of them for the
-- lessons whose first scope field is that one.
CREATE INDEX lessons_active_service ON lessons (scope_service) WHERE status = 'active';
CREATE INDEX lessons_active_alert ON lessons (scope_alert) WHERE status = 'active';
CREATE INDEX lessons_active_route ON lessons (scope_route) WHERE status = 'active';
CREATE INDEX lessons_active_strategy ON lessons (scope_strategy) WHERE status = 'active';
-- The global lessons are the active ones with no scope field. The index is
-- on scope_service, which is NULL for all of them, so a query for
-- scope_service IS NULL can use it.
CREATE INDEX lessons_active_global ON lessons (scope_service)
  WHERE status = 'active' AND scope_alert IS NULL AND scope_route IS NULL AND scope_strategy IS NULL;
-- Searching the active lessons with memory__search.
CREATE INDEX lessons_search ON lessons USING gin (search) WHERE status = 'active';
-- Finding the lesson drafted from a feedback row.
CREATE INDEX lessons_feedback ON lessons USING gin (feedback_ids);
-- The review queue and the list, by status, newest first.
CREATE INDEX lessons_status_created ON lessons (status, created_at DESC, id DESC);

CREATE TABLE lesson_uses (
  lesson_id        uuid NOT NULL REFERENCES lessons (id) ON DELETE CASCADE,
  investigation_id uuid NOT NULL REFERENCES investigations (id) ON DELETE CASCADE,
  PRIMARY KEY (lesson_id, investigation_id)
);

-- The lessons an investigation used, and the counts the trigger updates.
CREATE INDEX lesson_uses_investigation ON lesson_uses (investigation_id);

ALTER TABLE steps ADD COLUMN lessons jsonb;

-- +goose StatementBegin
CREATE FUNCTION lessons_count_rating() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  helpful_change int := (NEW.rating = 'correct')::int;
  unhelpful_change int := (NEW.rating = 'wrong')::int;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    helpful_change := helpful_change - (OLD.rating = 'correct')::int;
    unhelpful_change := unhelpful_change - (OLD.rating = 'wrong')::int;
  END IF;

  UPDATE lessons
  SET helpful = helpful + helpful_change, unhelpful = unhelpful + unhelpful_change
  WHERE id IN (SELECT lesson_id FROM lesson_uses WHERE investigation_id = NEW.investigation_id);

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER feedback_lessons_insert
  AFTER INSERT ON feedback
  FOR EACH ROW
  WHEN (NEW.target_kind = 'verdict')
  EXECUTE FUNCTION lessons_count_rating();

CREATE TRIGGER feedback_lessons_update
  AFTER UPDATE ON feedback
  FOR EACH ROW
  WHEN (NEW.target_kind = 'verdict' AND OLD.rating IS DISTINCT FROM NEW.rating)
  EXECUTE FUNCTION lessons_count_rating();

-- +goose Down
DROP TRIGGER feedback_lessons_update ON feedback;
DROP TRIGGER feedback_lessons_insert ON feedback;
DROP FUNCTION lessons_count_rating();
ALTER TABLE steps DROP COLUMN lessons;
DROP TABLE lesson_uses;
DROP TABLE lessons;

-- Tells every replica, once the writing transaction commits, that an
-- investigation changed, so the replica that serves a browser's event
-- stream can send what is new without running the investigation. The
-- notifications go to the channel investigation_updates and carry the
-- investigation ID, what changed and where:
--
--   <id>:step:<seq>        a step was saved
--   <id>:status:<status>   the status, the run or the stop request changed,
--                          or the confirmed verdict did
--   <id>:watchers          a watcher of the investigation started, ran a
--                          check or ended
--
-- A trigger sends them, in the transaction of the change, so every code
-- path that saves a step, ends a run or changes a watcher is covered,
-- including ones added later. A notification only says that something
-- changed; the stream reads the rows. Postgres sends one notification per
-- distinct payload in a transaction.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION notify_investigation_step() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('investigation_updates', NEW.investigation_id::text || ':step:' || NEW.seq::text);

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER steps_notify
  AFTER INSERT ON steps
  FOR EACH ROW EXECUTE FUNCTION notify_investigation_step();

-- +goose StatementBegin
CREATE FUNCTION notify_investigation_status() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('investigation_updates', NEW.id::text || ':status:' || NEW.status);

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER investigations_notify
  AFTER UPDATE ON investigations
  FOR EACH ROW
  WHEN (
    OLD.status IS DISTINCT FROM NEW.status
    OR OLD.run IS DISTINCT FROM NEW.run
    OR OLD.cancel_requested_at IS DISTINCT FROM NEW.cancel_requested_at
    OR OLD.confirmed_at IS DISTINCT FROM NEW.confirmed_at
  )
  EXECUTE FUNCTION notify_investigation_status();

-- +goose StatementBegin
CREATE FUNCTION notify_investigation_watchers() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('investigation_updates', NEW.investigation_id::text || ':watchers');

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER watchers_notify
  AFTER INSERT OR UPDATE ON watchers
  FOR EACH ROW
  WHEN (NEW.investigation_id IS NOT NULL)
  EXECUTE FUNCTION notify_investigation_watchers();

-- +goose Down
DROP TRIGGER watchers_notify ON watchers;
DROP FUNCTION notify_investigation_watchers();
DROP TRIGGER investigations_notify ON investigations;
DROP FUNCTION notify_investigation_status();
DROP TRIGGER steps_notify ON steps;
DROP FUNCTION notify_investigation_step();

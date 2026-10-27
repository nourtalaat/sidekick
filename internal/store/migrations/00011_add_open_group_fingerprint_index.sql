-- +goose NO TRANSACTION
-- At most one open group per fingerprint, so events with the same
-- fingerprint join one group even when several replicas group them at once.
--
-- Before this, every routed event opened a group of its own, so a database
-- can hold several open groups with the same fingerprint. All but the newest
-- of them are closed first, as superseded. The index is built without
-- blocking writes, which needs a migration outside a transaction. A run that
-- failed part way, for example because an older replica opened another
-- duplicate meanwhile, can leave an invalid index behind; running the
-- migration again drops it, closes the duplicates again and builds it anew.

-- +goose Up
UPDATE event_groups AS g
SET status = 'closed', closed_reason = 'superseded'
WHERE g.status = 'open'
  AND EXISTS (
    SELECT 1 FROM event_groups AS newer
    WHERE newer.fingerprint = g.fingerprint
      AND newer.status = 'open'
      AND (newer.opened_at, newer.id) > (g.opened_at, g.id)
  );

DROP INDEX CONCURRENTLY IF EXISTS event_groups_open_fingerprint;

CREATE UNIQUE INDEX CONCURRENTLY event_groups_open_fingerprint
  ON event_groups (fingerprint) WHERE status = 'open';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS event_groups_open_fingerprint;

-- This fails if it runs twice, and it sleeps so that a second migrator
-- started at the same time tries to run while this one holds the lock.

-- +goose Up
CREATE TABLE migrated_once (id integer PRIMARY KEY);
SELECT pg_sleep(1);

-- +goose Down
DROP TABLE migrated_once;

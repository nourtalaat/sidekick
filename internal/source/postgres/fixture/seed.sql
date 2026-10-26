-- The watched database of each integration test: a table that sessions
-- lock and update, and the pg_stat_statements extension.
CREATE EXTENSION pg_stat_statements;

CREATE TABLE accounts (
	id integer PRIMARY KEY,
	owner text NOT NULL,
	balance integer NOT NULL
);

INSERT INTO accounts (id, owner, balance)
SELECT i, 'owner-' || i, 100
FROM generate_series(1, 1000) AS i;

ANALYZE accounts;

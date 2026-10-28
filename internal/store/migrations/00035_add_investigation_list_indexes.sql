-- +goose NO TRANSACTION
-- The indexes of the investigation list. Every filter has an index that
-- starts with the filter and goes on in the list's order, newest first, so
-- a page is read from the start of one index range and stops after the
-- page's rows. The bare index serves the list without a filter and the
-- time range. Filters that are NULL for many rows have partial indexes.
-- The text index serves the search of the alert title.
--
-- The indexes are built without blocking writes, which needs a migration
-- outside a transaction. A run that failed part way leaves an invalid
-- index, which running the migration again drops and builds anew.

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS investigations_list;
CREATE INDEX CONCURRENTLY investigations_list ON investigations (created_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS investigations_list_route;
CREATE INDEX CONCURRENTLY investigations_list_route ON investigations (route, created_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS investigations_list_status;
CREATE INDEX CONCURRENTLY investigations_list_status ON investigations (status, created_at DESC, id DESC);

DROP INDEX CONCURRENTLY IF EXISTS investigations_list_service;
CREATE INDEX CONCURRENTLY investigations_list_service ON investigations (service, created_at DESC, id DESC) WHERE service IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS investigations_list_model;
CREATE INDEX CONCURRENTLY investigations_list_model ON investigations (model, created_at DESC, id DESC) WHERE model IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS investigations_list_verdict;
CREATE INDEX CONCURRENTLY investigations_list_verdict ON investigations (verdict, created_at DESC, id DESC) WHERE verdict IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS investigations_list_title;
CREATE INDEX CONCURRENTLY investigations_list_title ON investigations USING gin (to_tsvector('english', coalesce(alert_title, '')));

-- The rows the backfill job has left, oldest first; empty once it is done.
DROP INDEX CONCURRENTLY IF EXISTS investigations_unlisted;
CREATE INDEX CONCURRENTLY investigations_unlisted ON investigations (created_at) WHERE alert_title IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS investigations_unlisted;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list_title;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list_verdict;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list_model;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list_service;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list_status;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list_route;
DROP INDEX CONCURRENTLY IF EXISTS investigations_list;

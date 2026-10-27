// Package postgres is the native source for the Postgres databases that
// Sidekick watches, not Sidekick's own. It registers itself as the source
// type "postgres".
//
// Its tools read the statistics views of each configured database, such as
// pg_stat_activity. Every query is fixed SQL that runs in a READ ONLY
// transaction with a statement timeout and a lock timeout; the model only
// picks the database and a few limits. The connection URL of each database
// comes from a secret that is read on each call, and the database's
// connection pool is rebuilt only when the secret changes.
package postgres

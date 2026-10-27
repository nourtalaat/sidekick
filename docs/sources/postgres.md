# Postgres source

The Postgres source (`type: postgres`) lets investigations look inside a
Postgres database that your services use: the statements that take the
most time, the queries running now, sessions waiting for locks and the
sessions blocking them, and replication lag. It is for the databases
Sidekick watches, not for Sidekick's own database, and it only reads.

This page lists the smallest set of permissions the source needs, how to
give Sidekick the connection URL, and what Sidekick does to keep its
queries harmless. The source's config keys are in
[Configuration](../configuration.md#postgres-sources-type-postgres).

The source works with Postgres 14 and newer.

## What the source reads

| Tool | Reads |
|---|---|
| `slow_queries` | `pg_stat_statements`, when the extension is installed. |
| `running_queries` | `pg_stat_activity`. |
| `lock_waits` | `pg_locks` joined to `pg_stat_activity`, and `pg_blocking_pids`. |
| `replication_lag` | `pg_stat_replication` on a primary; `pg_stat_wal_receiver`, `pg_last_xact_replay_timestamp()` and the WAL positions on a standby. |

Every query is fixed in Sidekick's code. The model picks only the database,
from the names in the config, and a few limits: a minimum duration, an
order and a row count. It never writes SQL.

## Permissions

Make a role for Sidekick that can log in and is a member of `pg_monitor`,
and give it nothing else:

```sql
CREATE ROLE sidekick_monitor LOGIN PASSWORD '<a long random password>';
GRANT pg_monitor TO sidekick_monitor;
```

`pg_monitor` is a built-in role that can read the statistics views,
including the query text of other users' sessions, and the server's
settings. It cannot read or change any table. Without it, the tools see
only the role's own sessions and statements, and Sidekick logs a warning
when it connects.

Sidekick refuses to connect as a superuser. Do not grant the role any
table, schema or other role either: the tools would not use it, and a
leaked password should not give more than the statistics.

Roles are per server, but the right to connect is per database. Postgres
gives every role `CONNECT` on every database unless someone revoked it;
if so, grant it for each database Sidekick reads:

```sql
GRANT CONNECT ON DATABASE orders TO sidekick_monitor;
```

Allow the role in `pg_hba.conf` over TLS only, for example
`hostssl orders sidekick_monitor 10.0.0.0/8 scram-sha-256`.

Managed services such as Amazon RDS, Google Cloud SQL and Azure Database
for PostgreSQL have `pg_monitor` too; run the statements above as the
admin user the service gives you.

### Turning on pg_stat_statements

`slow_queries` needs the `pg_stat_statements` extension. Without it, the
tool reports that it has no data and the other tools work as usual.

1. Add it to `shared_preload_libraries` in `postgresql.conf`, for example
   `shared_preload_libraries = 'pg_stat_statements'`, and restart
   Postgres. Many managed services load it already, or have a parameter
   for it.
2. In each database Sidekick reads, create the extension as a superuser
   or the service's admin user:

   ```sql
   CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
   ```

The statistics count from the last reset, so totals cover however long
that has been.

## The connection URL

Each database's connection URL is in a Secret, as a `postgres://` URL with
the role and its password:

```text
postgres://sidekick_monitor:<password>@orders-db.internal:5432/orders?sslmode=verify-full&sslrootcert=/var/run/secrets/sidekick/sidekick-orders-db/ca.crt
```

Store it, and the certificate of the CA that signed the server's
certificate when it is not a public one, in a Kubernetes Secret:

```sh
kubectl create secret generic sidekick-orders-db \
  --from-literal=url='postgres://sidekick_monitor:...@orders-db.internal:5432/orders?sslmode=verify-full&sslrootcert=/var/run/secrets/sidekick/sidekick-orders-db/ca.crt' \
  --from-file=ca.crt=./orders-db-ca.crt
```

Sidekick reads each secret from the file `<secrets.dir>/<name>/<key>`
(by default under `/var/run/secrets/sidekick`), so with the Secret mounted
there, `sslrootcert` can point at the CA file from the same Secret, as
above.

Then add the database to the source:

```yaml
sources:
  db:
    type: postgres
    databases:
      - name: orders
        urlSecret: { name: sidekick-orders-db, key: url }
```

Sidekick reads the Secret on every call and opens new connections when
the URL changed, so you can rotate the password without a restart: add
the new password to the role, update the Secret, then remove the old one.
Connections opened with the old URL close once their calls end.

### TLS

The URL must set `sslmode=verify-full` or `sslmode=verify-ca`, so Sidekick
checks the server's certificate and nobody on the network path can read or
change the queries and their results. `verify-full` also checks the host
name and is the better choice. A URL without `sslmode` uses `prefer`,
which does not check the certificate, so it is refused too.

The rule does not apply when every host in the URL is `localhost`, a
loopback address or a Unix socket, where TLS adds nothing.

For a server that cannot offer a certificate Sidekick can check, set
`allowInsecure: true` on that database. Sidekick then accepts any
`sslmode` and logs a warning each time it connects with the URL.

## How Sidekick keeps its queries harmless

- Each tool call runs in a `READ ONLY` transaction, which Postgres rolls
  back at the end, so even a role with more privileges could not write
  through it.
- Each transaction sets `statement_timeout` to `statementTimeout` (default
  `10s`) and `lock_timeout` to 1 second, so a query never runs long or
  queues behind a lock. A query that hits either limit goes to the model
  as an error that says so, which is itself a sign that the database is in
  trouble.
- Sidekick opens at most `concurrency` connections per database (default
  4), named `sidekick` in `pg_stat_activity`, and closes them after 5
  minutes without a call.
- Every tool returns at most `limit` rows (default 20, at most 100), and
  the text of each query is cut at 2 KiB.

## Query text

Query text in `pg_stat_activity` and `pg_stat_statements` can hold literal
values, such as an email address in a `WHERE` clause. Sidekick redacts the
values its redaction rules recognize before the model sees them, but it
cannot know every kind of sensitive value. Set `omitQueryText: true` to
leave the text out of every result; the tools still return the query ID,
timings, users and lock details.

## When something is wrong

- **Every call fails with "the role is a superuser"**: the URL's role is a
  superuser. Use a role with only `pg_monitor`.
- **Every call fails with "the URL does not verify the server's TLS
  certificate"**: set `sslmode=verify-full` or `verify-ca` in the URL, or
  `allowInsecure` on the database.
- **The log warns that the role is not a member of `pg_monitor`**: grant
  it, or the tools see only the role's own sessions.
- **`slow_queries` answers that `pg_stat_statements` must be loaded via
  `shared_preload_libraries`**: the extension is created in the database
  but not loaded at start-up; see step 1 above.
- **Query text shows as `<insufficient privilege>`**: the role lacks
  `pg_monitor`.

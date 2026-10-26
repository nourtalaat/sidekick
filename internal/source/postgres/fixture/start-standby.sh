#!/bin/sh
# Starts a streaming standby of the container's Postgres server, in the same
# container, on port 5433. Runs as the postgres user. The image trusts local
# replication connections, so the base backup needs no password.
set -eu

pg_basebackup --pgdata=/tmp/standby --write-recovery-conf --wal-method=stream \
	--checkpoint=fast --host=/var/run/postgresql --username="$1"
chmod 700 /tmp/standby
pg_ctl --pgdata=/tmp/standby --log=/tmp/standby.log --wait \
	--options="-p 5433 -c listen_addresses=* -c shared_buffers=16MB -c unix_socket_directories=/tmp" start

#!/bin/sh
# Creates the next goose migration in internal/store/migrations, numbered one
# above the highest existing one. Usage: scripts/migrate-new.sh <short_name>
set -eu

dir="internal/store/migrations"
name="${1:-}"

if ! printf '%s' "$name" | grep -Eq '^[a-z0-9_]+$'; then
	echo "usage: make migrate-new name=<short_name>, using a-z, 0-9 and _ only" >&2
	exit 1
fi

last="$(find "$dir" -maxdepth 1 -name '[0-9][0-9][0-9][0-9][0-9]_*.sql' -exec basename {} \; | sort | tail -n 1 | cut -c 1-5)"
# expr reads 00008 as decimal, where $((...)) could read it as octal.
next="$(printf '%05d' "$(expr "${last:-0}" + 1)")"
file="$dir/${next}_${name}.sql"

cat >"$file" <<'SQL'
-- +goose Up

-- +goose Down
SQL

echo "Created $file"

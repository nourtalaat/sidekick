#!/bin/sh
# Runs `make generate` and fails if it changed, added or removed any file.
# It compares snapshots of the working tree, so uncommitted work is fine:
# only changes made by the generators count.
set -eu

index="$(git rev-parse --git-path sidekick-verify-generated.index)"

snapshot() {
	cp "$(git rev-parse --git-path index)" "$index"
	GIT_INDEX_FILE="$index" git add --all
	GIT_INDEX_FILE="$index" git write-tree
}

before="$(snapshot)"
"${MAKE:-make}" --no-print-directory generate
after="$(snapshot)"

if [ "$before" != "$after" ]; then
	echo "Generated files are out of date. Run 'make generate' and commit the result:" >&2
	git diff --stat "$before" "$after" >&2
	exit 1
fi

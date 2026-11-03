#!/usr/bin/env bash
# Fails when a tracked file cites a requirement ID from the private design
# documents, or names those documents. Run it from the repository root.
#
# The public repository never points at the private documents, so a comment
# says the thing in words instead of citing an ID. There are no exclusions: if a
# fixture ever needs text that looks like an ID, reword it.
#
# The letters of the document names are written in brackets below so this file
# does not match its own patterns.
set -euo pipefail

# Requirement IDs: a prefix, a dash and a number, not part of a longer word.
id_pattern='(^|[^A-Za-z])(ACT|SRC|UI|BM|WT|MC|CFG|VR|INV|FB|RT|OUT|IN|DUR|SEC|OBS)-[0-9]+'

# The product requirements document, and the architecture document in any
# form except a public standard cited by its number (for example RFC 3339).
# The architecture document is cited bare, by a section number such as 19.3,
# or by a number that starts with 0.
doc_pattern='(^|[^A-Za-z])(PR[D]([^A-Za-z]|$)|RF[C](s|$|[^A-Za-z0-9 ]| [^0-9]| 0[0-9]*| [0-9]{1,2}\.[0-9]))'

found=0

for pattern in "$id_pattern" "$doc_pattern"; do
	status=0
	matches=$(git grep -nIE -e "$pattern") || status=$?

	if [ "$status" -eq 0 ]; then
		echo "$matches"
		found=1
	elif [ "$status" -ne 1 ]; then
		echo "check-private-ids: git grep failed with status $status" >&2
		exit "$status"
	fi
done

if [ "$found" -ne 0 ]; then
	echo >&2
	echo "The lines above cite a requirement ID or name a private design document." >&2
	echo "Say it in words instead; the public repository does not point at those documents." >&2
	exit 1
fi

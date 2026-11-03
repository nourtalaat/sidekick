#!/usr/bin/env bash
# Tests scripts/check-private-ids.sh in a throwaway Git repository: it must
# fail, naming the file and line, on text that cites a private requirement ID
# or names a private design document, and pass on text that does not.
# Sample text is built from pieces so this file does not match the check.
set -euo pipefail

check="$(cd "$(dirname "$0")" && pwd)/check-private-ids.sh"

work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

id="ACT-""1"
product="PR""D"
architecture="RF""C"

failures=0

# run_case <name> <want: pass|fail> <file content> [<path>]
# Writes the content to a tracked file in a new repository and runs the check.
run_case() {
	local name=$1 want=$2 content=$3 path=${4:-sample.go}
	local repo output status=0

	repo=$(mktemp -d "$work/case.XXXXXX")
	git -C "$repo" init -q
	mkdir -p "$repo/$(dirname "$path")"
	printf '%s\n' "package sample" "$content" >"$repo/$path"
	git -C "$repo" add .

	output=$(cd "$repo" && bash "$check" 2>&1) || status=$?

	if [ "$want" = fail ] && [ "$status" -ne 1 ]; then
		echo "FAIL: $name: want exit 1, got $status"
		failures=$((failures + 1))
	elif [ "$want" = pass ] && [ "$status" -ne 0 ]; then
		echo "FAIL: $name: want exit 0, got $status"
		failures=$((failures + 1))
	elif [ "$want" = fail ] && [[ $output != *"$path:2:"* ]]; then
		echo "FAIL: $name: output does not name file and line $path:2:"
		echo "$output"
		failures=$((failures + 1))
	else
		echo "ok: $name"
	fi
}

run_case "a requirement ID in a comment fails" fail "// Mode is a route's action mode ($id)."
run_case "a requirement ID in a YAML comment fails" fail "# A webhook without authentication ($id)." fixture/sample.yaml
run_case "a range of IDs fails" fail "// Validate checks the report against VR-""1 to VR-""7."
run_case "a comment without an ID passes" pass "// Mode is a route's action mode."
run_case "a ticket key that is not a requirement prefix passes" pass '// See OPS-123 and ENG-4.'
run_case "a prefix inside a longer word passes" pass '// The XACT-1 case and the REACT-2 case.'
run_case "a prefix without a number passes" pass '// The ACT-based rule and the SEC-less test.'

run_case "the product document fails" fail "// Guards against hiding real incidents ($product risk 1)."
run_case "the architecture document, bare, fails" fail "// The design is in the $architecture."
run_case "the architecture document, plural, fails" fail "// Private design docs ($product, ${architecture}s)"
run_case "the architecture document by number fails" fail "// See $architecture 0001."
run_case "the architecture document by section fails" fail "// Tests follow $architecture 19.3."
run_case "a public standard by number passes" pass "// A time in $architecture 3339 form, and $architecture 9457 problem details."
run_case "a word that contains the letters passes" pass "// The ${product}X and ${architecture}X names are not documents."

run_case "an ID under bench/scenarios fails too" fail "// A scenario cites ($id)." bench/scenarios/sample.go

# An untracked file is not checked, so build artifacts and notes stay out.
untracked=$(mktemp -d "$work/untracked.XXXXXX")
git -C "$untracked" init -q
printf '%s\n' "package sample" "// ($id)" >"$untracked/sample.go"

status=0
(cd "$untracked" && bash "$check" >/dev/null 2>&1) || status=$?

if [ "$status" -ne 0 ]; then
	echo "FAIL: an untracked file passes: got exit $status"
	failures=$((failures + 1))
else
	echo "ok: an untracked file passes"
fi

if [ "$failures" -ne 0 ]; then
	echo "$failures case(s) failed" >&2
	exit 1
fi

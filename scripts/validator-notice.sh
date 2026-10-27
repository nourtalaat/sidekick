#!/bin/sh
# Writes the tools part of the validator image's NOTICE, which follows the
# Go modules part in cmd/sidekick-validate/NOTICE. For each tool that
# scripts/validator-tools.sh installed (promtool, tofu and each provider),
# it copies the tool's own license and notice files and links to its
# source. Then it lists every Go module linked into those binaries, read
# from their build information with `go version -m`, with each module's
# license and notice files, fetched through the Go module proxy by
# `go mod download`, which checks them against the checksum database.
#
# The validator image's release build runs this. With "false" as the
# second argument the module list is left out, for test builds that should
# not download every module.
#
# Usage: scripts/validator-notice.sh <tools dir> [true|false]
set -eu

tools=$1
modules=${2:-true}

. "$(dirname "$0")/notice-lib.sh"

work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

printf '\n%s\n%s\n%s\n' "$rule" "TOOLS IN THE VALIDATOR IMAGE" "$rule"
cat <<'EOF'

The validator image ships these programs unchanged, as their projects
release them. Each one's own license and notice files follow, with a link
to its source code. The Mozilla Public License 2.0 covers OpenTofu and the
three OpenTofu providers; their source code is available at those links.
EOF

: >"$work/binaries"

while read -r name version source binary; do
	section "$name $version" "$tools/licenses/$name"
	printf '\nSource code: %s\n' "$source"

	case "$binary" in
	*.zip)
		mkdir "$work/$name"
		unzip -q -d "$work/$name" "$tools/$binary"
		find "$work/$name" -maxdepth 1 -type f -name "$name"'_*' >>"$work/binaries"
		;;
	*)
		echo "$tools/$binary" >>"$work/binaries"
		;;
	esac
done <"$tools/tools.txt"

printf '\n%s\n%s\n%s\n' "$rule" "GO MODULES LINKED INTO THE TOOLS" "$rule"

if [ "$modules" != "true" ]; then
	printf '\nThe module list is left out of this test build of the image.\n'
	exit 0
fi

cat <<'EOF'

promtool, tofu and the providers are Go programs. These are the modules
linked into them, as their build information lists them, each with its
license and notice files.
EOF

# Each dep line names a module; a => line after it names the module that
# replaced it. A replacement by a local directory has no version and no
# download; its source is in the tool's own repository.
while read -r binary; do
	go version -m "$binary" | awk '
		$1 == "dep" { if (pending != "") print pending; pending = $2 "@" $3; next }
		$1 == "=>" { pending = ($3 == "" || $3 ~ /^h1:/) ? "" : $2 "@" $3; next }
		END { if (pending != "") print pending }
	'
done <"$work/binaries" | LC_ALL=C sort -u >"$work/modules"

failed=""
while read -r module; do
	if ! go_module_section "$module"; then
		failed="$failed $module"
	fi
done <"$work/modules"

if [ -n "$failed" ]; then
	echo "validator-notice: could not get the license files of:$failed" >&2
	exit 1
fi

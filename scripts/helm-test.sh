#!/bin/sh
# Tests the Helm chart without a cluster:
# - `helm template` of the quick-start values must match the golden file
#   charts/testdata/quickstart.golden.yaml;
# - each charts/testdata/<template>/<case>.values.yaml, layered on the
#   quick-start values, must render templates/<template>.yaml as
#   <case>.golden.yaml, or fail with the message in <case>.error. A
#   template that renders nothing for a case has an empty golden file;
# - the sidekick.yaml the chart renders for each example values file and
#   each case with a golden file must pass `sidekick config validate`.
# With --update, it rewrites the golden files.
# Usage: scripts/helm-test.sh <helm> [--update]
set -eu

helm="$1"
update="${2:-}"

chart=charts/sidekick
golden=charts/testdata/quickstart.golden.yaml
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

# The bundled Postgres gets a random password; a fixed one keeps the output
# the same from run to run.
render() {
	values="$1"
	shift
	"$helm" template sidekick "$chart" --namespace sidekick -f "$values" --set postgres.password=golden-not-a-real-password "$@"
}

# check compares the file $2 with the golden file $1, or with --update
# copies it there.
check() {
	if [ "$update" = "--update" ]; then
		cp "$2" "$1"
		echo "Updated $1"
	elif ! diff -u "$1" "$2"; then
		echo "helm template changed $1. If the change is intended, run 'make helm-golden' and commit it." >&2
		exit 1
	fi
}

# only keeps the objects of the template $1 from a whole render, laid out
# as --show-only lays them out. Unlike --show-only, it gives empty output
# when the template renders nothing.
only() {
	awk -v source="# Source: sidekick/$1" '
		function finish() { if (keep) { print ""; print "" } blanks = 0; keep = 0 }
		/^---$/ { finish(); header = 1; next }
		header && $0 == source { print "---"; keep = 1 }
		{ header = 0 }
		keep && $0 == "" { blanks++; next }
		keep { for (; blanks > 0; blanks--) print ""; print }
		END { finish() }
	'
}

render "$chart/quickstart-values.yaml" >"$work/quickstart.yaml"
check "$golden" "$work/quickstart.yaml"

for values in charts/testdata/*/*.values.yaml; do
	[ -f "$values" ] || continue
	dir=$(dirname "$values")
	name=$(basename "$values" .values.yaml)
	template="templates/$(basename "$dir").yaml"

	if [ -f "$dir/$name.error" ]; then
		if render "$chart/quickstart-values.yaml" -f "$values" >/dev/null 2>"$work/error.txt"; then
			echo "$values: helm template succeeded, but $dir/$name.error expects it to fail" >&2
			exit 1
		fi

		if ! grep -qF -f "$dir/$name.error" "$work/error.txt"; then
			echo "$values: helm template failed without the message in $dir/$name.error:" >&2
			cat "$work/error.txt" >&2
			exit 1
		fi

		continue
	fi

	render "$chart/quickstart-values.yaml" -f "$values" >"$work/render.yaml"
	only "$template" <"$work/render.yaml" >"$work/case.yaml"
	check "$dir/$name.golden.yaml" "$work/case.yaml"
done

go build -o "$work/sidekick" ./cmd/sidekick

# validate runs sidekick config validate on the sidekick.yaml the chart
# renders for the values files given.
validate() {
	render "$@" --show-only templates/configmap.yaml |
		sed -n '/sidekick.yaml: |/,$p' | tail -n +2 | sed 's/^    //' >"$work/sidekick.yaml"
	"$work/sidekick" config validate -f "$work/sidekick.yaml"
}

for values in "$chart/quickstart-values.yaml" "$chart"/ci/*.yaml; do
	echo "sidekick config validate: sidekick.yaml from $values"
	validate "$values"
done

for values in charts/testdata/*/*.values.yaml; do
	[ -f "$values" ] || continue
	if [ -f "$(dirname "$values")/$(basename "$values" .values.yaml).error" ]; then
		continue
	fi

	echo "sidekick config validate: sidekick.yaml from the quick-start values and $values"
	validate "$chart/quickstart-values.yaml" -f "$values"
done

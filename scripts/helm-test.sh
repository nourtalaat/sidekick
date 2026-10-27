#!/bin/sh
# Tests the Helm chart without a cluster:
# - `helm template` of the quick-start values must match the golden file
#   charts/testdata/quickstart.golden.yaml (with --update, it rewrites it);
# - each charts/testdata/<template>/<case>.values.yaml, layered on the
#   quick-start values, must render templates/<template>.yaml as
#   <case>.golden.yaml, or fail with the message in <case>.error;
# - the sidekick.yaml the chart renders for each example values file must
#   pass `sidekick config validate`.
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

render "$chart/quickstart-values.yaml" >"$work/quickstart.yaml"

if [ "$update" = "--update" ]; then
	mkdir -p "$(dirname "$golden")"
	cp "$work/quickstart.yaml" "$golden"
	echo "Updated $golden"
elif ! diff -u "$golden" "$work/quickstart.yaml"; then
	echo "helm template of the quick-start values changed. If the change is intended, run 'make helm-golden' and commit $golden." >&2
	exit 1
fi

for values in charts/testdata/*/*.values.yaml; do
	[ -f "$values" ] || continue
	dir=$(dirname "$values")
	name=$(basename "$values" .values.yaml)
	template="templates/$(basename "$dir").yaml"

	if [ -f "$dir/$name.error" ]; then
		if render "$chart/quickstart-values.yaml" -f "$values" --show-only "$template" >/dev/null 2>"$work/error.txt"; then
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

	render "$chart/quickstart-values.yaml" -f "$values" --show-only "$template" >"$work/case.yaml"
	if [ "$update" = "--update" ]; then
		cp "$work/case.yaml" "$dir/$name.golden.yaml"
		echo "Updated $dir/$name.golden.yaml"
	elif ! diff -u "$dir/$name.golden.yaml" "$work/case.yaml"; then
		echo "$template with $values changed. If the change is intended, run 'make helm-golden' and commit $dir/$name.golden.yaml." >&2
		exit 1
	fi
done

go build -o "$work/sidekick" ./cmd/sidekick

for values in "$chart/quickstart-values.yaml" "$chart"/ci/*.yaml; do
	"$helm" template sidekick "$chart" --namespace sidekick -f "$values" \
		--set postgres.password=golden-not-a-real-password --show-only templates/configmap.yaml |
		sed -n '/sidekick.yaml: |/,$p' | tail -n +2 | sed 's/^    //' >"$work/sidekick.yaml"

	echo "sidekick config validate: sidekick.yaml from $values"
	"$work/sidekick" config validate -f "$work/sidekick.yaml"
done

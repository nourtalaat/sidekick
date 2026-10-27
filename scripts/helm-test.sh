#!/bin/sh
# Tests the Helm chart without a cluster:
# - `helm template` of the quick-start values must match the golden file
#   charts/testdata/quickstart.golden.yaml (with --update, it rewrites it);
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
	"$helm" template sidekick "$chart" --namespace sidekick -f "$1" --set postgres.password=golden-not-a-real-password
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

go build -o "$work/sidekick" ./cmd/sidekick

for values in "$chart/quickstart-values.yaml" "$chart"/ci/*.yaml; do
	"$helm" template sidekick "$chart" --namespace sidekick -f "$values" \
		--set postgres.password=golden-not-a-real-password --show-only templates/configmap.yaml |
		sed -n '/sidekick.yaml: |/,$p' | tail -n +2 | sed 's/^    //' >"$work/sidekick.yaml"

	echo "sidekick config validate: sidekick.yaml from $values"
	"$work/sidekick" config validate -f "$work/sidekick.yaml"
done

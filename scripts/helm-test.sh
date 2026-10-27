#!/bin/sh
# Tests the Helm chart without a cluster:
# - `helm template` of the quick-start values must match the golden file
#   charts/testdata/quickstart.golden.yaml;
# - for each charts/testdata/rbac/<case>.values.yaml, laid over the
#   quick-start values, the Kubernetes source's RBAC objects and whether
#   the pods mount a service account token must match
#   charts/testdata/rbac/<case>.golden.yaml;
# - the sidekick.yaml the chart renders for each example values file and
#   each RBAC case must pass `sidekick config validate`.
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
	"$helm" template sidekick "$chart" --namespace sidekick "$@" --set postgres.password=golden-not-a-real-password
}

# check compares the file $2 with the golden file $1, or with --update
# copies it there.
check() {
	if [ "$update" = "--update" ]; then
		mkdir -p "$(dirname "$1")"
		cp "$2" "$1"
		echo "Updated $1"
	elif ! diff -u "$1" "$2"; then
		echo "helm template changed $1. If the change is intended, run 'make helm-golden' and commit it." >&2
		exit 1
	fi
}

# rbac prints, for the values files given, whether the Sidekick pods mount
# a service account token, then every object from templates/rbac.yaml.
rbac() {
	token=$(render "$@" --show-only templates/deployment.yaml | sed -n 's/^ *automountServiceAccountToken: //p')
	echo "# The Sidekick pods: automountServiceAccountToken: $token"
	render "$@" | awk '
		/^---$/ { separator = $0; keep = 0; header = 1; next }
		header { header = 0; if ($0 == "# Source: sidekick/templates/rbac.yaml") { keep = 1; print separator } }
		keep { print }
	'
}

render -f "$chart/quickstart-values.yaml" >"$work/quickstart.yaml"
check "$golden" "$work/quickstart.yaml"

for values in charts/testdata/rbac/*.values.yaml; do
	name=$(basename "$values" .values.yaml)
	rbac -f "$chart/quickstart-values.yaml" -f "$values" >"$work/rbac-$name.yaml"
	check "charts/testdata/rbac/$name.golden.yaml" "$work/rbac-$name.yaml"
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
	validate -f "$values"
done

for values in charts/testdata/rbac/*.values.yaml; do
	echo "sidekick config validate: sidekick.yaml from the quick-start values and $values"
	validate -f "$chart/quickstart-values.yaml" -f "$values"
done

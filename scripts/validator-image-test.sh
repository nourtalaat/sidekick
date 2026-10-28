#!/bin/sh
# Tests a built validator image with no network, as user 65532 with a
# read-only root filesystem, as the chart runs it:
# - `job` checks one good and one bad fixture of each format, and a lock
#   file that asks for a provider version the image does not have;
# - `serve` starts and `health` answers inside the container;
# - the NOTICE file is in the image.
# Usage: scripts/validator-image-test.sh <docker> <image>
set -eu

docker="$1"
image="$2"
fixtures=internal/validate/fixture

work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
chmod 755 "$work"

run() {
	"$docker" run --rm --network none --read-only --tmpfs /tmp:rw,exec,size=1g \
		-v "$work:/work:ro" "$image" "$@"
}

# request writes a request for the files under a fixture directory.
request() {
	format=$1
	dir=$2
	out=$3

	(cd "$dir" && find . -type f | sed 's|^\./||' | LC_ALL=C sort) | while read -r path; do
		jq -n --arg path "$path" --rawfile content "$dir/$path" '{path: $path, content: $content}'
	done | jq -s --arg format "$format" '{format: $format, files: .}' >"$out"
	chmod 644 "$out"
}

# check runs one request in job mode and compares .ok, and the first error
# when a substring is given.
check() {
	name=$1
	format=$2
	dir=$3
	want_ok=$4
	want_error=${5:-}

	request "$format" "$dir" "$work/$name.json"
	run job --request "/work/$name.json" --termination-log "" >"$work/$name.out"

	ok=$(jq -r .ok "$work/$name.out")
	if [ "$ok" != "$want_ok" ]; then
		echo "$name: ok is $ok, want $want_ok:" >&2
		cat "$work/$name.out" >&2
		exit 1
	fi

	if [ -n "$want_error" ] && ! jq -r '.errors[0].message' "$work/$name.out" | grep -qF "$want_error"; then
		echo "$name: the first error does not contain \"$want_error\":" >&2
		cat "$work/$name.out" >&2
		exit 1
	fi

	echo "ok: $name"
}

mkdir "$work/rules-good" "$work/rules-bad" "$work/grafana-good" "$work/grafana-bad"
cp "$fixtures/prometheus_rules/good/checkout.yaml" "$work/rules-good/"
cp "$fixtures/prometheus_rules/bad/bad-expression.yaml" "$work/rules-bad/"
cp "$fixtures/grafana_alert_file/good/payments.yaml" "$work/grafana-good/"
cp "$fixtures/grafana_alert_file/bad/bad-promql.yaml" "$work/grafana-bad/"

check rules-good prometheus_rules "$work/rules-good" true
check rules-bad prometheus_rules "$work/rules-bad" false "could not parse expression"
check grafana-good grafana_alert_file "$work/grafana-good" true
check grafana-bad grafana_alert_file "$work/grafana-bad" false "PromQL"
check datadog-good terraform "$fixtures/terraform/good/datadog" true
check grafana-provider-good terraform "$fixtures/terraform/good/grafana" true
check signoz-good terraform "$fixtures/terraform/good/signoz" true
check datadog-bad terraform "$fixtures/terraform/bad/unsupported-argument" false "Unsupported argument"
check locked-version terraform "$fixtures/terraform/bad/locked-version-not-in-mirror" false "provider version not in the validator image"

container=$("$docker" run -d --network none --read-only --tmpfs /tmp:rw,exec,size=1g "$image" serve)
trap '"$docker" rm -f "$container" >/dev/null; rm -r "$work"' EXIT

for _ in 1 2 3 4 5 6 7 8 9 10; do
	if "$docker" exec "$container" sidekick-validate health; then
		echo "ok: serve and health"
		break
	fi

	sleep 1
done

"$docker" exec "$container" sidekick-validate health

"$docker" run --rm --entrypoint sidekick-validate "$image" version
"$docker" create --name "validator-notice-$$" "$image" >/dev/null
"$docker" cp "validator-notice-$$:/usr/share/doc/sidekick-validate/NOTICE" "$work/NOTICE"
"$docker" rm "validator-notice-$$" >/dev/null
grep -q "TOOLS IN THE VALIDATOR IMAGE" "$work/NOTICE"
echo "ok: NOTICE"

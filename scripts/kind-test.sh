#!/bin/sh
# Installs the chart with the quick-start values and the bundled Postgres in
# the current kind cluster, using a local image, and waits until Sidekick is
# ready. Then it checks the pod's user and root filesystem, that the schema
# is migrated, and that an upgrade keeps the database password. The Secrets
# hold fake values, so nothing calls Slack or a model provider.
# Usage: scripts/kind-test.sh <helm> <image repository> <image tag>
set -eu

helm="$1"
repository="$2"
tag="$3"

namespace=sidekick
release=sidekick
timeout=5m

kubectl create namespace "$namespace"
kubectl -n "$namespace" create secret generic sidekick-anthropic --from-literal=apiKey=ci-not-a-real-key
kubectl -n "$namespace" create secret generic sidekick-slack --from-literal=botToken=xoxb-ci-not-a-real-token
kubectl -n "$namespace" create secret generic sidekick-webhook --from-literal=token=ci-not-a-real-token

install() {
	"$helm" "$@" "$release" charts/sidekick --namespace "$namespace" \
		-f charts/sidekick/quickstart-values.yaml \
		--set image.repository="$repository" --set image.tag="$tag" --set image.pullPolicy=Never \
		--wait --timeout "$timeout"
}

echo "--- helm install"
install install
kubectl -n "$namespace" rollout status deploy/"$release" --timeout "$timeout"

echo "--- the pod runs as 65532 with a read-only root filesystem"
pod=$(kubectl -n "$namespace" get pod -l app.kubernetes.io/component=server -o jsonpath='{.items[0].metadata.name}')
user=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.spec.securityContext.runAsUser}')
readonly=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.spec.containers[?(@.name=="sidekick")].securityContext.readOnlyRootFilesystem}')
echo "runAsUser=$user readOnlyRootFilesystem=$readonly"
[ "$user" = 65532 ] && [ "$readonly" = true ]

echo "--- the schema is migrated"
kubectl -n "$namespace" exec "$pod" -c sidekick -- sidekick migrate status

echo "--- an upgrade with a config change rolls the pod and keeps the database password"
install upgrade --reuse-values --set config.outputs.slack.channel='#sidekick-ci'
kubectl -n "$namespace" rollout status deploy/"$release" --timeout "$timeout"
kubectl -n "$namespace" exec deploy/"$release" -c sidekick -- sidekick migrate status

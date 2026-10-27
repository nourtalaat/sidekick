#!/bin/sh
# Installs the tools of the validator image for one Linux platform, at the
# versions pinned below, each checked against its SHA-256 checksum from the
# project's release page:
#   <dest>/promtool
#   <dest>/tofu
#   <dest>/providers/registry.opentofu.org/<namespace>/<type>/terraform-provider-<type>_<version>_linux_<arch>.zip
#   <dest>/providers/registry.terraform.io -> registry.opentofu.org
#   <dest>/licenses/<tool>/...   each tool's own license and notice files
#   <dest>/tools.txt             each tool's name, version, source and
#                                binary (or provider package), one per line
# The provider mirror uses OpenTofu's packed layout, so `tofu init` checks
# each package against the hashes in a module's lock file. The
# registry.terraform.io link serves the same packages to lock files and
# sources that name Terraform's registry.
#
# The Dockerfile of the validator image and `make validator-tools` (which CI
# uses for the validator tests) both run this script. To update a tool,
# change its version and both checksums together, from the release's
# checksum file.
#
# Usage: scripts/validator-tools.sh <dest> <arch>   (arch: amd64 or arm64)
set -eu

PROMETHEUS_VERSION=3.15.0
PROMETHEUS_SHA256_amd64=2a542df32eac02ee17b9d844fb2aa1de00dafa5476579ba8a3ba862e9d572ea0
PROMETHEUS_SHA256_arm64=f1f90ec08e849d494ca66c611470afc50192f0355f1a61c33f2cbde02d067823

OPENTOFU_VERSION=1.13.1
OPENTOFU_SHA256_amd64=378ada19d4bc70c43732004e8159be771b23b9a5afdf059e5f8a2b3fa2c70a69
OPENTOFU_SHA256_arm64=9c1ef375aa1852db0b2888aa921b640c71f8140d4682aa4fec99378a64fa7dc3

DATADOG_VERSION=4.25.0
DATADOG_SHA256_amd64=614cc4460ca4b500548ff9f68d5b1ea54d117086bac0e42c90bf1b6948f0eabc
DATADOG_SHA256_arm64=66bd2befdc8177904d9524c274706911a1599cbbed7fb1d3eee060451e7d2b0c

SIGNOZ_VERSION=0.1.5
SIGNOZ_SHA256_amd64=e0d4bb3065f85fad4c501b62eaff06ff937ae8fc9e6437ec5c526dc0a4c91525
SIGNOZ_SHA256_arm64=cb838ce3bb96419eceab1f00211a13a128cfc4725e5ee07107b24dbaa355a609

GRAFANA_VERSION=4.49.0
GRAFANA_SHA256_amd64=1c027958e25a6d1d1692157373646c5ae3ab1fd1dccc7abccd5b9365eacc5a43
GRAFANA_SHA256_arm64=f62e2e8d58d7772e84f4a2469ccc6cef72700bc703e6f11b73d5b214bbac8bf2

dest="$1"
arch="$2"

case "$arch" in
amd64 | arm64) ;;
*)
	echo "validator-tools: arch must be amd64 or arm64, not $arch" >&2
	exit 1
	;;
esac

# sha256 prints the SHA-256 checksum of standard input.
sha256() {
	if command -v sha256sum >/dev/null; then
		sha256sum | cut -d' ' -f1
	else
		shasum -a 256 | cut -d' ' -f1
	fi
}

# A stamp of this script and the arch: an install that matches it is kept.
stamp="$(sha256 <"$0") $arch"
if [ -f "$dest/.stamp" ] && [ "$(cat "$dest/.stamp")" = "$stamp" ]; then
	exit 0
fi

work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

# fetch downloads a URL into the work directory and checks its checksum.
fetch() {
	url=$1
	sum=$2
	file="$work/$(basename "$url")"

	curl -fsSL --retry 3 -o "$file" "$url"

	got=$(sha256 <"$file")
	if [ "$got" != "$sum" ]; then
		echo "validator-tools: $(basename "$url") has checksum $got, want $sum" >&2
		exit 1
	fi
}

# license copies the license and notice files a tool ships with.
license() {
	tool=$1
	shift

	mkdir -p "$dest/licenses/$tool"
	cp "$@" "$dest/licenses/$tool/"
}

if [ -e "$dest" ]; then
	rm -r "$dest"
fi

mkdir -p "$dest/providers/registry.opentofu.org"

eval "prometheus_sum=\$PROMETHEUS_SHA256_$arch"
prometheus="prometheus-$PROMETHEUS_VERSION.linux-$arch"
fetch "https://github.com/prometheus/prometheus/releases/download/v$PROMETHEUS_VERSION/$prometheus.tar.gz" "$prometheus_sum"
tar -xzf "$work/$prometheus.tar.gz" -C "$work" "$prometheus/promtool" "$prometheus/LICENSE" "$prometheus/NOTICE"
cp "$work/$prometheus/promtool" "$dest/promtool"
license promtool "$work/$prometheus/LICENSE" "$work/$prometheus/NOTICE"

eval "opentofu_sum=\$OPENTOFU_SHA256_$arch"
opentofu="tofu_${OPENTOFU_VERSION}_linux_$arch"
fetch "https://github.com/opentofu/opentofu/releases/download/v$OPENTOFU_VERSION/$opentofu.tar.gz" "$opentofu_sum"
mkdir "$work/tofu"
tar -xzf "$work/$opentofu.tar.gz" -C "$work/tofu" tofu LICENSE
cp "$work/tofu/tofu" "$dest/tofu"
license tofu "$work/tofu/LICENSE"

{
	echo "promtool $PROMETHEUS_VERSION https://github.com/prometheus/prometheus/tree/v$PROMETHEUS_VERSION promtool"
	echo "tofu $OPENTOFU_VERSION https://github.com/opentofu/opentofu/tree/v$OPENTOFU_VERSION tofu"
} >"$dest/tools.txt"

# provider installs one provider package into the mirror: its namespace,
# type, GitHub repository and version, and the checksum for this arch.
provider() {
	namespace=$1
	type=$2
	repo=$3
	version=$4
	sum=$5
	zip="terraform-provider-${type}_${version}_linux_$arch.zip"

	fetch "https://github.com/$repo/releases/download/v$version/$zip" "$sum"
	package="providers/registry.opentofu.org/$namespace/$type/$zip"
	mkdir -p "$(dirname "$dest/$package")"
	cp "$work/$zip" "$dest/$package"

	mkdir "$work/$type"
	unzip -q -d "$work/$type" "$work/$zip"
	license "terraform-provider-$type" $(find "$work/$type" -maxdepth 1 -type f \( -iname 'licen[cs]e*' -o -iname 'notice*' \))
	echo "terraform-provider-$type $version https://github.com/$repo/tree/v$version $package" >>"$dest/tools.txt"
}

eval "datadog_sum=\$DATADOG_SHA256_$arch"
eval "signoz_sum=\$SIGNOZ_SHA256_$arch"
eval "grafana_sum=\$GRAFANA_SHA256_$arch"
provider datadog datadog DataDog/terraform-provider-datadog "$DATADOG_VERSION" "$datadog_sum"
provider signoz signoz SigNoz/terraform-provider-signoz "$SIGNOZ_VERSION" "$signoz_sum"
provider grafana grafana grafana/terraform-provider-grafana "$GRAFANA_VERSION" "$grafana_sum"

ln -s registry.opentofu.org "$dest/providers/registry.terraform.io"

echo "$stamp" >"$dest/.stamp"

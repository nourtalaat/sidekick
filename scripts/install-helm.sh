#!/bin/sh
# Downloads one Helm release, checks it against a pinned SHA-256 and puts
# the helm binary in a directory.
# Usage: scripts/install-helm.sh <version> <sha256> <directory>
set -eu

version="$1"
sum="$2"
dir="$3"

if [ -z "$sum" ]; then
	echo "no pinned Helm checksum for $(uname -s)/$(uname -m); add one next to HELM_VERSION in the Makefile" >&2
	exit 1
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
x86_64) arch=amd64 ;;
aarch64) arch=arm64 ;;
esac

archive="helm-$version-$os-$arch.tar.gz"
mkdir -p "$dir"
curl -fsSL -o "$dir/$archive" "https://get.helm.sh/$archive"

if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$dir/$archive" | cut -d ' ' -f 1)
else
	got=$(shasum -a 256 "$dir/$archive" | cut -d ' ' -f 1)
fi

if [ "$got" != "$sum" ]; then
	echo "$archive has SHA-256 $got, want $sum" >&2
	exit 1
fi

tar -xzf "$dir/$archive" -C "$dir" --strip-components 1 "$os-$arch/helm"

#!/bin/sh
# Installs the latest Doubletake desktop app release into ~/.local (no root):
#
#   curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake-ui/main/scripts/install.sh | sh
#
# Set PREFIX to install elsewhere (e.g. PREFIX=/usr/local, run as root).
set -eu

REPO=ValentinHerrmann/doubletake-ui
BASE=https://github.com/$REPO/releases/latest/download

case $(uname -m) in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*)
	echo "error: unsupported architecture $(uname -m)" >&2
	exit 1
	;;
esac
BUNDLE=doubletake-ui-linux-$ARCH

for tool in curl tar sha256sum; do
	command -v "$tool" >/dev/null || {
		echo "error: $tool is required" >&2
		exit 1
	}
done

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $BUNDLE.tar.gz…"
curl -fsSL -o "$TMP/$BUNDLE.tar.gz" "$BASE/$BUNDLE.tar.gz"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
(cd "$TMP" && grep " $BUNDLE.tar.gz\$" SHA256SUMS | sha256sum -c --quiet -) || {
	echo "error: checksum verification failed" >&2
	exit 1
}

tar -xzf "$TMP/$BUNDLE.tar.gz" -C "$TMP"
"$TMP/$BUNDLE/install.sh"

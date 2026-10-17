#!/usr/bin/env bash
# Installs the pinned oasdiff into .tools/bin (or the directory given as $1),
# checking the release archive's SHA-256. Used by CI, the release workflow and
# local development. To upgrade, change the version and all three checksums
# (from the release's checksums.txt).
set -euo pipefail

version=1.33.0
sha_linux_amd64=43a4e328e2d13ba1552d760aa68d2485c75c5621f309f6ff64ae895188345247
sha_linux_arm64=4ae3c362d6074d919aada2dea82d0ee84366591600384455d0bdc658ddf8f7ae
sha_darwin_all=2a479337c15afdcbf0b1e89c0b4d0cf0176472dbc11483358fed1f831d1e46c5

dest=${1:-"$(cd "$(dirname "$0")/.." && pwd)/.tools/bin"}

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) asset=linux_amd64 sha=$sha_linux_amd64 ;;
  Linux/aarch64 | Linux/arm64) asset=linux_arm64 sha=$sha_linux_arm64 ;;
  Darwin/*) asset=darwin_all sha=$sha_darwin_all ;;
  *) echo "install-oasdiff: no pinned build for $(uname -s)/$(uname -m)" >&2 && exit 1 ;;
esac

if [ -x "$dest/oasdiff" ] && [ "$("$dest/oasdiff" --version)" = "oasdiff version $version" ]; then
  echo "oasdiff $version already in $dest"
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="$tmp/oasdiff.tar.gz"
curl -fsSL --retry 3 -o "$archive" \
  "https://github.com/oasdiff/oasdiff/releases/download/v${version}/oasdiff_${version}_${asset}.tar.gz"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$archive" | cut -d' ' -f1)
fi
if [ "$actual" != "$sha" ]; then
  echo "install-oasdiff: checksum mismatch for $asset: got $actual, want $sha" >&2
  exit 1
fi

tar -xzf "$archive" -C "$tmp" oasdiff
mkdir -p "$dest"
install -m 0755 "$tmp/oasdiff" "$dest/oasdiff"
"$dest/oasdiff" --version

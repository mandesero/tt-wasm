#!/bin/sh
set -eu

if [ "$#" != 1 ]; then
    echo "Usage: $0 INSTALL_DIR" >&2
    exit 2
fi
case "$(uname -m)" in
    x86_64) sha=a18020674fac4107f329bc9ed720ea9c88e4b30a61e8a527787bcce051123d32 ;;
    aarch64) sha=a263f34aefad1b24ba4d9ca635ddfea415f4acfe3f3f1bb6d4525c34fc6a7397 ;;
    *) echo "Unsupported Wasmtime architecture: $(uname -m)" >&2; exit 1 ;;
esac
asset=wasmtime-v51.0.0-$(uname -m)-linux
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT
curl -fsSL --retry 4 --retry-all-errors \
    "https://github.com/bytecodealliance/wasmtime/releases/download/v51.0.0/$asset.tar.xz" \
    -o "$archive"
printf '%s  %s\n' "$sha" "$archive" | sha256sum --check
mkdir -p "$1"
tar -xJf "$archive" --strip-components=1 -C "$1" "$asset/wasmtime"

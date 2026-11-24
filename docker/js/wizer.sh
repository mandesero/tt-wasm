#!/bin/sh
set -eu

# ComponentizeJS starts Wizer with a guest-specific environment. Supply a
# writable cache even when the host UID has no writable passwd home.
export HOME="${HOME:-/tmp}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-/tmp/.cache}"
exec "$0.bin" "$@"

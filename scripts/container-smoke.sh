#!/bin/sh
set -eu

if [ "$#" != 2 ]; then
    echo "Usage: $0 TARGET IMAGE" >&2
    exit 2
fi
target=$1
image=$2
case "$target" in
    c|js|go|rust|python|csharp) ;;
    *) echo "Unknown builder target: $target" >&2; exit 2 ;;
esac

repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d)
# Go's module downloader makes cached sources read-only.
cleanup() {
    chmod -R u+w "$work"
    rm -rf "$work"
}
trap cleanup EXIT
mkdir -p "$work/cache/sdk"

sdk_sha=7fed52ae5edb6309e5a45e2eacc185d9437752f423076f482f307cb6f779850e
sdk_archive=${TT_WASM_CONTAINER_SDK_ARCHIVE:-$work/sdk.tar.gz}
if [ -z "${TT_WASM_CONTAINER_SDK_ARCHIVE:-}" ]; then
    curl -fsSL --retry 4 \
        https://github.com/tarantool/tarantool-wasm-rs/releases/download/v0.1.0/tarantool-wasm-0.1.0-sdk.tar.gz \
        -o "$sdk_archive"
fi
printf '%s  %s\n' "$sdk_sha" "$sdk_archive" | sha256sum --check
tar -xzf "$sdk_archive" --strip-components=1 -C "$work/cache/sdk"

run() {
    docker run --rm --pull=never --read-only --cap-drop=ALL \
        --security-opt=no-new-privileges --tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777 \
        --user "$(id -u):$(id -g)" "$@"
}

run --network none "$image" check "$target"
for phase in fetch build; do
    network=bridge
    set --
    if [ "$phase" = build ]; then
        network=none
        set -- --mount "type=bind,src=$work/cache,dst=/work/cache,readonly"
        case "$target" in
            c) projects="c/project cpp/project" ;;
            go) projects=go/project ;;
            *) projects=project ;;
        esac
        for project in $projects; do
            set -- "$@" --mount "type=bind,src=$work/$project,dst=/work/$project,readonly"
        done
    fi
    # shellcheck disable=SC2016
    run --network "$network" --entrypoint /bin/sh \
        --mount "type=bind,src=$repo,dst=/src,readonly" \
        --mount "type=bind,src=$work,dst=/work" "$@" \
        "$image" -ec '
            /bin/sh "/src/tests/container/$1.sh" "$2"
            if [ "${3:-}" = 1 ] && [ -r /sys/fs/cgroup/memory.peak ]; then
                printf "phase=%s memory_peak_bytes=%s " "$2" "$(cat /sys/fs/cgroup/memory.peak)"
                awk '"'"'$1 == "usage_usec" { print "cpu_usage_usec=" $2 }'"'"' /sys/fs/cgroup/cpu.stat
            fi
        ' sh "$target" "$phase" "${TT_WASM_CONTAINER_METRICS:-}"
done

if [ -n "${WASMTIME:-}" ]; then
    case "$target" in
        c) set -- "$work/c/build/component.wasm" "$work/cpp/build/component.wasm" ;;
        go) set -- "$work/go/build/component.wasm" ;;
        *) set -- "$work/build/component.wasm" ;;
    esac
    for component do
        result=$("$WASMTIME" run --invoke 'add(20, 22)' "$component")
        echo "Executing ${component#"$work"/}: $result"
        test "$result" = 42
    done
fi

#!/bin/sh
set -eu

project=/work/project
export CARGO_HOME=/work/cache/dependencies/cargo

run_builder() {
    tt-wasm-builder <<EOF
{"language":"rust","operation":"$1","dir":"$project","cache":"/work/cache","work":"/work/$1","source":"src/lib.rs","wit":"wit","world":"calculator"}
EOF
}

case "${1:-}" in
    fetch)
        mkdir -p "$project/src" "$project/wit"
        sed 's/{{.World}}/calculator/g' /src/templates/rust/default/Cargo.toml > "$project/Cargo.toml"
        sed -e 's/{{.Package}}/example:calculator/g' -e 's/{{.World}}/calculator/g' \
            /src/templates/rust/default/world.wit > "$project/wit/world.wit"
        cp /src/templates/rust/default/Cargo.lock "$project/Cargo.lock"
        cp /src/templates/rust/default/src/lib.rs "$project/src/lib.rs"
        cat >> "$project/src/lib.rs" <<'EOF'

#[cfg(test)]
mod tests {
    use super::{bindings::Guest, Component};

    #[test]
    fn adds_two_numbers() {
        assert_eq!(Component::add(19, 23), 42);
    }
}
EOF
        run_builder fetch
        # Fetch host dependencies as well as the WASI dependencies staged by tt.
        cargo fetch --locked --manifest-path /work/fetch/rust/Cargo.toml
        ;;
    build)
        run_builder bind
        grep -q 'trait Guest' /work/bind/bindings/bindings.rs
        run_builder build
        wasm-tools validate --features component-model /work/build/component.wasm
        wasm-tools component wit /work/build/component.wasm | grep -q 'export add: func'
        (
            cd /work/build/rust
            mkdir -p .tt-wasm/bindings
            cp /work/bind/bindings/bindings.rs .tt-wasm/bindings/bindings.rs
            export CARGO_HOME=/work/build/cargo
            rustfmt --edition 2024 .tt-wasm/bindings/bindings.rs src/bindings.rs
            cargo fmt --check
            cargo test --locked --offline
            cargo clippy --locked --offline --all-targets -- -D warnings
        )
        ;;
    *)
        printf 'Usage: %s fetch|build\n' "$0" >&2
        exit 2
        ;;
esac

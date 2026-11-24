#!/bin/sh
set -eu

project=/work/project

run_builder() {
    tt-wasm-builder <<EOF
{"language":"js","operation":"$1","dir":"$project","cache":"/work/cache","work":"/work/$1","source":"main.js","wit":"world.wit","world":"calculator"}
EOF
}

case "${1:-}" in
    fetch)
        mkdir -p "$project"
        cat > "$project/package.json" <<'EOF'
{
  "name": "tt-wasm-js-container-test",
  "version": "1.0.0",
  "private": true,
  "type": "module",
  "dependencies": {
    "lodash-es": "4.17.23"
  }
}
EOF
        cat > "$project/package-lock.json" <<'EOF'
{
  "name": "tt-wasm-js-container-test",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "tt-wasm-js-container-test",
      "version": "1.0.0",
      "dependencies": {
        "lodash-es": "4.17.23"
      }
    },
    "node_modules/lodash-es": {
      "version": "4.17.23",
      "resolved": "https://registry.npmjs.org/lodash-es/-/lodash-es-4.17.23.tgz",
      "integrity": "sha512-kVI48u3PZr38HdYz98UmfPnXl2DXrpdctLrFLCd3kOx1xUkOmpFPx7gCWWM5MPkL/fD8zb+Ph0QzjGFs4+hHWg=="
    }
  }
}
EOF
        cat > "$project/main.js" <<'EOF'
import clamp from "lodash-es/clamp.js";

export function add(a, b) {
  return clamp(a + b, -2147483648, 2147483647);
}
EOF
        cat > "$project/main.test.js" <<'EOF'
import assert from "node:assert/strict";
import { test } from "node:test";
import { add } from "./main.js";

test("adds two numbers", () => {
  assert.equal(add(19, 23), 42);
});
EOF
        cat > "$project/world.wit" <<'EOF'
package example:calculator;

world calculator {
    export add: func(a: s32, b: s32) -> s32;
}
EOF
        run_builder fetch
        test -d /work/cache/dependencies/js/node_modules/twr-js-sdk
        ;;
    build)
        run_builder bind
        grep -R -q 'add' /work/bind/bindings
        run_builder build
        wasm-tools validate --features component-model /work/build/component.wasm
        wasm-tools component wit /work/build/component.wasm | grep -q 'export add: func'
        (
            cd /work/build/project
            node --check main.js
            node --test main.test.js
            prettier --check main.js main.test.js
        )
        ;;
    *)
        printf 'Usage: %s fetch|build\n' "$0" >&2
        exit 2
        ;;
esac

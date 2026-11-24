#!/bin/sh
set -eu

root=/work/go
project=$root/project
export GOENV=off GOTOOLCHAIN=local GOTELEMETRY=off GOWORK=off
export GOCACHE=$root/go-cache GOMODCACHE=/work/cache/dependencies/go/modules

run_builder() {
    tt-wasm-builder <<EOF
{"language":"go","operation":"$1","dir":"$project","work":"$root/$1","cache":"/work/cache","source":".","wit":"world.wit","world":"smoke"}
EOF
}

case "${1:-}" in
    fetch)
        mkdir -p "$project/arithmetic"
        cp /src/templates/go/default/go.mod.tmpl "$project/go.mod"
        cp /src/templates/go/default/go.sum "$project/go.sum"
        sed -e 's/{{.Package}}/example:smoke/g' -e 's/{{.World}}/smoke/g' \
            -e '/include tarantool:tarantool/d' /src/templates/go/default/world.wit > "$project/world.wit"
        cat > "$project/main.go" <<'EOF'
package main

import (
	world "example.org/ttwasm/go-component/.tt-wasm/bindings/example/smoke/smoke"
	"example.org/ttwasm/go-component/arithmetic"
)

func init() { world.Exports.Add = arithmetic.Add }

func main() {}
EOF
        cat > "$project/arithmetic/add.go" <<'EOF'
package arithmetic

func Add(a, b int32) int32 { return a+b }
EOF
        cat > "$project/arithmetic/add_test.go" <<'EOF'
package arithmetic

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(20, 22); got != 42 {
		t.Fatalf("Add(20, 22) = %d, want 42", got)
	}
}
EOF
        gofmt -w "$project/main.go" "$project/arithmetic"
        run_builder fetch
        ;;
    build)
        export GOPROXY=off GOSUMDB=off
        tt-wasm-builder check go
        run_builder bind
        test -s "$root/bind/bindings/example/smoke/smoke/smoke.exports.go"
        run_builder build
        cd "$root/build/project"
        test -z "$(gofmt -l arithmetic main.go)"
        go vet ./arithmetic
        go test ./arithmetic
        go test -c -o "$root/arithmetic.test" ./arithmetic
        go tool objdump -s TestAdd "$root/arithmetic.test" | grep -F TestAdd
        cat > "$root/cgo.go" <<'EOF'
package main

// #include <stdint.h>
import "C"

func main() {
    if C.int32_t(20) + C.int32_t(22) != 42 {
        panic("cgo result must equal 42")
    }
}
EOF
        CGO_ENABLED=1 go run "$root/cgo.go"
        wasm-tools validate "$root/build/component.wasm"
        wasm-tools component wit "$root/build/component.wasm" | grep -F 'export add: func('
        ;;
    *) printf 'usage: %s fetch|build\n' "$0" >&2; exit 2 ;;
esac

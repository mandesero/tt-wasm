#!/bin/sh
set -eu

run_builder() {
    tt-wasm-builder <<EOF
{"language":"$language","operation":"$1","dir":"$project","work":"$root/$1","cache":"$root/cache","source":"main.$extension","wit":"wit","world":"smoke"}
EOF
}

for language in c cpp; do
    root=/work/$language
    project=$root/project
    case "$language" in
        c) extension=c; compiler=wasm32-wasip2-clang ;;
        cpp) extension=cpp; compiler=wasm32-wasip2-clang++ ;;
    esac

    case "${1:-}" in
        fetch)
            mkdir -p "$project/wit/deps/operands" "$root/cache"
            ln -s /work/cache/sdk "$root/cache/sdk"
            cat > "$project/wit/world.wit" <<'EOF'
package example:smoke;

world smoke {
    use example:operands/types.{operand};
    export add: func(a: operand, b: operand) -> operand;
}
EOF
            cat > "$project/wit/deps/operands/types.wit" <<'EOF'
package example:operands;

interface types {
    type operand = s32;
}
EOF
            if [ "$language" = c ]; then
                sed 's/{{.WorldSnake}}/smoke/g' /src/templates/c/default/main.c > "$project/main.c"
            else
                cat > "$project/main.cpp" <<'EOF'
#include "smoke.h"
#include <array>

int32_t exports_smoke_add(int32_t a, int32_t b) {
    const std::array<int32_t, 2> operands{a, b};
    return operands.front() + operands.back();
}
EOF
            fi
            clang-format -i "$project/main.$extension"
            run_builder fetch
            ;;
        build)
            tt-wasm-builder check "$language"
            run_builder bind
            test -s "$root/bind/bindings/smoke.h"
            clang-format --dry-run --Werror "$project/main.$extension"
            cat > "$root/Makefile" <<'EOF'
check:
	$(CC) --analyze $(FLAGS) $(SOURCE) -o analysis.plist
	$(CC) -Werror -Wall -Wextra -g $(FLAGS) -c $(SOURCE) -o debug.o
	llvm-ar rcs libsmoke.a debug.o
	llvm-objdump -h debug.o | grep -F .debug_info
EOF
            make -C "$root" check CC="$compiler" FLAGS="-I$root/bind/bindings" \
                SOURCE="$project/main.$extension"
            run_builder build
            wasm-tools validate "$root/build/component.wasm"
            wasm-tools component wit "$root/build/component.wasm" | grep -F 'export add: func('
            ;;
        *) printf 'usage: %s fetch|build\n' "$0" >&2; exit 2 ;;
    esac
done

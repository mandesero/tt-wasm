#!/bin/sh
set -eu

run_builder() {
    build_arguments=
    if [ "$1" = build ]; then
        build_arguments=',"args":["-Ivendor/include","-Lvendor/lib","-lnative-math"]'
    fi
    tt-wasm-builder <<EOF
{"language":"$language","operation":"$1","dir":"$project","work":"$root/$1","cache":"$root/cache","source":"main.$extension","wit":"wit","world":"smoke"$build_arguments}
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
            mkdir -p "$project/wit/deps/operands" "$project/vendor/include" \
                "$project/vendor/lib" "$root/cache"
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
            cat > "$project/vendor/include/native_math.h" <<'EOF'
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif
int32_t native_add(int32_t a, int32_t b);
#ifdef __cplusplus
}
#endif
EOF
            cat > "$root/native_math.c" <<'EOF'
#include "native_math.h"

int32_t native_add(int32_t a, int32_t b) {
    return a + b;
}
EOF
            wasm32-wasip2-clang -I"$project/vendor/include" -c "$root/native_math.c" \
                -o "$root/native_math.o"
            llvm-ar rcs "$project/vendor/lib/libnative-math.a" "$root/native_math.o"
            if [ "$language" = c ]; then
                cat > "$project/main.c" <<'EOF'
#include "native_math.h"
#include "smoke.h"

int32_t exports_smoke_add(int32_t a, int32_t b) {
    return native_add(a, b);
}
EOF
            else
                cat > "$project/main.cpp" <<'EOF'
#include "native_math.h"
#include "smoke.h"
#include <array>

int32_t exports_smoke_add(int32_t a, int32_t b) {
    const std::array<int32_t, 2> operands{a, b};
    return native_add(operands.front(), operands.back());
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
            make -C "$root" check CC="$compiler" FLAGS="-I$root/bind/bindings -I$project/vendor/include" \
                SOURCE="$project/main.$extension"
            run_builder build
            wasm-tools validate "$root/build/component.wasm"
            wasm-tools component wit "$root/build/component.wasm" | grep -F 'export add: func('
            ;;
        *) printf 'usage: %s fetch|build\n' "$0" >&2; exit 2 ;;
    esac
done

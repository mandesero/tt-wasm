#!/bin/sh
set -eu

work=/work
project=$work/project
cache=$work/cache

request() {
    tt-wasm-builder <<EOF
{"language":"python","operation":"$1","dir":"$project","work":"$work/$1","cache":"$cache","source":"main.py","wit":"world.wit","world":"calc"}
EOF
}

case "${1:-}" in
    fetch)
        mkdir -p "$project"
        cat > "$project/world.wit" <<'EOF'
package example:calc@0.1.0;

world calc {
    export add: func(a: s32, b: s32) -> s32;
}
EOF
        cat > "$project/main.py" <<'EOF'
import wit_world
from packaging.version import Version


class WitWorld(wit_world.WitWorld):
    def add(self, a: int, b: int) -> int:
        return Version(f"{a + b}.0").major
EOF
        cat > "$project/requirements.lock" <<'EOF'
packaging==25.0 --hash=sha256:29572ef2b1f17581046b3a2227d5c611fb25ec70ca1ba8554b24b0e69331a484
EOF
        request fetch
        test -d "$cache/dependencies/site-packages/packaging"
        ;;
    build)
        request bind
        test -d "$work/bind/bindings/wit_world"
        ruff check "$project/main.py"
        ruff format --check "$project/main.py"
        python3 -m venv "$work/venv"
        mkdir -p "$work/unit"
        cat > "$work/unit/test_calc.py" <<'EOF'
import unittest

from main import WitWorld


class CalculateTest(unittest.TestCase):
    def test_add(self):
        self.assertEqual(WitWorld().add(20, 22), 42)
EOF
        PYTHONPATH="$project:$work/bind/bindings:$cache/dependencies/site-packages" \
            "$work/venv/bin/python" -m unittest discover -s "$work/unit"
        cat > "$work/unit/debug_calc.py" <<'EOF'
from main import WitWorld

WitWorld().add(20, 22)
EOF
        PYTHONPATH="$project:$work/bind/bindings:$cache/dependencies/site-packages" \
            "$work/venv/bin/python" -m pdb -c "break $project/main.py:7" \
                -c continue -c 'p a + b' -c quit "$work/unit/debug_calc.py" \
                > "$work/pdb.log" 2>&1
        grep -Fx '42' "$work/pdb.log"
        request build
        wasm-tools validate "$work/build/component.wasm"
        wasm-tools component wit "$work/build/component.wasm" | grep -F 'export add:'
        ;;
    *)
        echo "usage: $0 fetch|build" >&2
        exit 2
        ;;
esac

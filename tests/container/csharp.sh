#!/bin/sh
set -eu

work=/work
project=$work/project
cache=$work/cache
template=/src/templates/csharp/default

request() {
    tt-wasm-builder <<EOF
{"language":"csharp","operation":"$1","dir":"$project","work":"$work/${2:-$1}","cache":"$cache","source":"Component.csproj","wit":"world.wit","world":"calc"}
EOF
}

case "${1:-}" in
    fetch)
        mkdir -p "$project/src"
        cp "$template/Component.csproj" "$template/global.json" "$template/nuget.config" \
            "$template/packages.lock.json" "$project"
        sed -e 's/{{.Package}}/example:calc@0.1.0/g' -e 's/{{.World}}/calc/g' \
            "$template/world.wit" > "$project/world.wit"
        sed 's/{{.WorldPascal}}/Calc/g' "$template/src/Implementation.cs" \
            > "$project/src/Implementation.cs"
        request fetch
        test -d "$cache/nuget/runtime.linux-x64.microsoft.dotnet.ilcompiler.llvm"
        ;;
    build)
        request bind
        test -s "$work/bind/bindings/Calc.cs"
        mkdir -p "$work/unit"
        cd "$work/unit"
        cp "$project/src/Implementation.cs" .
        dotnet new console --no-restore
        cat > Program.cs <<'EOF'
using CalcWorld;

if (CalcWorldExportsImpl.Add(20, 22) != 42)
{
    throw new InvalidOperationException("Add(20, 22) must equal 42");
}
EOF
        cat > ICalcWorldExports.cs <<'EOF'
namespace CalcWorld;

public interface ICalcWorldExports
{
    static abstract int Add(int a, int b);
}
EOF
        dotnet build --configuration Release -p:UseAppHost=false
        dotnet bin/Release/net10.0/unit.dll
        dotnet format whitespace --no-restore --verify-no-changes
        request build
        wasm-tools validate "$work/build/component.wasm"
        wasm-tools component wit "$work/build/component.wasm" | grep -F 'export add:'
        request build rebuild
        cmp "$work/build/component.wasm" "$work/rebuild/component.wasm"
        ;;
    *)
        echo "usage: $0 fetch|build" >&2
        exit 2
        ;;
esac

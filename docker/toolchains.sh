#!/bin/sh
set -eu

toolchain=$1
architecture=$2
case "$architecture" in
    amd64) upstream_arch=x86_64 ;;
    arm64) upstream_arch=aarch64 ;;
    *) echo "Unsupported build architecture: $architecture" >&2; exit 1 ;;
esac

# Keep upstream archives outside /out. Only the installed tools are copied into
# final images, so the compressed downloads are absent from runtime layers.
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
mkdir -p /out/bin

fetch() {
    curl -fsSL --retry 4 --retry-all-errors "$1" -o "$temp/$3"
    printf '%s  %s\n' "$2" "$temp/$3" | "${4:-sha256sum}" --check
}

case "$toolchain" in
    shared)
        case "$architecture" in
            amd64)
                wasm_tools_sha=ad62b2176037e93e1348cb65d6212d128ca9f097b63d155569f25215818ff7b1
                wkg_sha=235ecf455e11b8b146e00f24f24c95b573cfe6a432f332358463b229311cec57
                ;;
            arm64)
                wasm_tools_sha=6f19389a69e21c7dbd2a4e7ae72f807dc999988b48782c706c4102bad3e44487
                wkg_sha=1b897312b55a3586a91e7ceb7e14224f815b6a5ddeee903ffeca58735716d1be
                ;;
        esac
        tools=wasm-tools-1.261.0-$upstream_arch-linux
        fetch "https://github.com/bytecodealliance/wasm-tools/releases/download/v1.261.0/$tools.tar.gz" \
            "$wasm_tools_sha" wasm-tools.tar.gz
        fetch "https://github.com/bytecodealliance/wasm-pkg-tools/releases/download/v0.16.1/wkg-$upstream_arch-unknown-linux-gnu" \
            "$wkg_sha" wkg
        tar -xzf "$temp/wasm-tools.tar.gz" --strip-components=1 -C /out/bin "$tools/wasm-tools"
        install -m 0755 "$temp/wkg" /out/bin/wkg
        ;;
    c)
        case "$architecture" in
            amd64)
                wasi_arch=x86_64
                wasi_sha=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4
                bindgen_sha=3e81cc6523729f7532b4aa7968648a04abf0c711b7d1677150e9121f4e6458fe
                ;;
            arm64)
                wasi_arch=arm64
                wasi_sha=f7e243dff54d60bcc576e94d6166b69f410f2500ae4a9ceef34315be10e77971
                bindgen_sha=16829d8e4b81ef381c7007ad94d2b14c365360d0024cdfb5a34e5fbd4d8f1d89
                ;;
        esac
        bindgen=wit-bindgen-0.62.0-$upstream_arch-linux
        fetch "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-34/wasi-sdk-34.0-$wasi_arch-linux.tar.gz" \
            "$wasi_sha" wasi-sdk.tar.gz
        fetch "https://github.com/bytecodealliance/wit-bindgen/releases/download/v0.62.0/$bindgen.tar.gz" \
            "$bindgen_sha" wit-bindgen.tar.gz
        mkdir /out/wasi-sdk
        tar -xzf "$temp/wasi-sdk.tar.gz" --strip-components=1 -C /out/wasi-sdk
        tar -xzf "$temp/wit-bindgen.tar.gz" --strip-components=1 -C /out/bin "$bindgen/wit-bindgen"
        ;;
    go)
        case "$architecture" in
            amd64)
                tinygo_sha=ca9fc6d055e3889462dea4450a2e42a92242d88b5c362a7ec39bdb563413d78a
                binaryen_sha=2dc9c7813f5375db93d96ead4b78222fcc3e2677bbb832297af4797782a37489
                ;;
            arm64)
                tinygo_sha=a2165b9f46aff9a895a0abed54aedffab1e93317a387c6c2e9dd839425527cbd
                binaryen_sha=89c07ea56faf38d0fbecf36ca8ec0721756716185f265b568e133d427f299bf8
                ;;
        esac
        fetch "https://github.com/tinygo-org/tinygo/releases/download/v0.43.0/tinygo0.43.0.linux-$architecture.tar.gz" \
            "$tinygo_sha" tinygo.tar.gz
        binaryen="binaryen-version_133"
        fetch "https://github.com/WebAssembly/binaryen/releases/download/version_133/$binaryen-$upstream_arch-linux.tar.gz" \
            "$binaryen_sha" binaryen.tar.gz
        # Generated register maps for embedded boards are outside the Wasm
        # toolchain. Retain the CPU architecture packages and Wasm sysroots.
        tar -xzf "$temp/tinygo.tar.gz" -C /out \
            --exclude=tinygo/src/device/avr --exclude=tinygo/src/device/esp \
            --exclude=tinygo/src/device/nrf --exclude=tinygo/src/device/nxp \
            --exclude=tinygo/src/device/py32 --exclude=tinygo/src/device/renesas \
            --exclude=tinygo/src/device/rp --exclude=tinygo/src/device/sam \
            --exclude=tinygo/src/device/stm32
        tar -xzf "$temp/binaryen.tar.gz" --strip-components=2 -C /out/tinygo/bin "$binaryen/bin/wasm-opt"
        ;;
    csharp)
        if [ "$architecture" != amd64 ]; then
            echo "The released Tarantool C# guest SDK requires linux/amd64" >&2
            exit 1
        fi
        fetch https://builds.dotnet.microsoft.com/dotnet/Sdk/10.0.402/dotnet-sdk-10.0.402-linux-x64.tar.gz \
            1904a98dda2db34ba78ba3c4fb1b224455329af91c341d83547843c1839e112b04ae699ee92fa2449a8f62249196a938723078c0c60b9f65c88539d5687729c2 \
            dotnet.tar.gz sha512sum
        fetch https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-29/wasi-sdk-29.0-x86_64-linux.tar.gz \
            87d1d1a2879d139cdc624b968efad3d4a97b8078cdff95e63ac88ecafd1a0171 wasi-sdk.tar.gz
        fetch https://github.com/bytecodealliance/wit-bindgen/releases/download/v0.58.0/wit-bindgen-0.58.0-x86_64-linux.tar.gz \
            16831a7b18fed12f1b1ece26e458adb0c6c9ce0c7f3308e3f2fb7770f990a618 wit-bindgen.tar.gz
        mkdir /out/dotnet /out/wasi-sdk
        tar -xzf "$temp/dotnet.tar.gz" -C /out/dotnet
        tar -xzf "$temp/wasi-sdk.tar.gz" --strip-components=1 -C /out/wasi-sdk
        tar -xzf "$temp/wit-bindgen.tar.gz" --strip-components=1 -C /out/bin \
            wit-bindgen-0.58.0-x86_64-linux/wit-bindgen
        ;;
    *) echo "Unknown toolchain: $toolchain" >&2; exit 1 ;;
esac

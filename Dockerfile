# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY cmd/tt-wasm-builder ./cmd/tt-wasm-builder
COPY internal/component/driver ./internal/component/driver
COPY --from=tt-sdk /fetch /sdk/fetch
RUN --network=none go mod init github.com/mandesero/tt-wasm \
    && go mod edit -require=github.com/tarantool/tt/sdk/fetch@v0.0.0-0 \
        -replace=github.com/tarantool/tt/sdk/fetch=/sdk/fetch \
    && CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go build -mod=readonly -trimpath -o /tt-wasm-builder ./cmd/tt-wasm-builder

FROM debian:bookworm-slim AS wasm-tools
ADD --checksum=sha256:ad62b2176037e93e1348cb65d6212d128ca9f097b63d155569f25215818ff7b1 \
    https://github.com/bytecodealliance/wasm-tools/releases/download/v1.261.0/wasm-tools-1.261.0-x86_64-linux.tar.gz \
    /tmp/wasm-tools.tar.gz
ADD --checksum=sha256:235ecf455e11b8b146e00f24f24c95b573cfe6a432f332358463b229311cec57 \
    https://github.com/bytecodealliance/wasm-pkg-tools/releases/download/v0.16.1/wkg-x86_64-unknown-linux-gnu \
    /tmp/wkg
RUN --network=none mkdir -p /out/bin \
    && tar -xzf /tmp/wasm-tools.tar.gz --strip-components=1 -C /out/bin \
        wasm-tools-1.261.0-x86_64-linux/wasm-tools \
    && install -m 0755 /tmp/wkg /out/bin/wkg

FROM debian:bookworm-slim AS common
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=wasm-tools /out/bin/ /usr/local/bin/
COPY --from=builder /tt-wasm-builder /usr/local/bin/tt-wasm-builder
ENTRYPOINT ["tt-wasm-builder"]

FROM common AS c
ADD --checksum=sha256:b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4 \
    https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-34/wasi-sdk-34.0-x86_64-linux.tar.gz \
    /tmp/wasi-sdk.tar.gz
ADD --checksum=sha256:3e81cc6523729f7532b4aa7968648a04abf0c711b7d1677150e9121f4e6458fe \
    https://github.com/bytecodealliance/wit-bindgen/releases/download/v0.62.0/wit-bindgen-0.62.0-x86_64-linux.tar.gz \
    /tmp/wit-bindgen.tar.gz
RUN --network=none mkdir -p /opt/wasi-sdk \
    && tar -xzf /tmp/wasi-sdk.tar.gz --strip-components=1 -C /opt/wasi-sdk \
    && tar -xzf /tmp/wit-bindgen.tar.gz --strip-components=1 -C /usr/local/bin \
        wit-bindgen-0.62.0-x86_64-linux/wit-bindgen \
    && rm /tmp/wasi-sdk.tar.gz /tmp/wit-bindgen.tar.gz
ENV PATH="/opt/wasi-sdk/bin:${PATH}"
RUN --network=none tt-wasm-builder check c && tt-wasm-builder check cpp

FROM node:24.21.0-bookworm-slim AS js
COPY --from=common /usr/local/bin/ /usr/local/bin/
WORKDIR /opt/wasm-js
RUN printf '%s\n' '{"dependencies":{"@bytecodealliance/jco":"1.37.0","@bytecodealliance/componentize-js":"0.23.0"},"overrides":{"@bytecodealliance/componentize-js":"0.23.0"}}' > package.json \
    && npm install --ignore-scripts --no-audit --no-fund \
    && npm cache clean --force
ENV PATH="/opt/wasm-js/node_modules/.bin:${PATH}"
RUN --network=none tt-wasm-builder check js
ENTRYPOINT ["tt-wasm-builder"]

FROM golang:1.27-bookworm AS go
COPY --from=common /usr/local/bin/ /usr/local/bin/
RUN GOBIN=/usr/local/bin go install go.bytecodealliance.org/cmd/wit-bindgen-go@v0.7.0 \
    && go clean -cache -modcache
ADD --checksum=sha256:ca9fc6d055e3889462dea4450a2e42a92242d88b5c362a7ec39bdb563413d78a \
    https://github.com/tinygo-org/tinygo/releases/download/v0.43.0/tinygo0.43.0.linux-amd64.tar.gz \
    /tmp/tinygo.tar.gz
ADD --checksum=sha256:2dc9c7813f5375db93d96ead4b78222fcc3e2677bbb832297af4797782a37489 \
    https://github.com/WebAssembly/binaryen/releases/download/version_133/binaryen-version_133-x86_64-linux.tar.gz \
    /tmp/binaryen.tar.gz
RUN --network=none tar -xzf /tmp/tinygo.tar.gz -C /opt \
    && tar -xzf /tmp/binaryen.tar.gz --strip-components=2 -C /opt/tinygo/bin \
        binaryen-version_133/bin/wasm-opt \
    && rm /tmp/tinygo.tar.gz /tmp/binaryen.tar.gz
ENV PATH="/opt/tinygo/bin:${PATH}"
RUN --network=none tt-wasm-builder check go
ENTRYPOINT ["tt-wasm-builder"]

FROM rust:1.99.0-bookworm AS rust
COPY --from=common /usr/local/bin/ /usr/local/bin/
RUN rustup target add wasm32-wasip1 \
    && cargo install --locked --root /usr/local cargo-component --version 0.21.1 \
    && rm -rf /usr/local/cargo/registry /usr/local/cargo/git
RUN --network=none tt-wasm-builder check rust
ENTRYPOINT ["tt-wasm-builder"]

FROM rust:1.99.0-bookworm AS csharp-bindgen
RUN cargo install --locked --root /out wit-bindgen-cli --version 0.58.0

FROM common AS csharp
COPY --from=csharp-bindgen /out/bin/wit-bindgen /usr/local/bin/
ADD --checksum=sha256:87d1d1a2879d139cdc624b968efad3d4a97b8078cdff95e63ac88ecafd1a0171 \
    https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-29/wasi-sdk-29.0-x86_64-linux.tar.gz \
    /tmp/wasi-sdk.tar.gz
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl libicu72 libssl3 zlib1g \
        libgssapi-krb5-2 libstdc++6 libgcc-s1 \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /opt/dotnet /opt/wasi-sdk \
    && curl --fail --silent --show-error --location \
        https://builds.dotnet.microsoft.com/dotnet/Sdk/10.0.402/dotnet-sdk-10.0.402-linux-x64.tar.gz \
        --output /tmp/dotnet.tar.gz \
    && echo '1904a98dda2db34ba78ba3c4fb1b224455329af91c341d83547843c1839e112b04ae699ee92fa2449a8f62249196a938723078c0c60b9f65c88539d5687729c2  /tmp/dotnet.tar.gz' | sha512sum --check \
    && tar -xzf /tmp/dotnet.tar.gz -C /opt/dotnet \
    && tar -xzf /tmp/wasi-sdk.tar.gz --strip-components=1 -C /opt/wasi-sdk \
    && rm /tmp/dotnet.tar.gz /tmp/wasi-sdk.tar.gz
ENV DOTNET_ROOT=/opt/dotnet WASI_SDK_PATH=/opt/wasi-sdk \
    PATH="/opt/dotnet:${PATH}" DOTNET_CLI_TELEMETRY_OPTOUT=1
RUN --network=none tt-wasm-builder check csharp

FROM python:3.12-slim-bookworm AS python
COPY --from=common /usr/local/bin/ /usr/local/bin/
ADD --checksum=sha256:8623cd8cab8453cb0cd8bf03f3c7103420d06f2f3d360186a280a70a1ca5e89f \
    https://files.pythonhosted.org/packages/ed/c6/ad1431751ff1969a132323f6e2797ec38ccdf9d0a53aabb45d79221092a4/componentize_py-0.25.1-cp39-abi3-manylinux_2_28_x86_64.whl \
    /tmp/componentize_py-0.25.1-cp39-abi3-manylinux_2_28_x86_64.whl
RUN --network=none python3 -m pip install --no-cache-dir --no-index --no-deps --no-compile \
        /tmp/componentize_py-0.25.1-cp39-abi3-manylinux_2_28_x86_64.whl \
    && rm /tmp/componentize_py-0.25.1-cp39-abi3-manylinux_2_28_x86_64.whl
RUN --network=none tt-wasm-builder check python
ENTRYPOINT ["tt-wasm-builder"]

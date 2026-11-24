# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
ARG DEBIAN_SNAPSHOT=20261124T000000Z
ARG DEBIAN=debian:trixie-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
ARG GOLANG=golang:1.27.4-trixie@sha256:0f82c58710c405cc012da2fbd2b04bde9ced20392a89f66062822d711d7d6b4b
ARG RUST=rust:1.99.0-slim-trixie@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581
ARG NODE=node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66
ARG PYTHON=python:3.12-slim-trixie@sha256:191083cdf6c7bda8c76158983163ead5d5cf7b77cf3033e805bb0f5296f51b4b

FROM --platform=$BUILDPLATFORM ${GOLANG} AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY cmd/tt-wasm-builder ./cmd/tt-wasm-builder
COPY internal/component/driver ./internal/component/driver
COPY --from=tt-sdk /fetch /sdk/fetch
RUN --network=none --mount=type=cache,target=/root/.cache/go-build \
    go mod init github.com/mandesero/tt-wasm \
    && go mod edit -require=github.com/tarantool/tt/sdk/fetch@v0.0.0-0 \
        -replace=github.com/tarantool/tt/sdk/fetch=/sdk/fetch \
    && CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go build -mod=readonly -trimpath -o /out/tt-wasm-builder ./cmd/tt-wasm-builder

FROM --platform=$BUILDPLATFORM ${DEBIAN} AS downloads
ARG DEBIAN_SNAPSHOT
RUN --mount=type=bind,source=docker/install-debs.sh,target=/tmp/install-debs \
    sh /tmp/install-debs "$DEBIAN_SNAPSHOT" ca-certificates curl
COPY docker/toolchains.sh /usr/local/bin/install-toolchain

FROM downloads AS wasm-tools
ARG TARGETARCH
RUN sh /usr/local/bin/install-toolchain shared "$TARGETARCH"
COPY --from=builder /out/tt-wasm-builder /out/bin/tt-wasm-builder

FROM ${DEBIAN} AS common
ARG DEBIAN_SNAPSHOT
RUN --mount=type=bind,source=docker/install-debs.sh,target=/tmp/install-debs \
    sh /tmp/install-debs "$DEBIAN_SNAPSHOT" ca-certificates \
    && install -d -m 1777 /work
COPY --link --from=wasm-tools /out/bin/ /usr/local/bin/
ENV HOME=/tmp XDG_CACHE_HOME=/tmp/.cache
WORKDIR /work
ENTRYPOINT ["tt-wasm-builder"]

FROM downloads AS c-tools
ARG TARGETARCH
RUN sh /usr/local/bin/install-toolchain c "$TARGETARCH"

FROM common AS c
ARG DEBIAN_SNAPSHOT
RUN --mount=type=bind,source=docker/install-debs.sh,target=/tmp/install-debs \
    sh /tmp/install-debs "$DEBIAN_SNAPSHOT" make
COPY --link --from=c-tools /out/wasi-sdk /opt/wasi-sdk
COPY --link --from=c-tools /out/bin/ /usr/local/bin/
ENV PATH="/opt/wasi-sdk/bin:${PATH}"
USER 10001:10001
RUN --network=none --mount=type=tmpfs,target=/tmp tt-wasm-builder check c && tt-wasm-builder check cpp

FROM ${NODE} AS js-tools
ARG TARGETARCH
WORKDIR /opt/wasm-js
COPY docker/js/package.json docker/js/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --ignore-scripts --no-audit --no-fund
RUN --mount=type=bind,source=docker/js/wizer.sh,target=/tmp/wizer \
    case "$TARGETARCH" in amd64) wizer_arch=x64 ;; arm64) wizer_arch=arm64 ;; *) exit 1 ;; esac \
    && wizer="node_modules/@bytecodealliance/wizer-linux-$wizer_arch/wizer" \
    && mv "$wizer" "$wizer.bin" \
    && install -m 0755 /tmp/wizer "$wizer"

FROM ${NODE} AS js
COPY --link --from=wasm-tools /out/bin/ /usr/local/bin/
COPY --link --from=js-tools /opt/wasm-js /opt/wasm-js
RUN install -d -m 1777 /work
ENV HOME=/tmp XDG_CACHE_HOME=/tmp/.cache PATH="/opt/wasm-js/node_modules/.bin:${PATH}"
WORKDIR /work
USER 10001:10001
RUN --network=none --mount=type=tmpfs,target=/tmp tt-wasm-builder check js
ENTRYPOINT ["tt-wasm-builder"]

FROM --platform=$BUILDPLATFORM ${GOLANG} AS go-bindgen
ARG TARGETOS
ARG TARGETARCH
# go install refuses GOBIN for cross-compiled binaries. Its default output
# location is architecture-qualified only for cross compilation.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go install go.bytecodealliance.org/cmd/wit-bindgen-go@v0.7.0 \
    && mkdir -p /out \
    && find /go/bin -type f -name wit-bindgen-go -exec mv {} /out/wit-bindgen-go \; \
    && test -x /out/wit-bindgen-go

FROM ${GOLANG} AS go-distribution

FROM downloads AS go-tools
ARG TARGETARCH
RUN sh /usr/local/bin/install-toolchain go "$TARGETARCH"

FROM common AS go
ARG DEBIAN_SNAPSHOT
RUN --mount=type=bind,source=docker/install-debs.sh,target=/tmp/install-debs \
    sh /tmp/install-debs "$DEBIAN_SNAPSHOT" gcc git libc6-dev
COPY --link --from=go-distribution /usr/local/go /usr/local/go
COPY --link --from=go-bindgen /out/wit-bindgen-go /usr/local/bin/
COPY --link --from=go-tools /out/tinygo /opt/tinygo
ENV PATH="/opt/tinygo/bin:/usr/local/go/bin:${PATH}" GOTOOLCHAIN=local
USER 10001:10001
RUN --network=none --mount=type=tmpfs,target=/tmp tt-wasm-builder check go

FROM ${RUST} AS rust-tools
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    cargo install --locked --root /out cargo-component --version 0.21.1

FROM ${RUST} AS rust
ARG DEBIAN_SNAPSHOT
RUN --mount=type=bind,source=docker/install-debs.sh,target=/tmp/install-debs \
    sh /tmp/install-debs "$DEBIAN_SNAPSHOT" apt g++ git libssl-dev linux-libc-dev make pkg-config \
    && install -d -m 1777 /work
RUN rustup target add wasm32-wasip1 && rustup component add clippy rustfmt
COPY --link --from=wasm-tools /out/bin/ /usr/local/bin/
COPY --link --from=rust-tools /out/bin/cargo-component /usr/local/bin/
ENV HOME=/tmp XDG_CACHE_HOME=/tmp/.cache CARGO_HOME=/tmp/cargo
WORKDIR /work
USER 10001:10001
RUN --network=none --mount=type=tmpfs,target=/tmp tt-wasm-builder check rust
ENTRYPOINT ["tt-wasm-builder"]

FROM downloads AS csharp-tools
ARG TARGETARCH
RUN sh /usr/local/bin/install-toolchain csharp "$TARGETARCH"

FROM common AS csharp
ARG DEBIAN_SNAPSHOT
RUN --mount=type=bind,source=docker/install-debs.sh,target=/tmp/install-debs \
    sh /tmp/install-debs "$DEBIAN_SNAPSHOT" libicu76 libssl3t64 zlib1g \
        libgssapi-krb5-2 libstdc++6 libgcc-s1
COPY --link --from=csharp-tools /out/bin/wit-bindgen /usr/local/bin/
COPY --link --from=csharp-tools /out/dotnet /opt/dotnet
COPY --link --from=csharp-tools /out/wasi-sdk /opt/wasi-sdk
ENV DOTNET_ROOT=/opt/dotnet WASI_SDK_PATH=/opt/wasi-sdk \
    PATH="/opt/dotnet:${PATH}" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1
USER 10001:10001
RUN --network=none --mount=type=tmpfs,target=/tmp tt-wasm-builder check csharp

FROM ${PYTHON} AS python
COPY --link --from=wasm-tools /out/bin/ /usr/local/bin/
RUN --mount=type=bind,source=docker/python-requirements.lock,target=/tmp/toolchain.lock \
    --mount=type=cache,target=/root/.cache/pip \
    python3 -m pip install --require-hashes --only-binary=:all: --no-deps --no-compile \
        -r /tmp/toolchain.lock \
    && install -d -m 1777 /work
ENV HOME=/tmp XDG_CACHE_HOME=/tmp/.cache
WORKDIR /work
USER 10001:10001
RUN --network=none --mount=type=tmpfs,target=/tmp tt-wasm-builder check python
ENTRYPOINT ["tt-wasm-builder"]

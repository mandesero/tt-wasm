# Wasm builder image audit

Audit date 24 November 2026. The tested project SDK was Tarantool Wasm
0.1.0. The shared image tools are `tt-wasm-builder`, Wasm Tools 1.261.0
and wkg 0.16.1. The tests exercise the builder's JSON input, bindings,
dependency acquisition, component output and native host tooling.

## Toolchain coverage

| Language | Installed development tools | Functional checks |
| --- | --- | --- |
| C | WASI SDK 34 with Clang, LLD, LLVM utilities, clang-format, clang-tidy and LLDB, wit-bindgen 0.62.0, make | Fetch vendored WIT, generate bindings, analyze and format sources, compile with warnings and DWARF, archive an object, link and execute a component. |
| C++ | The C image plus the SDK's Clang++ and libc++ sysroots | Exercise the same pipeline with a C++ standard-library type, inspect DWARF and execute the result. |
| Go | Go 1.27.4 with gofmt, go vet and object inspection, Git, GCC and libc headers, TinyGo 0.43.0, wit-bindgen-go 0.7.0 and Binaryen 133 wasm-opt | Fetch a checksummed Go module, generate Go bindings, verify formatting, vet and run a host package test, compile a cgo program, inspect a compiled test and execute a TinyGo component. |
| JavaScript | Node.js 24.21.0, npm and Yarn, Jco 1.37.0, ComponentizeJS 0.23.0, Wizer, Prettier, Node's test runner and syntax checker | Install a lockfile dependency, inject the guest SDK, generate TypeScript declarations, bundle and componentize a guest, check syntax and formatting, run the Node test and execute the component. |
| Rust | Rust 1.99.0, rustup, Cargo, `wasm32-wasip1`, cargo-component 0.21.1, rustfmt, Clippy and native build dependencies | Fetch locked host and WASI crates, generate bindings, build with networking disabled, check formatting and Clippy, run the host test and execute the component. |
| Python | CPython 3.12, pip, venv, unittest, pdb, Ruff and componentize-py 0.25.1 | Install a checksummed project dependency, inject the guest SDK, generate bindings, lint and format, run the host unit test, inspect the calculation in pdb and execute the component. |
| C# | .NET SDK 10.0.402 with NuGet, Roslyn and dotnet format, WASI SDK 29 and wit-bindgen 0.58.0 | Restore the scaffold's locked packages, generate C# bindings, run the host calculation, check formatting, publish using NativeAOT and execute the result. |

These are development images for the Tarantool Wasm workflow. Interactive
language runtimes, unit-test facilities, formatters and static checks are
available in the environments where the table lists them. Rust, Go and
.NET projects can produce debug symbols for host tools and inspect Wasm
artifacts with Wasm Tools. IDEs, host-specific debugger add-ons and
project-specific test frameworks can be supplied by the project.
C# remains amd64-only because the current guest SDK and existing NuGet
lockfiles select the Linux x64 NativeAOT packages.

## Packaging changes

- The original Bookworm runtime could not load the shipped wkg executable.
  It requires glibc 2.38 and 2.39 symbols. Moving the images to pinned
  Trixie bases restored the shared toolchain. The old JavaScript install also
  selected a ComponentizeJS shim the driver rejected. The npm lock now pins
  that shim and preserves Jco's separate dependency version.
- Final images receive extracted, checksum-verified tools. This removes
  the archived payloads formerly distributed in C/C++ (198 MB), Go
  (298 MB), C# (119 MB) and Python (17 MB) layers. Those numbers are the
  archived file sizes per amd64 target. The Go final image copies the
  official Go distribution into the shared base and installs selected host
  build dependencies. Rust uses the official slim base and copies in the
  locked Cargo tool from a producer stage. C# uses the verified upstream
  wit-bindgen binary and avoids compiling it during the image build.
- The three shared executables are published as a linked BuildKit copy
  layer. C, Go and C# extend the same Debian runtime stage. Separate
  download, npm and Cargo stages keep temporary inputs out of the images
  and permit build cache reuse.
- The images run as UID and GID 10001 by default. Smoke tests also exercise
  the Docker runner's numeric caller UID pattern and a read-only root.
  The JavaScript package wraps Wizer to supply a writable cache and home
  because ComponentizeJS launches it with a guest-specific environment.
  The builder has no service startup, listeners or toolchain installation
  requirement when serving a request.

## Base image trade-offs

The upstream GNU/Linux tool archives require glibc. Alpine would require
separate musl builds or a compatibility layer for those archives.
Debian Trixie supplies the required glibc and keeps the official slim
Node, Python and Rust runtimes on the same distribution. The Go and Rust
buildpack-deps variants include a broader set of native packages than
the builders need. The Dockerfile retains the compilers, headers and
VCS tools needed for supported project workflows. The WASI SDK archives
preserve their sysroots and LLVM debug tools. Go retains the native Go
and TinyGo Wasm toolchains and omits generated TinyGo peripheral maps
for nine microcontroller families. That change removed 934 MB of
uncompressed layers and 90 MB of compressed layers from the first
working Go candidate. The full upstream TinyGo distribution supplies
the register maps for embedded firmware builds. Prettier and Ruff add
development coverage in JavaScript and Python.

## Observed amd64 measurements

Docker Engine 24.0.2 and Buildx 0.10.5 on an eight-vCPU Linux host.
The original Dockerfile failed its toolchain check. To measure the
baseline footprint, the audit exported the original image configuration
and layers immediately before that check. All six diagnostic images
still failed when the check ran separately. The measurements below
compare those diagnostic images with the final images.

Image sizes are decimal MB. Compressed sizes count the registry layer
blobs. Cached build times include a repeat Buildx build and load on the
same host. A single cold pull per image used a fresh Docker store and
a loopback OCI registry. Startup is the median of five Docker runs of
`/bin/true` with locally loaded layers, including creation and cleanup.
The two revisions used the same host and network path. Concurrent disk
activity and the single pull sample limit timing precision.

| Target | Compressed MB | Uncompressed MB | Cached build s | Cold pull s | Startup s |
| --- | ---: | ---: | ---: | ---: | ---: |
| C and C++ | 454.8 → 257.2 | 984.7 → 785.8 | 1.10 → 1.12 | 16.93 → 13.93 | 0.447 → 0.381 |
| Go | 815.9 → 343.0 | 2499.6 → 1043.0 | 2.58 → 0.70 | 38.63 → 20.42 | 0.390 → 0.382 |
| JavaScript | 203.1 → 207.9 | 644.4 → 660.2 | 2.01 → 0.71 | 10.89 → 12.76 | 0.368 → 0.432 |
| Python | 105.7 → 97.4 | 258.0 → 261.7 | 1.96 → 0.75 | 5.01 → 6.16 | 0.366 → 0.427 |
| Rust | 640.6 → 471.3 | 1768.7 → 1331.5 | 2.04 → 0.70 | 25.24 → 21.38 | 0.371 → 0.468 |
| C# | 559.3 → 437.7 | 1352.6 → 1225.0 | 1.15 → 0.66 | 19.87 → 18.70 | 0.374 → 0.425 |

The total compressed size across the six manifest references fell from
2779.4 MB to 1814.6 MB. The unique compressed
blobs fell from 2695.6 MB to 1644.9 MB. The baseline had
43 unique layers and 4 shared across multiple images. The final set has
39 unique layers and 5 shared across multiple images. The shared layers
save 169.7 MB when a store pulls all six final images.

| Target | Fetch peak memory MB | Offline build peak memory MB | Offline CPU s | High scan findings | Critical scan findings |
| --- | ---: | ---: | ---: | ---: | ---: |
| C and C++ | 6.7 | 30.6 | 1.3 | 52 | 0 |
| Go | 16.2 | 484.1 | 68.8 | 93 | 0 |
| JavaScript | 79.8 | 1210.3 | 32.0 | 78 | 0 |
| Python | 50.6 | 557.5 | 24.2 | 53 | 0 |
| Rust | 76.7 | 1246.3 | 155.1 | 90 | 0 |
| C# | 1070.2 | 311.8 | 39.0 | 56 | 0 |

Memory and CPU are cgroup v2 counters for the final fixture containers.
The build figures include the host language checks and exclude Wasmtime,
which executes separately. The C row includes both language fixtures.
The failed baseline toolchain checks prevented comparable build resource
measurements.

Trivy 0.76.2 scanned all final amd64 images. The Rust scan initially
flagged three critical findings in the inherited `linux-libc-dev`
package. The final image updates that package to the Debian snapshot's
6.12.118-1 release. All six final scans returned zero critical findings.
The high findings remain visible in CI and can guide package updates.

## Deployment validation

All six amd64 target images completed local Docker builds and registry
pushes and pulls. The smoke tests exercised offline builds with a
read-only project, dependency cache and root filesystem. Wasmtime 51
returned `42` from each language's component. The workflow validates
the same fixtures on native architecture runners and publishes an OCI
index only after every required platform passes. A local Buildx rehearsal
pushed two platform manifests by digest, merged them into an annotated
OCI index and pulled by digest. The Go repository test suite passed with
the public SDK revision used by CI.

Native arm64 toolchain execution, authenticated GHCR publication and
Kubernetes deployment still require their target environments. C#
package restores contact Microsoft's upstream NuGet feed during the
online fetch phase.

## Reproduce the measurements

Use separate Buildx builders when measuring a fresh build. The command
writes per-target timing and exit status to JSON.

```sh
scripts/benchmark-images.py build --sdk ../tt/sdk --builder audit-builder \
  --no-cache --image-prefix tt-wasm-audit c go js python rust csharp
```

To reproduce the diagnostic baseline, export `f23e74d` to a temporary
directory, remove the final `RUN --network=none tt-wasm-builder check …`
lines from that copy of its Dockerfile, and pass the directory as
`--source`. Use a fresh builder and `--no-cache` for cold build timings.
For cold pulls, use disposable Docker workers, an empty image store per
sample, and the same registry, platform and network conditions for
both revisions. Record cache state when measuring warm pulls.

```sh
WASMTIME=/tmp/tt-wasm-test-runtime/wasmtime \
  scripts/benchmark-images.py measure --registry --pull --smoke \
  python=ghcr.io/mandesero/tt-wasm-builder-python:v1.2.3
```

Pass all image references to calculate registry layer reuse across the
set. Use the same test runtime and smoke fixtures for comparable CPU and
memory figures. See the [deployment guide](builder-images.md) for the
native CI matrix, scan gate and digest-based release process.

# The wasm module

`tt wasm` prepares, scaffolds and builds WebAssembly components for
[tarantool-wasm](https://github.com/tarantool/tarantool-wasm-rs). The
module registers the `wasm` backend used by `tt package build` and
`tt package pack`. It provides the `tt wasm` commands and checks the
`.wasm` files of a package.

This Go module uses the public tt SDK for project, build and fetch operations.
The exported constructor `wasm.New` registers it as a tt module.

- `wasm.go`, `new.go` and `operations.go` define the commands. `prepare`
  resolves the runtime rock through the project SDK. `new` scaffolds and
  declares a component. `tt wasm build NAME` runs the package build for
  that component.
- `internal/component` implements the backend, profiles and generated
  files. Its drivers generate bindings and run the Python, JavaScript, Go,
  Rust, C, C++ and C# toolchains. The runner uses local tools or starts
  `cmd/tt-wasm-builder` in Docker.
- `internal/wasm` handles the runtime rocks and verified SDK release
  downloads. `TT_WASM_RELEASES` can override the release table.
- `filecheck.go` checks the package's `.wasm` files. It accepts components,
  warns on core modules and rejects files with an invalid Wasm header.

## Develop with a local tt checkout

Keep this repository beside a `tt` checkout. Use a distribution manifest in
this directory to select this module and generate a workspace against that SDK.

```toml
manifest_version = "0.1"
manifest_kind = "tt-distribution"

[package]
name = "tt"

[core]
path = "../tt"

[distribution]
exclude = ["wasm"]

[dependencies]
wasm = "."
```

```console
$ tt binary workspace
$ go test ./...
$ tt binary build tt
```

The development workspace replaces the SDK's placeholder versions. For a
release, require the `sdk`, `sdk/build`, `sdk/connect` and `sdk/fetch`
modules at the same published `github.com/tarantool/tt` commit. See the
[SDK versioning guide](https://github.com/tarantool/tt/blob/v3/sdk/README.md#versioning).

## Build a local toolchain image

```console
$ docker build --build-context tt-sdk=../tt/sdk --platform linux/amd64 \
    --target python -t tt-wasm-builder:python .
```

Other targets are `js`, `go`, `rust`, `c` and `csharp`. The `c` image
contains the C and C++ toolchains. From the tt checkout on Linux x86-64,
use the prebuilt image and the tt binary built with this module:

```console
$ PYTEST_ADDOPTS=--tt-cmd=/path/to/tt TT_WASM_TEST_PYTHON_IMAGE=tt-wasm-builder:python test/integration/wasm/run_linux.sh python
```

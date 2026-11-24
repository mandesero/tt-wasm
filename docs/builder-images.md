# Wasm builder images

These images build Tarantool WebAssembly components. The entrypoint performs
one `tt-wasm-builder` operation, accepts a JSON request on standard input,
and exits. `tt wasm` handles request creation and dependency caches.

| Docker target | `tt wasm` language | Image suffix | Host platforms |
| --- | --- | --- | --- |
| `c` | `c`, `cpp` | `builder-c` | `linux/amd64`, `linux/arm64` |
| `go` | `go` | `builder-go` | `linux/amd64`, `linux/arm64` |
| `js` | `js` | `builder-js` | `linux/amd64`, `linux/arm64` |
| `python` | `python` | `builder-python` | `linux/amd64`, `linux/arm64` |
| `rust` | `rust` | `builder-rust` | `linux/amd64`, `linux/arm64` |
| `csharp` | `csharp` | `builder-csharp` | `linux/amd64` |

The platform describes the machine running the toolchain. The output is a
WebAssembly component on both host architectures. These are builder
images. The separate native Tarantool runtime rock in the default 0.1.0
release table currently targets Linux amd64 and macOS arm64. C# follows the released
Tarantool guest SDK and its locked Linux x64 NativeAOT compiler packages.

## Build and validate locally

Use Docker with Buildx and a compatible `tt` checkout. The release workflow
pins a `tt` repository and revision for the SDK. Pass the SDK directory as
a named build context.

```sh
target=python
platform=linux/amd64
image=tt-wasm-builder:$target

docker buildx build --load --build-context tt-sdk=../tt/sdk \
  --platform "$platform" --target "$target" --tag "$image" .
docker run --rm --network none "$image" check "$target"

scripts/install-test-runtime.sh /tmp/tt-wasm-test-runtime
WASMTIME=/tmp/tt-wasm-test-runtime/wasmtime scripts/container-smoke.sh "$target" "$image"
```

The smoke test uses a checksum-verified Tarantool guest SDK, fetches project
dependencies, then generates bindings and builds in a separate container with
networking disabled. It validates the component and calls its exported
`add` function when Wasmtime is set. Pass `c` to exercise both C and C++.
Use a native machine for each architecture when validating a release.
Set `TT_WASM_CONTAINER_SDK_ARCHIVE` to reuse the pinned SDK archive across
runs. Set `TT_WASM_CONTAINER_METRICS=1` to collect cgroup v2 CPU and memory
measurements.

## Publish and deploy

The [builder image workflow](../.github/workflows/builder-images.yml) calls
the same [native build workflow](../.github/workflows/builder-images-native.yml)
for validation and release. Pull requests, branch pushes, and manual branch
runs have read-only repository access. The Go suite runs against the same
pinned `tt` SDK revision as the container builds. Validation keeps a separate
BuildKit cache for each target and architecture.

A `vMAJOR.MINOR.PATCH` Git tag on the history of the repository's default
branch can publish. The workflow checks the tag before the image matrix
starts, grants registry writes to the release caller, and builds from the
same commit that passed the Go suite. Release builds bypass the GitHub
Actions BuildKit cache. Each native job stages an OCI image by digest, pulls
that digest for the component smoke test, and closes its GHCR login before
testing. Trivy downloads its database separately and scans the saved image
offline with read-only inputs. A fixable critical finding blocks promotion.

Once the complete matrix passes, the publication jobs check the staged
platforms, assemble one OCI index per target, and publish
`ghcr.io/<owner>/<repository>-builder-<target>:vX.Y.Z`. A tag for the same
source and revision is reused on reruns. A conflicting tag fails publication.
Roll forward or back by choosing a release index digest. See the
[upstream CI comparison](#tarantool-ci-reference) for the rationale.

Enable GitHub Actions and allow its repository token to publish to the
associated GHCR packages. Protect the version Git tags and restrict
package write access. Package visibility controls whether consumers
need registry credentials. For native arm64 on private repositories,
configure a compatible runner and update the workflow runner label as
needed. The promotion script accepts an OCI repository reference. The
provided GitHub workflow authenticates to GHCR with `GITHUB_TOKEN`. [GitHub runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners), [Docker multi-platform builds](https://docs.docker.com/build/ci/github-actions/multi-platform/)

Use the immutable **index digest** from the workflow summary or the
`builder-images-vX.Y.Z` artifact to deploy the same release across native
workers. The artifact contains a `builder-images.json` that maps all six
targets to their index and platform digests. Extract the Python reference with
`jq -er '.images[] | select(.target == "python") | .reference' builder-images.json`.
The version tag also lets you rediscover the digest with
`docker buildx imagetools inspect`:

```sh
image=ghcr.io/mandesero/tt-wasm-builder-python:v1.2.3
docker buildx imagetools inspect "$image"
```

Replace the tag with the reported `@sha256:…` digest:

```sh
PYTHON_BUILDER='ghcr.io/mandesero/tt-wasm-builder-python@sha256:<index-digest>'
tt wasm prepare --lang python --platform linux/amd64 --image "$PYTHON_BUILDER"
tt wasm build calculator
```

Set `--platform linux/arm64` explicitly for supported arm64 images. Deploy
an upgrade with `tt wasm prepare --update` and the new digest. Roll back by
preparing with `--update` and the previous digest, or by restoring the
previous profile lock. Retain the indexes you use for rollbacks in your
registry.

The images default to UID and GID 10001, work in `/work`, and use `/tmp`
for writable home state. The Docker runner uses the calling Linux UID and GID
for bind-mounted files. Start with a read-only project mount, writable work
and cache mounts, and a writable `/tmp`. Give network access to dependency
fetches and mount cached dependencies read-only for the build. On Kubernetes,
use a Job for each operation, reference the index digest, mount the JSON
request as a file and redirect it to the entrypoint's standard input.
A non-root security context, dropped capabilities, a read-only root
filesystem, and an `emptyDir` for `/tmp` match the smoke-test setup. Grant
the selected UID write access to shared work and cache volumes.

## Update a toolchain

- Update the image index digests and Debian snapshot date in the Dockerfile
  together, then run the full native matrix. Debian snapshots retain
  signed package metadata. Use an updated snapshot to bring in security fixes.
- Update release URLs and checksums in `docker/toolchains.sh`. Update the
  JavaScript npm lock with the pinned Node/npm distribution and use
  `npm ci` in builds. Keep Python wheel hashes for both architectures.
- Keep driver version and SDK constraints aligned with the image. Run
  `check` and the full fetch, bind, build and runtime tests for each affected
  target. Review the vulnerability scan and record any platform limitations
  before tagging a release.

## Tarantool CI reference

| Area | Upstream practice | Wasm builder decision |
| --- | --- | --- |
| Release orchestration | Tarantool's distribution workflow calls native package build and test jobs and invokes Docker publishing on release tags after both architecture jobs succeed. The `tt` release workflow checks tag ancestry before packaging. | Reuse one builder workflow. Check stable tag ancestry and run the Go suite before the image matrix. Promote release indexes after every image test and scan passes. [Tarantool distribution](https://github.com/tarantool/tarantool/blob/18aa11bac1eff135199efa6482dbdb4f0145ffbd/.github/workflows/distrib.yml#L34-L86) · [Tarantool packaging](https://github.com/tarantool/tarantool/blob/18aa11bac1eff135199efa6482dbdb4f0145ffbd/.github/workflows/packaging_build_test_deploy.yml#L58-L131) · [tt release](https://github.com/tarantool/tt/blob/b3a4a5fc836ccb65889d3ab11d0cb5f5f750d83a/.github/workflows/publish.yml#L27-L46) |
| Images, architectures and caches | Tarantool assembles the server image from released packages and uses Buildx with QEMU for Linux amd64 and arm64 on an x86 runner. It disables the build cache for publication. The `tt` Docker workflow exercises test images on native runners. | Keep native runners and component smoke tests so each platform executes its own toolchain. Keep per-platform validation caches, bypass external caches on releases, and retain registry layer reuse from shared Dockerfile stages. [Tarantool Docker build](https://github.com/tarantool/tarantool/blob/18aa11bac1eff135199efa6482dbdb4f0145ffbd/.github/workflows/docker_build_push.yml#L10-L94) · [Server Dockerfile](https://github.com/tarantool/tarantool/blob/18aa11bac1eff135199efa6482dbdb4f0145ffbd/docker/Dockerfile#L1-L54) · [tt Docker tests](https://github.com/tarantool/tt/blob/b3a4a5fc836ccb65889d3ab11d0cb5f5f750d83a/.github/workflows/docker.yml#L63-L120) |
| Registry credentials and scanning | Tarantool's Docker job authenticates to Docker Hub using repository secrets. The `tt` package workflow uses an Actions token, and its Trivy workflow separates database fetch from offline analysis. | Keep GHCR and the repository's short-lived Actions token. Scope package writes to release jobs, keep credentials out of Dockerfile build arguments and test containers, and scan with networking disabled. [Docker Hub login](https://github.com/tarantool/tarantool/blob/18aa11bac1eff135199efa6482dbdb4f0145ffbd/.github/workflows/docker_build_push.yml#L81-L94) · [tt package authentication](https://github.com/tarantool/tt/blob/b3a4a5fc836ccb65889d3ab11d0cb5f5f750d83a/.github/workflows/publish.yml#L83-L127) · [tt scan isolation](https://github.com/tarantool/tt/blob/b3a4a5fc836ccb65889d3ab11d0cb5f5f750d83a/.github/workflows/_trivy.yml#L90-L140) |
| Tags, promotion and rollback | Tarantool keeps patch-version tags and may update minor, major, and latest aliases in its Docker Hub push. Its Docker workflow publishes those tags directly. | Publish exact stable tags by promoting verified platform digests. Record the index digests in one release manifest so project locks can advance or revert to a specific artifact. [Tarantool tag policy](https://github.com/tarantool/tarantool/blob/18aa11bac1eff135199efa6482dbdb4f0145ffbd/docker/README.md#L157-L181) |

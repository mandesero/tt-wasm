#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 6 ]]; then
    echo "Usage: $0 IMAGE VERSION REVISION SOURCE TARGET VERIFIED_DIR" >&2
    exit 2
fi

image=$1
version=$2
revision=$3
source=$4
target=$5
verified_dir=$6

case "$target" in
    c|go|js|python|rust) architectures=(amd64 arm64) ;;
    csharp) architectures=(amd64) ;;
    *) echo "Unknown builder target: $target" >&2; exit 2 ;;
esac

tag="$image:$version"
platforms=$(printf '%s\n' "${architectures[@]}" | jq -Rsc 'split("\n")[:-1]')

index_platforms() {
    jq -ec --argjson expected "$1" '
        if any(.manifests[]; .platform.os != "linux" and
            (.platform != {os: "unknown", architecture: "unknown"} or
             .annotations["vnd.docker.reference.type"] != "attestation-manifest"))
        then error("Unexpected non-Linux image descriptor")
        else [.manifests[] | select(.platform.os == "linux")] | sort_by(.platform.architecture)
        end |
        if map(.platform.architecture) != $expected or
            any(.[]; (.digest | test("^sha256:[a-f0-9]{64}$") | not))
        then error("Unexpected image platforms")
        else map({key: ("linux/" + .platform.architecture), value: .digest}) | from_entries
        end
    '
}

lookup_tag() {
    docker buildx imagetools inspect "$tag" --format '{{json .Manifest}}'
}

inspect_index() {
    docker buildx imagetools inspect --raw "$1"
}

expected_subjects='{}'
if descriptor=$(lookup_tag 2>&1); then
    echo "Checking existing release tag $tag" >&2
elif grep -qiE 'manifest unknown|name unknown|: not found$' <<< "$descriptor"; then
    references=()
    for architecture in "${architectures[@]}"; do
        reference=$(< "$verified_dir/$architecture.txt")
        if [[ "$reference" != "$image@sha256:"* || ! "${reference##*:}" =~ ^[a-f0-9]{64}$ ]]; then
            echo "Invalid verified digest for $architecture" >&2
            exit 1
        fi
        expected=$(jq -cn --arg architecture "$architecture" '[$architecture]')
        subjects=$(inspect_index "$reference" | index_platforms "$expected")
        expected_subjects=$(jq -cn --argjson a "$expected_subjects" --argjson b "$subjects" '$a + $b')
        references+=("$reference")
    done

    docker buildx imagetools create --tag "$tag" \
        --annotation "index:org.opencontainers.image.source=$source" \
        --annotation "index:org.opencontainers.image.revision=$revision" \
        --annotation "index:org.opencontainers.image.version=$version" \
        "${references[@]}" >&2
    descriptor=$(lookup_tag)
else
    printf '%s\n' "$descriptor" >&2
    exit 1
fi

digest=$(jq -er '.digest | select(test("^sha256:[a-f0-9]{64}$"))' <<< "$descriptor")
reference="$image@$digest"
index=$(inspect_index "$reference")
jq -e --arg source "$source" --arg revision "$revision" --arg version "$version" '
    .mediaType == "application/vnd.oci.image.index.v1+json" and
    .annotations["org.opencontainers.image.source"] == $source and
    .annotations["org.opencontainers.image.revision"] == $revision and
    .annotations["org.opencontainers.image.version"] == $version
' <<< "$index" >/dev/null || {
    echo "Release tag $tag belongs to a different source, revision, or version, or has an invalid index." >&2
    exit 1
}
subjects=$(index_platforms "$platforms" <<< "$index")
if [[ "$expected_subjects" != '{}' && "$subjects" != "$expected_subjects" ]]; then
    echo "Published index contains unexpected image digests." >&2
    exit 1
fi

jq -n --arg target "$target" --arg tag "$tag" --arg reference "$reference" \
    --arg version "$version" --arg revision "$revision" --argjson platforms "$subjects" \
    '{$target, $tag, $reference, $version, $revision, $platforms}'

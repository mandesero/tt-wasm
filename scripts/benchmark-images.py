#!/usr/bin/env python3
"""Record build and deployment measurements for the Wasm builder images."""

import argparse
import collections
import json
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import time


def command(args, check=True):
    started = time.monotonic()
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    elapsed = round(time.monotonic() - started, 3)
    if check and result.returncode:
        raise RuntimeError(f"{' '.join(args)} failed:\n{result.stderr}")
    return result, elapsed


def build(args):
    measurements = []
    for target in args.targets:
        image = f"{args.image_prefix}:{target}"
        cmd = [
            "docker", "buildx", "build", "--load", "--progress=plain",
            "--platform", args.platform, "--target", target, "--tag", image,
            "--build-context", f"tt-sdk={Path(args.sdk).resolve()}",
        ]
        if args.builder:
            cmd += ["--builder", args.builder]
        if args.no_cache:
            cmd += ["--no-cache", "--pull"]
        cmd.append(args.source)
        started = time.monotonic()
        result = subprocess.run(cmd, stdout=sys.stderr, stderr=sys.stderr)
        measurements.append({
            "target": target,
            "image": image,
            "build_seconds": round(time.monotonic() - started, 3),
            "exit_code": result.returncode,
        })
    return {"platform": args.platform, "builds": measurements}


def manifest_layers(image, platform):
    """Resolve the Linux platform manifest and report its compressed blobs."""
    args = ["docker", "buildx", "imagetools", "inspect", "--raw"]
    result, _ = command([*args, image])
    manifest = json.loads(result.stdout)
    if "manifests" in manifest:
        selected = next(
            entry for entry in manifest["manifests"]
            if platform == f"{entry.get('platform', {}).get('os')}/{entry.get('platform', {}).get('architecture')}"
        )
        repo = image.split("@", 1)[0]
        if ":" in repo.rsplit("/", 1)[-1]:
            repo = repo.rsplit(":", 1)[0]
        result, _ = command([*args, f"{repo}@{selected['digest']}"])
        manifest = json.loads(result.stdout)
    return {layer["digest"]: layer["size"] for layer in manifest["layers"]}


def measure(args):
    observations = []
    unique_layers = {}
    layer_use = collections.Counter()
    for pair in args.images:
        target, image = pair.split("=", 1)
        data = {"target": target, "image": image}
        if args.pull:
            present, _ = command(["docker", "image", "inspect", image], check=False)
            _, data["pull_seconds"] = command(["docker", "pull", "--platform", args.platform, image])
            data["image_present_before_pull"] = present.returncode == 0
        inspect, _ = command(["docker", "image", "inspect", image])
        image_info = json.loads(inspect.stdout)[0]
        actual = f"{image_info['Os']}/{image_info['Architecture']}"
        if actual != args.platform:
            raise RuntimeError(f"{image} is {actual}, expected {args.platform}")
        data["uncompressed_bytes"] = image_info["Size"]
        data["uncompressed_layer_count"] = len(image_info["RootFS"]["Layers"])
        if args.registry:
            blobs = manifest_layers(image, args.platform)
            unique_layers.update(blobs)
            layer_use.update(blobs.keys())
            data["compressed_layer_bytes"] = sum(blobs.values())
            data["compressed_layer_digests"] = list(blobs)
        run = ["docker", "run", "--rm", "--pull=never", "--network=none"]
        starts = [
            command([*run, "--entrypoint", "/bin/true", image])[1] for _ in range(args.samples)
        ]
        data["startup_seconds_median"] = statistics.median(starts)
        data["startup_seconds_samples"] = starts
        health, elapsed = command([*run, image, "check", target], check=False)
        data["healthcheck_seconds"] = elapsed
        data["healthcheck_exit_code"] = health.returncode
        if health.returncode:
            data["healthcheck_error"] = health.stderr.strip()
        if args.smoke and not health.returncode:
            env = dict(os.environ, TT_WASM_CONTAINER_METRICS="1")
            script = Path(__file__).with_name("container-smoke.sh")
            started = time.monotonic()
            result = subprocess.run([script, target, image], env=env, text=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            data["smoke_seconds"] = round(time.monotonic() - started, 3)
            data["smoke_exit_code"] = result.returncode
            data["resource_usage"] = {
                phase: {"memory_peak_bytes": int(memory), "cpu_usage_usec": int(cpu)}
                for phase, memory, cpu in re.findall(
                    r"^phase=(\w+) memory_peak_bytes=(\d+) cpu_usage_usec=(\d+)$",
                    result.stdout, flags=re.MULTILINE,
                )
            }
            if result.returncode:
                data["smoke_error"] = result.stdout.strip()
        observations.append(data)
    result = {"platform": args.platform, "images": observations}
    if args.registry:
        result["layer_sharing"] = {
            "compressed_bytes_without_deduplication": sum(
                data["compressed_layer_bytes"] for data in observations
            ),
            "compressed_unique_bytes": sum(unique_layers.values()),
            "unique_layers": len(unique_layers),
            "layers_present_in_multiple_images": sum(count > 1 for count in layer_use.values()),
        }
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(required=True)
    build_parser = subparsers.add_parser("build")
    build_parser.add_argument("--source", default=".")
    build_parser.add_argument("--sdk", required=True)
    build_parser.add_argument("--builder")
    build_parser.add_argument("--image-prefix", required=True)
    build_parser.add_argument("--no-cache", action="store_true")
    build_parser.add_argument("targets", nargs="+")
    build_parser.set_defaults(run=build)
    measure_parser = subparsers.add_parser("measure")
    measure_parser.add_argument("--registry", action="store_true", help="Read compressed layer sizes from the registry")
    measure_parser.add_argument("--pull", action="store_true", help="Time a Docker pull using the current daemon")
    measure_parser.add_argument("--smoke", action="store_true", help="Measure the fetch and build cgroups")
    measure_parser.add_argument("--samples", type=int, default=5)
    measure_parser.add_argument("images", metavar="TARGET=IMAGE", nargs="+")
    measure_parser.set_defaults(run=measure)
    for subparser in (build_parser, measure_parser):
        subparser.add_argument("--platform", default="linux/amd64")
    args = parser.parse_args()
    if getattr(args, "samples", 1) < 1:
        parser.error("--samples must be positive")
    if any("=" not in pair for pair in getattr(args, "images", ())):
        parser.error("provide image references as TARGET=IMAGE")
    print(json.dumps(args.run(args), indent=2))


if __name__ == "__main__":
    main()

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	rustLanguage       = "rust"
	cargoTool          = "cargo"
	componentCargoTool = "cargo-component"
	rustcTool          = "rustc"
	rustTarget         = "wasm32-wasip1"
	rustSDK            = "tarantool-sdk"
	rustEditorSDK      = ".tt-wasm/bindings/" + rustSDK
	rustLegacySDK      = ".tt-wasm-sdk-rust"
	rustCache          = "dependencies/cargo"
	rustSDKCache       = "dependencies/rust-sdk"
	rustLock           = "Cargo.lock"
	rustComponentLock  = "Cargo-component.lock"
)

var (
	errRustSDK    = errors.New("expected one Tarantool Rust SDK crate")
	errRustTarget = errors.New("select the component's WIT, world and cdylib source in Cargo.toml")
	errRustWITDep = errors.New("use local paths for Cargo component dependencies")
	errRustLock   = errors.New("cargo component dependencies differ from Cargo-component.lock")
)

func rustDriver() languageDriver {
	return languageDriver{
		fetch:   fetchRust,
		bind:    bindRust,
		build:   buildRust,
		check:   checkRust,
		help:    rustHelp,
		managed: rustManaged,
		locks:   []string{"Cargo.toml", rustLock, rustComponentLock},
		cache:   []string{rustCache, rustSDKCache},
	}
}

func checkRust(ctx context.Context) error {
	err := checkTools(ctx, toolVersion{
		name: componentCargoTool, version: "0.21.1", args: []string{versionOption},
	})
	if err != nil {
		return err
	}

	for _, name := range []string{rustcTool, cargoTool} {
		output, err := checkOutput(ctx, name, versionOption)
		if err != nil {
			return fmt.Errorf("checking %s: %w", name, err)
		}

		version := versionNumber.FindString(string(output))
		major, remaining, _ := strings.Cut(version, ".")

		minor, _, _ := strings.Cut(remaining, ".")
		minorVersion, _ := strconv.Atoi(minor)

		if major != "1" || minorVersion < 96 {
			return fmt.Errorf("%w: %s 1.96 or newer required, got %s", errTool, name, version)
		}
	}

	libdir, err := checkOutput(ctx, rustcTool, "--print", "target-libdir", targetOption, rustTarget)
	if err != nil {
		return fmt.Errorf("checking %s target: %w", rustTarget, err)
	}

	info, err := os.Stat(strings.TrimSpace(string(libdir)))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: install the Rust %s target", errTool, rustTarget)
	}

	return nil
}

func rustHelp(operation string) []string {
	switch operation {
	case Bind:
		return []string{cargoTool, componentCommand, bindingsCommand, helpOption}
	case Build:
		// cargo-component inspects the manifest before passing build help
		// through, so invoke Cargo's help directly.
		return []string{cargoTool, Build, helpOption}
	default:
		return []string{wkg, Fetch, helpOption}
	}
}

func rustManaged(operation string) []string {
	switch operation {
	case Bind:
		return []string{"--cache-dir", configOption}
	case Build:
		return []string{
			"--cache-dir", configOption, "--manifest-path", "--workspace", "--exclude",
			"--package", "-p", "--lib", "--bin", "--bins", "--example", "--examples",
			"--test", "--tests", "--bench", "--benches", "--all-targets", targetOption,
			"--target-dir", "--artifact-dir", outDirOption, "--release", "-r", "--profile",
			"--locked", "--offline", "--frozen", "--message-format",
		}
	default:
		return nil
	}
}

func fetchRust(ctx context.Context, run executor, req Request) error {
	err := fetchWIT(ctx, run, req, filepath.Join(req.Cache, dependenciesDir))
	if err != nil {
		return err
	}

	sdk := filepath.Join(req.Cache, sdkDir, "packages", rustLanguage)
	archives, err := fs.Glob(os.DirFS(sdk), "tarantool-wasm-*.crate")

	if err != nil || len(archives) != 1 {
		return fmt.Errorf("%w in %s", errRustSDK, sdk)
	}

	err = unpackSDKArchive(filepath.Join(sdk, archives[0]), filepath.Join(req.Cache, rustSDKCache))
	if err != nil {
		return fmt.Errorf("unpacking the Rust SDK: %w", err)
	}

	run, err = stageRust(run, req)
	if err != nil {
		return err
	}

	_, err = rustArtifact(ctx, run, req)
	if err != nil {
		return err
	}

	return run.command(ctx, cargoTool, Fetch, "--locked", targetOption, rustTarget)
}

func bindRust(ctx context.Context, run executor, req Request) error {
	run, err := stageRust(run, req)
	if err != nil {
		return err
	}

	_, err = rustArtifact(ctx, run, req)
	if err != nil {
		return err
	}

	lockPath := filepath.Join(run.dir, rustComponentLock)

	before, err := readRustLock(lockPath)
	if err != nil {
		return err
	}

	args := append([]string{componentCommand, bindingsCommand}, req.Args...)

	err = run.command(ctx, cargoTool, args...)
	if err != nil {
		return err
	}

	after, err := readRustLock(lockPath)
	if err != nil {
		return err
	}

	if !bytes.Equal(before, after) {
		return errRustLock
	}

	bindings := filepath.Join(req.Work, BindingsDir)

	err = os.MkdirAll(bindings, dirPerm)
	if err == nil {
		err = os.Rename(filepath.Join(run.dir, "src", "bindings.rs"),
			filepath.Join(bindings, "bindings.rs"))
	}

	if err == nil {
		err = os.Rename(filepath.Join(run.dir, rustEditorSDK), filepath.Join(bindings, rustSDK))
	}
	if err != nil {
		return fmt.Errorf("collecting Rust bindings: %w", err)
	}

	return nil
}

func buildRust(ctx context.Context, run executor, req Request) error {
	run, err := stageRust(run, req)
	if err != nil {
		return err
	}

	artifact, err := rustArtifact(ctx, run, req)
	if err != nil {
		return err
	}

	args := append([]string{
		componentCommand, Build, "--lib", "--release", "--locked", "--offline",
		targetOption, rustTarget,
	}, req.Args...)

	err = run.command(ctx, cargoTool, args...)
	if err != nil {
		return err
	}

	binary := filepath.Join(req.Work, "target", rustTarget, "release", artifact+".wasm")
	output := filepath.Join(req.Work, OutputFile)

	err = os.Rename(binary, output)
	if err != nil {
		return fmt.Errorf("collecting Rust component: %w", err)
	}

	return validateComponent(ctx, run, output)
}

func stageRust(run executor, req Request) (executor, error) {
	_, err := projectPath(req.Dir, rustLock)
	if err != nil {
		return run, fmt.Errorf("checking locked Cargo dependencies: %w", err)
	}

	work, err := filepath.EvalSymlinks(req.Work)
	if err != nil {
		return run, fmt.Errorf("resolving the Rust working directory: %w", err)
	}

	stage := filepath.Join(work, "rust")

	err = stageProject(req.Dir, stage, req.Source, rustLegacySDK)
	if err != nil {
		return run, err
	}

	err = stageRustSDK(stage, filepath.Join(req.Cache, rustSDKCache))
	if err != nil {
		return run, fmt.Errorf("staging the Rust SDK: %w", err)
	}

	cache := filepath.Join(req.Cache, rustCache)
	cargoHome := cache
	offline := req.Operation != Fetch

	if offline {
		err = stageRustWIT(req, stage)
		if err != nil {
			return run, err
		}

		cargoHome = filepath.Join(work, "cargo")
	}

	if req.Operation == Build {
		err = os.CopyFS(cargoHome, os.DirFS(cache))
	} else {
		err = os.MkdirAll(cargoHome, dirPerm)
	}

	if err != nil {
		return run, fmt.Errorf("preparing the Cargo cache: %w", err)
	}

	run.dir = stage
	run.env = append(run.env,
		"CARGO_HOME="+cargoHome,
		"CARGO_TARGET_DIR="+filepath.Join(req.Work, "target"),
		"CARGO_COMPONENT_CACHE_DIR="+filepath.Join(work, "component-cache"),
		"TMPDIR="+filepath.Join(work, "tmp"),
	)

	if offline {
		run.env = append(run.env, "CARGO_NET_OFFLINE=true")
	}

	return run, nil
}

// stageRustSDK supports both scaffold paths in disposable build trees.
func stageRustSDK(stage, source string) error {
	for _, path := range []string{rustEditorSDK, rustLegacySDK} {
		err := os.CopyFS(filepath.Join(stage, path), os.DirFS(source))
		if err != nil {
			return err
		}
	}

	return nil
}

func stageRustWIT(req Request, stage string) error {
	wit, err := projectPath(req.Dir, req.WIT)
	if err != nil {
		return err
	}

	info, err := os.Stat(wit)
	if err != nil {
		return fmt.Errorf("checking Rust WIT: %w", err)
	}

	dest := filepath.Join(stage, req.WIT)
	if !info.IsDir() {
		dest = filepath.Dir(dest)
	}

	dest = filepath.Join(dest, witDepsDir)

	err = os.RemoveAll(dest)
	if err == nil {
		source := filepath.Join(req.Cache, dependenciesDir, witDir)

		err = os.CopyFS(dest, os.DirFS(source))
	}

	if err != nil {
		return fmt.Errorf("staging Rust WIT dependencies: %w", err)
	}

	return nil
}

type rustComponentMetadata struct {
	Target struct {
		Path         string                       `json:"path"`
		World        string                       `json:"world"`
		Dependencies map[string]rustWITDependency `json:"dependencies"`
	} `json:"target"`
	Dependencies map[string]rustWITDependency `json:"dependencies"`
}

type rustWITDependency struct {
	Path string `json:"path"`
}

func rustArtifact(ctx context.Context, run executor, req Request) (string, error) {
	var output bytes.Buffer

	run.stdout = &output

	err := run.command(ctx, cargoTool, "metadata", "--locked", "--no-deps", "--format-version", "1")
	if err != nil {
		return "", err
	}

	var metadata struct {
		Packages []struct {
			Manifest string `json:"manifest_path"`
			Metadata struct {
				Component rustComponentMetadata `json:"component"`
			} `json:"metadata"`
			Targets []struct {
				Name   string   `json:"name"`
				Kind   []string `json:"kind"`
				Source string   `json:"src_path"`
			} `json:"targets"`
		} `json:"packages"`
	}

	err = json.Unmarshal(output.Bytes(), &metadata)
	if err != nil {
		return "", fmt.Errorf("reading Cargo metadata: %w", err)
	}

	for _, pkg := range metadata.Packages {
		if pkg.Manifest != filepath.Join(run.dir, "Cargo.toml") {
			continue
		}

		err = checkRustWIT(pkg.Metadata.Component, req)
		if err != nil {
			return "", err
		}

		for _, target := range pkg.Targets {
			if slices.Contains(target.Kind, "cdylib") &&
				target.Source == filepath.Join(run.dir, req.Source) {
				return target.Name, nil
			}
		}
	}

	return "", errRustTarget
}

func checkRustWIT(component rustComponentMetadata, req Request) error {
	target := component.Target
	if target.Path == "" || !filepath.IsLocal(target.Path) ||
		filepath.Clean(target.Path) != filepath.Clean(req.WIT) || target.World != req.World {
		return errRustTarget
	}

	groups := []map[string]rustWITDependency{target.Dependencies, component.Dependencies}

	for _, deps := range groups {
		for name, dep := range deps {
			if !filepath.IsLocal(dep.Path) {
				return fmt.Errorf("%w: %s", errRustWITDep, name)
			}
		}
	}

	return nil
}

func readRustLock(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return data, nil
}

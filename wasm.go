// Package wasm provides the tt commands and build backend for WebAssembly
// components. It depends on the public tt SDK. [New] is the constructor a
// distribution passes to the core.
package wasm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tarantool/go-luarocks/remote"

	"github.com/mandesero/tt-wasm/internal/component"
	"github.com/mandesero/tt-wasm/internal/component/driver"
	"github.com/mandesero/tt-wasm/internal/wasm"
	"github.com/tarantool/tt/sdk"
	"github.com/tarantool/tt/sdk/build"
)

// New provides the wasm commands, the wasm build backend, and the check of
// .wasm files any backend lays out.
func New(services sdk.Services) sdk.Module {
	return sdk.Module{
		Commands: []sdk.Mount{{Path: "", Cmd: newWasmCmd(services)}},
		Backends: []build.Backend{component.Backend()},
		FileChecks: []build.FileCheck{
			{Suffix: wasmSuffix, Check: checkWasmFile},
		},
	}
}

func newWasmCmd(services sdk.Services) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wasm",
		Short: "Build WebAssembly components for Tarantool",
		Long: "Prepare toolchains, create and build WebAssembly components, and pack " +
			"releases of the tarantool-wasm runtime for a rock server.",
	}

	cmd.AddCommand(newPrepareCmd(services), newNewCmd(services), newStatusCmd(services),
		newDoctorCmd(services), newOperationCmd(services, opFetch, "Fetch locked dependencies"),
		newOperationCmd(services, opBind, "Generate language bindings from WIT"),
		newOperationCmd(services, opBuild, "Build one WebAssembly component"),
		newCleanCmd(services), newRockCmd(services))

	return cmd
}

// prepareCmd holds the flags of tt wasm prepare.
type prepareCmd struct {
	services   sdk.Services
	options    component.PrepareOptions
	from       string
	rockServer string
	registries []string
}

func newPrepareCmd(services sdk.Services) *cobra.Command {
	prepare := &prepareCmd{
		services: services,
		options: component.PrepareOptions{
			Version: "", Language: "", Runner: "", Platform: "", Context: "", Image: "",
			Update: false,
		},
		from: "", rockServer: "", registries: nil,
	}

	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Prepare the runtime dependency and WebAssembly toolchains",
		Long: "Declare the tarantool-wasm runtime dependency if it is absent, resolve " +
			"the package lock, and prepare the selected language or the package's " +
			"WebAssembly components. --rock-server changes the registry of an existing " +
			"runtime declaration. Docker is the default. In local mode the tools " +
			"must already be installed and available on PATH.",
		Args: cobra.NoArgs,
		RunE: prepare.run,
	}

	flags := cmd.Flags()
	flags.StringVar(&prepare.options.Version, "version", "", "tarantool-wasm runtime version")
	flags.StringVar(&prepare.options.Language, "lang", "",
		"the language to prepare: "+strings.Join(driver.Languages(), ", "))
	flags.StringVar(&prepare.options.Runner, "runner", "", "toolchain runner: local or docker")
	flags.StringVar(&prepare.options.Platform, "platform", "", "Docker builder platform")
	flags.StringVar(&prepare.options.Context, "context", "", "Docker context")
	flags.StringVar(&prepare.options.Image, "image", "",
		"locally built Docker image for development")
	flags.BoolVar(&prepare.options.Update, "update", false, "update locked WebAssembly profiles")
	flags.StringVar(&prepare.from, "from", "",
		"pack this local runtime build into "+wasm.RegistryDir)
	flags.StringVar(&prepare.rockServer, "rock-server", "",
		"rock server to use for the wasm runtime")
	flags.StringArrayVar(&prepare.registries, "registry", nil, sdk.RegistryFlagUsage)

	cmd.MarkFlagsMutuallyExclusive("from", "rock-server")

	return cmd
}

func (c *prepareCmd) run(cmd *cobra.Command, _ []string) error {
	project := c.services.Project()
	streams := c.services.Streams().IO()

	ctx, release, err := project.Lock(cmd.Context())
	if err != nil {
		return err
	}
	defer release()

	projectDir, err := project.Root()
	if err != nil {
		return err
	}

	man, err := readManifest(project, projectDir)
	if err != nil {
		return err
	}

	opts := c.options
	version := wasm.DeclaredVersion(man)

	if opts.Version != "" && version != "" && opts.Version != version &&
		opts.Version != runtimeVersion(version) {
		return sdk.Errorf(sdk.ExitFailure, "the package declares wasm %s, not %s",
			version, opts.Version)
	}

	if opts.Version != "" {
		version = opts.Version
	}

	dependency, declared := man.Dependencies[wasm.RockName]
	keepRegistry := declared &&
		(c.rockServer == "" || c.rockServer == dependency.Registry) &&
		(c.from == "" || dependency.Registry == wasm.RegistryDir)
	lock, lockErr := project.ReadLock()

	report, err := wasm.Prepare(ctx, wasm.Options{
		ProjectDir: projectDir, Version: version, From: c.from, RockServer: c.rockServer,
		Rocks: c.services.Rocks(),
		Declare: func(ctx context.Context, name, constraint, registry string) (bool, error) {
			if keepRegistry {
				if lockErr == nil && lock.Fresh && len(c.registries) == 0 && c.from == "" {
					return true, nil
				}

				change, err := project.Resolve(ctx, sdk.ResolveOptions{Registries: c.registries})
				if err != nil {
					return true, err
				}

				printMoves(streams.Out, change.Moves)

				return true, nil
			}

			if declared {
				constraint = dependency.Version
			}

			change, err := project.AddDependency(ctx, sdk.Dependency{
				Name: name, Constraint: constraint, Registry: registry, Dev: false,
				Registries: c.registries,
			})
			if err != nil {
				return false, err
			}

			printMoves(streams.Out, change.Moves)

			return false, nil
		},
		PrepareEnv: project.PrepareEnvironment,
		Progress:   streams.Err,
	})
	if err != nil {
		return err
	}

	if keepRegistry && c.from == "" {
		report.Registry = dependency.Registry
		if report.Registry == "" {
			report.Registry = wasm.DefaultRockServer
		}
	}

	lock, lockErr = project.ReadLock()
	if rock, ok := lock.Locked(wasm.RockName); lockErr == nil && ok {
		releaseVersion := runtimeVersion(rock.Version)
		if opts.Version != "" && opts.Version != rock.Version && opts.Version != releaseVersion {
			return sdk.Errorf(sdk.ExitFailure, "the package locks wasm %s, not %s",
				rock.Version, opts.Version)
		}

		opts.Version = rock.Version
		report.Release.Version = releaseVersion
	}

	if opts.Version == "" {
		opts.Version = report.Release.Version
	}

	err = component.Prepare(ctx, project, opts, streams.Out, streams.Err)
	if err != nil {
		return err
	}

	err = ignoreLocalSettings(projectDir)
	if err != nil {
		return fmt.Errorf("ignoring WebAssembly local settings: %w", err)
	}

	printPrepared(streams.Out, projectDir, report)

	return nil
}

// runtimeVersion removes the revision of a Lua rock from an exact version.
func runtimeVersion(version string) string {
	release, revision, found := strings.CutLast(version, "-")
	if !found {
		return version
	}

	_, err := strconv.ParseUint(revision, 10, 32)
	if err != nil {
		return version
	}

	return release
}

// ignoreLocalSettings keeps the machine's runner and Docker paths out of git.
// A runtime-only preparation has no local settings file to ignore.
func ignoreLocalSettings(projectDir string) error {
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	const localSettings = "wasm.local.json"

	_, err = root.Stat(localSettings)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	ignore, err := root.OpenFile(".gitignore", os.O_RDWR|os.O_CREATE|os.O_APPEND, scaffoldFileMode)
	if err != nil {
		return err
	}

	defer func() { _ = ignore.Close() }()

	data, err := io.ReadAll(ignore)
	if err != nil {
		return err
	}

	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == localSettings || line == "/"+localSettings {
			return nil
		}
	}

	line := "/" + localSettings + "\n"
	if len(data) > 0 && data[len(data)-1] != '\n' {
		line = "\n" + line
	}

	_, err = io.WriteString(ignore, line)
	if err != nil {
		return fmt.Errorf("appending to .gitignore: %w", err)
	}

	return nil
}

// rockCmd is tt wasm rock: the values of its flags.
type rockCmd struct {
	services sdk.Services
	// version is the tarantool-wasm release to pack.
	version string
	// out is the directory the rocks are packed into.
	out string
	// from is a local build of the module to pack.
	from string
	// arch is the LuaRocks arch of the build from names.
	arch string
}

// newRockCmd builds `tt wasm rock`.
func newRockCmd(services sdk.Services) *cobra.Command {
	rock := &rockCmd{services: services, version: "", out: "", from: "", arch: ""}

	rockCmd := &cobra.Command{
		Use:   "rock",
		Short: "Pack the rocks of a release for a rock server",
		Long: "Pack the wasm rock of every platform of a tarantool-wasm release into " +
			"--out, with the LuaRocks manifest and the rock index that make the " +
			"directory a rock server, the index pinning the sha256 of every rock " +
			"in the lock of a project that depends on it. Serve it, " +
			"or upload its files next to the assets of the release, and declare the " +
			"dependency from it with tt wasm prepare --rock-server: then every machine " +
			"gets the build of its own, and an archive packed --without-deps installs " +
			"anywhere. The assets are checked against the sha256 tt holds the release to. " +
			"With --from, pack the one local build of the module as the rock of --arch " +
			"(default this host's) and keep the rocks already in --out: run it once for " +
			"the build of each platform.",
		Example: `# The rocks of 0.1.0, for the assets of the release on GitHub.
  $ tt wasm rock --out dist/rocks
  $ gh release upload v0.1.0 dist/rocks/*
  $ tt wasm prepare --rock-server \
      https://github.com/tarantool/tarantool-wasm-rs/releases/download/v0.1.0/

  # Local builds, one per platform.
  $ tt wasm rock --from target/release/libtarantool_wasm_rs.so \
      --out dist/rocks
  $ tt wasm rock --arch macosx-aarch64 --out dist/rocks \
      --from target/release/libtarantool_wasm_rs.dylib`,
		Args: cobra.NoArgs,
		RunE: rock.run,
	}

	flags := rockCmd.Flags()
	flags.StringVar(&rock.version, "version", wasm.DefaultVersion, "tarantool-wasm release")
	flags.StringVar(&rock.out, "out", "", "the directory to pack the rocks into")
	flags.StringVar(&rock.from, "from", "",
		"pack this local build of the module instead of downloading the assets")
	flags.StringVar(&rock.arch, "arch", "",
		"the LuaRocks arch of the build --from names (linux-x86_64, macosx-aarch64; "+
			"default this host's)")

	_ = rockCmd.MarkFlagRequired("out")

	return rockCmd
}

// run packs the rocks of a release and prints their paths and the paths of
// the manifest and the rock index of the rock server.
func (c *rockCmd) run(cmd *cobra.Command, _ []string) error {
	streams := c.services.Streams().IO()

	rocks, err := wasm.PackRocks(cmd.Context(), wasm.PackOptions{
		Dir:      c.out,
		Version:  c.version,
		Fetcher:  newFetcher(),
		Rocks:    c.services.Rocks(),
		Progress: streams.Err,
		From:     c.from,
		Arch:     c.arch,
	})
	if err != nil {
		return err
	}

	for _, rock := range rocks {
		_, _ = fmt.Fprintln(streams.Out, rock)
	}

	_, _ = fmt.Fprintln(streams.Out, filepath.Join(c.out, "manifest"))
	_, _ = fmt.Fprintln(streams.Out, filepath.Join(c.out, remote.RocksIndexFile))

	return nil
}

// newFetcher returns the fetcher of the assets of tarantool-wasm and of the
// tools: the cache in <user cache>/tt/wasm and the default HTTP client.
func newFetcher() wasm.Fetcher {
	return wasm.Fetcher{CacheDir: wasm.DefaultCacheDir(), Client: nil}
}

// readManifest gives a missing manifest the same diagnostic as prepare.
func readManifest(project sdk.Project, projectDir string) (sdk.Manifest, error) {
	man, err := project.Manifest()
	if errors.Is(err, sdk.ErrNotFound) {
		return sdk.Manifest{}, wasm.NoManifestError(projectDir)
	}

	return man, err
}

func printMoves(out io.Writer, moves []sdk.DependencyMove) {
	for _, move := range moves {
		_, _ = fmt.Fprintf(out, "  %s\n", move)
	}
}

func printPrepared(out io.Writer, projectDir string, report *wasm.Report) {
	_, _ = fmt.Fprintf(out, "prepared for tarantool-wasm %s (%s)\n",
		report.Release.Version, report.Platform.RockArch)

	if report.Rock == "" {
		_, _ = fmt.Fprintf(out, "  rock:       from %s\n", report.Registry)

		return
	}

	path, err := filepath.Rel(projectDir, report.Rock)
	if err != nil {
		path = report.Rock
	}

	_, _ = fmt.Fprintf(out, "  rock:       %s\n", path)
}

package wasm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tarantool/tt/sdk"
	"github.com/tarantool/tt/sdk/scope"
)

const (
	// manifestFileName marks a tt project.
	manifestFileName = "app.manifest.toml"
	// RegistryDir is the rock server prepare writes the wasm rock of a local
	// build to, relative to the project: under .tarantool, which belongs to
	// the machine rather than the repository, since the rock is the build for
	// this host. The dependency names it so, and tt resolves it against the
	// project.
	RegistryDir = scope.ProjectDirName + "/registry"
	// DefaultRockServer is the rock server prepare declares the wasm rock
	// from unless it is told another or packs a local build: the
	// tt-rocks-index.json of the index branch of tarantool-contrib/rocks-repo,
	// which names the rocks of every platform on the release pages of the
	// repository with their sha256, so the lock pins them all. tt emmylua
	// prepare declares its rock from the same index.
	DefaultRockServer = "https://raw.githubusercontent.com/tarantool-contrib/rocks-repo/index/"
)

// Options configure Prepare.
type Options struct {
	ProjectDir string
	Version    string
	// From is a local native build to pack into the registry of the project.
	From       string
	RockServer string
	Rocks      sdk.Rocks
	// Declare records the runtime rock and reports whether its declaration was kept.
	Declare    func(ctx context.Context, name, constraint, registry string) (bool, error)
	PrepareEnv func() error
	Progress   io.Writer
}

// Report describes the runtime rock declared for the project.
type Report struct {
	Release  Release
	Platform Platform
	Rock     string
	Registry string
	Declared bool
}

// Prepare declares the runtime rock of the project and prepares the project's
// environment. With From, the rock is packed as the local platform's build.
func Prepare(ctx context.Context, opts Options) (*Report, error) {
	_, err := os.Stat(filepath.Join(opts.ProjectDir, manifestFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, NoManifestError(opts.ProjectDir)
	}

	if err != nil {
		return nil, fmt.Errorf("reading the project: %w", err)
	}

	release, platform, err := preparedRelease(opts)
	if err != nil {
		return nil, err
	}

	report := &Report{
		Release: release, Platform: platform, Rock: "", Registry: opts.RockServer, Declared: false,
	}

	err = prepareRock(ctx, opts, report)
	if err != nil {
		return nil, err
	}

	if opts.PrepareEnv != nil {
		err = opts.PrepareEnv()
		if err != nil {
			return nil, fmt.Errorf("writing .env/activate.sh: %w", err)
		}
	}

	return report, nil
}

// preparedRelease returns the release and the platform Prepare prepares the
// project for. A local build is packed from the release tt knows, as the rock
// of this host; a rock server knows its own releases, so any version is
// taken, and the arch of this host is all there is to say of the platform.
func preparedRelease(opts Options) (Release, Platform, error) {
	version := opts.Version
	if version == "" {
		version = DefaultVersion
	}

	if opts.From == "" {
		release := Release{
			Version: version, RockRevision: 0, SDKAsset: "", SDKMD5: "", SDKSHA256: "",
			Platforms: nil, BaseURL: "",
		}
		platform := Platform{
			GOOS: "", GOARCH: "", Asset: "", SHA256: "", RockArch: hostRockArch(), Module: "",
		}

		return release, platform, nil
	}

	release, err := LookupRelease(version)
	if err != nil {
		return Release{}, Platform{}, err
	}

	platform, err := LocalPlatform("")
	if err != nil {
		return Release{}, Platform{}, err
	}

	return release, platform, nil
}

// prepareRock does steps 1 and 2 of Prepare.
func prepareRock(ctx context.Context, opts Options, report *Report) error {
	switch {
	case opts.From != "":
		err := packLocalBuild(ctx, opts, report)
		if err != nil {
			return err
		}
	case report.Registry == "":
		report.Registry = DefaultRockServer
	}

	if opts.Declare == nil {
		return nil
	}

	kept, err := opts.Declare(ctx, RockName, report.Release.Version, report.Registry)
	if err != nil {
		return fmt.Errorf("declaring the %s dependency: %w", RockName, err)
	}

	report.Declared = !kept

	if kept {
		progressf(opts, "%s is already declared in %s; left as it is", RockName, manifestFileName)
	} else {
		progressf(opts, "declared %s %s from %s", RockName, report.Release.Version,
			report.Registry)
	}

	return nil
}

// packLocalBuild does step 1 of Prepare: it packs the module opts.From names
// as the rock of the release and the platform of report into the project's
// registry, indexes it, and has report declare the rock from there.
func packLocalBuild(ctx context.Context, opts Options, report *Report) error {
	progressf(opts, "reading the build in %s", opts.From)

	module, err := os.ReadFile(opts.From)
	if err != nil {
		return fmt.Errorf("reading the build: %w", err)
	}

	registry := filepath.Join(opts.ProjectDir, filepath.FromSlash(RegistryDir))

	rock, err := BuildRock(registry, report.Release, report.Platform, module)
	if err != nil {
		return err
	}

	// The registry of the project is a server of LuaRocks manifests: a rock
	// index would have the lock pin the digest of a rock packed anew from
	// every local build. One that is there is kept current, so it does not
	// hide the rock.
	err = indexRegistry(ctx, opts.Rocks, registry, true)
	if err != nil {
		return err
	}

	report.Rock = rock
	report.Registry = RegistryDir

	progressf(opts, "packed %s into %s", filepath.Base(rock), RegistryDir)

	return nil
}

// NoManifestError is the error of a project without a manifest in
// projectDir, as ReadManifest reports it.
func NoManifestError(projectDir string) error {
	return fmt.Errorf("%w: %s has no %s (create one with tt new)",
		ErrNoManifest, projectDir, manifestFileName)
}

// exactVersionRe matches a constraint that names one version: 0.1.0 or
// ==0.1.0.
var exactVersionRe = regexp.MustCompile(`^\s*(?:==\s*)?([0-9][0-9A-Za-z.\-]*)\s*$`)

// DeclaredVersion returns the version of the wasm rock man declares, when
// its constraint names one; empty otherwise.
func DeclaredVersion(man sdk.Manifest) string {
	dependency, ok := man.Dependencies[RockName]
	if !ok {
		return ""
	}

	match := exactVersionRe.FindStringSubmatch(dependency.Version)
	if match == nil {
		return ""
	}

	return match[1]
}

// progressf writes one line to opts.Progress.
func progressf(opts Options, format string, args ...any) {
	if opts.Progress == nil {
		return
	}

	_, _ = fmt.Fprintf(opts.Progress, format+"\n", args...)
}

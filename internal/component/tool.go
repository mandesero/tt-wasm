package component

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mandesero/tt-wasm/internal/component/driver"
	"github.com/mandesero/tt-wasm/internal/component/runner"
	"github.com/mandesero/tt-wasm/internal/wasm"
	"github.com/tarantool/tt/sdk"
	"github.com/tarantool/tt/sdk/build"
	"github.com/tarantool/tt/sdk/fetch"
)

const developmentDir = "dev"

var (
	errToolRunner    = errors.New("tt wasm tool requires the Docker runner; use --runner docker")
	errDevelopment   = errors.New("--dev requires a Python component with the Docker runner")
	errNoDevelopment = errors.New("Python development environment is missing; run tt wasm tool for this component")
	errDevPackages   = errors.New("invalid Python development package directory")
)

type developmentKey struct{}

// WithDevelopment uses the Python development environment for this build.
func WithDevelopment(ctx context.Context) context.Context {
	return context.WithValue(ctx, developmentKey{}, true)
}

func developmentRequested(ctx context.Context) bool {
	requested, _ := ctx.Value(developmentKey{}).(bool)

	return requested
}

// PrepareTool prepares any SDK files and Python environment needed by RunTool.
// The caller holds the project lock while it publishes generated state.
func (b *Build) PrepareTool(ctx context.Context, c *build.Context, stdout, stderr io.Writer) error {
	state, err := b.toolState(ctx, c)
	if err != nil {
		return err
	}

	dir := filepath.Join(c.Dirs.Component, stateName)

	err = os.MkdirAll(dir, stateMode)
	if err != nil {
		return fmt.Errorf("creating the Wasm component state: %w", err)
	}

	switch b.Language {
	case driver.Python:
		return b.preparePython(ctx, c, state, stdout, stderr)
	case driver.GoLanguage:
		return b.prepareToolSDK(ctx, c, "go.mod", stdout, stderr)
	case driver.Rust:
		return b.prepareToolSDK(ctx, c, "Cargo.toml", stdout, stderr)
	default:
		return nil
	}
}

// RunTool starts a command in the selected toolchain with the component as its
// working directory. It is safe to release the project lock before it runs.
func (b *Build) RunTool(
	ctx context.Context, c *build.Context, argv []string, in io.Reader, stdout, stderr io.Writer,
	tty bool,
) error {
	state, err := b.toolState(ctx, c)
	if err != nil {
		return err
	}

	tool := runner.Tool{
		Dir: c.Dirs.Component, State: filepath.Join(c.Dirs.Component, stateName),
		Language: b.Language, Argv: argv, TTY: tty,
	}

	if b.Language == driver.Python {
		tool.Development, err = state.developmentPath(c.Dirs.Component, c.Component.Name)
		if err != nil {
			return err
		}

		_, err = devPackages(tool.Development)
		if err != nil {
			return err
		}
	}

	err = runner.RunTool(ctx, state.settings, tool, in, stdout, stderr)

	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ExitCode() < 1 {
		return err
	}

	code := exit.ExitCode()
	coded := sdk.WithCode(code, err)
	if code >= 125 && code <= 127 {
		return coded // Docker uses these exit codes for launch failures.
	}

	return sdk.Reported(coded)
}

func (b *Build) toolState(ctx context.Context, c *build.Context) (componentState, error) {
	state, err := stateForBuild(ctx, c, &b.Config)
	if err != nil {
		return componentState{}, err
	}

	if state.settings.Mode != runner.Docker {
		return componentState{}, errToolRunner
	}

	version, pinned := c.LockedVersion(wasm.RockName)
	if !pinned {
		return componentState{}, errNoRuntime
	}

	return state, state.checkRuntime(version)
}

func (b *Build) prepareToolSDK(
	ctx context.Context, c *build.Context, manifest string, stdout, stderr io.Writer,
) error {
	state := filepath.Join(c.Dirs.Component, stateName)
	bindings := filepath.Join(state, bindingsName)
	sdkDir := filepath.Join(bindings, "tarantool-sdk")

	for _, path := range []string{bindings, sdkDir} {
		if err := stateDirectory(path); err != nil {
			return err
		}
	}

	info, err := os.Lstat(filepath.Join(sdkDir, manifest))
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", errGeneratedFile, filepath.Join(sdkDir, manifest))
		}

		return nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking the Wasm tool SDK: %w", err)
	}

	err = b.Operate(ctx, c, driver.Fetch, nil, false, stdout, stderr)
	if err != nil {
		return err
	}

	return b.Operate(ctx, c, driver.Bind, nil, false, stdout, stderr)
}

func (s componentState) developmentPath(dir, name string) (string, error) {
	parent := filepath.Join(dir, stateName, developmentDir)
	key := fetch.SHA256([]byte(name + "\n" + s.settings.Image + "\n" + s.settings.EffectivePlatform()))
	path := filepath.Join(parent, key)

	for _, dir := range []string{parent, path} {
		if err := stateDirectory(dir); err != nil {
			return "", err
		}
	}

	return path, nil
}

func (b *Build) preparePython(
	ctx context.Context, c *build.Context, state componentState, stdout, stderr io.Writer,
) error {
	path, err := state.developmentPath(c.Dirs.Component, c.Component.Name)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(path); err == nil {
		_, err = devPackages(path)

		return err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking the Python development environment: %w", err)
	}

	if !state.fetched() {
		err = b.Operate(ctx, c, driver.Fetch, nil, false, stdout, stderr)
		if err != nil {
			return err
		}
	}

	parent := filepath.Dir(path)

	err = os.MkdirAll(parent, stateMode)
	if err != nil {
		return fmt.Errorf("creating the Python development state: %w", err)
	}

	staged, err := os.MkdirTemp(parent, ".venv-")
	if err != nil {
		return fmt.Errorf("staging the Python development environment: %w", err)
	}

	defer func() { _ = os.RemoveAll(staged) }()

	seed := filepath.Join(state.cache, driver.PythonPackagesDir)

	err = runner.PreparePython(ctx, state.settings, staged, seed, stdout, stderr)
	if err != nil {
		return err
	}

	if _, err := devPackages(staged); err != nil {
		return err
	}

	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("publishing the Python development environment: %w", err)
	}

	return nil
}

// devPackages returns the host path to the venv's package directory. The venv
// itself always sees the same container path, even after staging or moving
// the project, so the scripts installed there retain valid interpreter paths.
func devPackages(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoDevelopment
	}

	if err != nil {
		return "", fmt.Errorf("opening the Python development environment: %w", err)
	}

	defer func() { _ = root.Close() }()

	data, err := root.ReadFile("site-packages")
	if err != nil {
		return "", fmt.Errorf("reading the Python development package path: %w", err)
	}

	packages := filepath.FromSlash(strings.TrimSpace(string(data)))
	if !filepath.IsLocal(packages) || filepath.Clean(packages) != packages ||
		!strings.HasPrefix(packages, "venv"+string(filepath.Separator)) {
		return "", errDevPackages
	}

	info, err := root.Stat(packages)
	if err != nil {
		return "", fmt.Errorf("opening the Python development package directory: %w", err)
	}

	if !info.IsDir() {
		return "", errDevPackages
	}

	return filepath.Join(dir, packages), nil
}

func (s componentState) developmentPackages(ctx context.Context, c *build.Context) (string, error) {
	if !developmentRequested(ctx) {
		return "", nil
	}

	if s.language != driver.Python || s.settings.Mode != runner.Docker {
		return "", errDevelopment
	}

	dir, err := s.developmentPath(c.Dirs.Component, c.Component.Name)
	if err != nil {
		return "", err
	}

	return devPackages(dir)
}

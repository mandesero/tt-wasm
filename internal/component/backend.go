// Package component builds WebAssembly components with the tt build SDK.
package component

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandesero/tt-wasm/internal/component/driver"
	"github.com/tarantool/tt/sdk/build"
)

// Name is the name of the WebAssembly component build backend.
const Name = "wasm"

var (
	errBadInput = errors.New("component input must be a regular file or directory")
	errBadPhase = errors.New("unknown wasm operation")
)

// Args holds the tool arguments of the three phases of a Wasm build.
type Args struct {
	Fetch []string `toml:"fetch"`
	Bind  []string `toml:"bind"`
	Build []string `toml:"build"`
}

// Config describes the backend-specific fields of a wasm build block.
type Config struct {
	Language string `toml:"language"`
	// Source and WIT are relative to the component directory.
	Source string `toml:"source"`
	WIT    string `toml:"wit"`
	// World is the world selected in the WIT package.
	World   string `toml:"world"`
	Profile string `toml:"profile"`
	Args    Args   `toml:"args"`
}

// Backend returns the Wasm component backend. Its output is a data file.
func Backend() build.Backend {
	return build.Backend{
		Name:        Name,
		Produces:    []build.Result{build.Data},
		Config:      func() any { return new(Config) },
		New:         build.Typed(newBuild),
		Incremental: false,
		OutputFrom:  build.OutputBackend,
		// The backend copies its own .wasm output into share.
		Exclude:        []string{"*.wasm"},
		NoSourceLayout: true,
		Hook:           false,
		// The wasm component has no native machine target of its own.
		Cross: true,
	}
}

// Build is a checked wasm build block.
type Build struct {
	Config

	// Output is the relative path of the component in its sources and in share.
	Output string
}

func newBuild(config *Config, _ build.Component, common build.Common) (build.Build, error) {
	switch {
	case strings.TrimSpace(config.Language) == "":
		return nil, build.FieldErrorf("language", "is required")
	case !filepath.IsLocal(config.Source):
		return nil, build.FieldErrorf("source", "must be a path within the component")
	case !filepath.IsLocal(config.WIT):
		return nil, build.FieldErrorf("wit", "must be a path within the component")
	case strings.TrimSpace(config.World) == "":
		return nil, build.FieldErrorf("world", "is required")
	case strings.TrimSpace(config.Profile) == "":
		return nil, build.FieldErrorf("profile", "is required")
	case len(common.Output) != 1 || common.Output[0].To != build.Data:
		return nil, build.FieldErrorf("output", "must name exactly one data file")
	case common.Cwd != "":
		return nil, build.FieldErrorf("cwd", "wasm builds run from the component directory")
	case len(common.Env) != 0:
		return nil, build.FieldErrorf("env", "is not supported by the wasm backend")
	}

	output := filepath.Clean(common.Output[0].Path)
	if !validOutput(output) {
		return nil, build.FieldErrorf("output", "must name a .wasm file within the component")
	}

	checked := *config

	checked.Source = filepath.Clean(checked.Source)
	checked.WIT = filepath.Clean(checked.WIT)

	if output == checked.Source || output == checked.WIT {
		return nil, build.FieldErrorf("output", "must differ from source and wit")
	}

	return &Build{Config: checked, Output: output}, nil
}

func validOutput(path string) bool {
	return filepath.IsLocal(path) && filepath.Ext(path) == ".wasm" &&
		!strings.HasPrefix(path, stateName+string(filepath.Separator))
}

// Preflight checks the component inputs and the prepared build environment.
func (b *Build) Preflight(ctx context.Context, c *build.Context) error {
	err := b.checkInputs(c.Dirs.Component)
	if err != nil {
		return err
	}

	return checkBuild(ctx, c, b)
}

// Run builds the component for tt package build and pack.
func (b *Build) Run(ctx context.Context, c *build.Context) error {
	var output bytes.Buffer

	err := b.Operate(ctx, c, driver.Build, c.Args, false, &output, &output)
	if err != nil && output.Len() > 0 {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(output.String()))
	}

	if c.Log != nil && output.Len() > 0 {
		c.Log.DebugContext(ctx, "wasm build output", "output", strings.TrimSpace(output.String()))
	}

	return err
}

// Operate runs a phase, adding the caller's args after those in the manifest.
// With help it runs only the phase's help, without the manifest arguments.
func (b *Build) Operate(
	ctx context.Context, c *build.Context, operation string, args []string, help bool,
	stdout, stderr io.Writer,
) error {
	var manifestArgs []string

	switch operation {
	case driver.Fetch:
		manifestArgs = b.Args.Fetch
	case driver.Bind:
		manifestArgs = b.Args.Bind
	case driver.Build:
		manifestArgs = b.Args.Build
	default:
		return fmt.Errorf("%w %q", errBadPhase, operation)
	}

	if !help {
		err := b.checkInputs(c.Dirs.Component)
		if err != nil {
			return err
		}

		args = slices.Concat(manifestArgs, args)
	}

	work, err := os.MkdirTemp(c.Dirs.Work, "wasm-*")
	if err != nil {
		return fmt.Errorf("creating a wasm work directory: %w", err)
	}

	defer func() { _ = os.RemoveAll(work) }()

	err = operate(ctx, c, b, operation, work, args, help, stdout, stderr)
	if err != nil || help || operation != driver.Build {
		return err
	}

	artifact := filepath.Join(work, driver.OutputFile)

	return publishOutput(c.Dirs, c.Component.Name, b.Output, artifact)
}

// CheckBindings verifies the bindings for this component.
func (b *Build) CheckBindings(ctx context.Context, c *build.Context) error {
	return checkBindings(ctx, c, b)
}

// Clean removes bindings and unchanged generated output from the source tree.
func (b *Build) Clean(_ context.Context, c *build.Context) error {
	err := cleanBindings(c)
	if err != nil {
		return err
	}

	return cleanOutputs(c.Dirs.Component, c.Component.Name)
}

func (b *Build) checkInputs(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("opening component directory: %w", err)
	}

	defer func() { _ = root.Close() }()

	for _, path := range []string{b.Source, b.WIT} {
		info, err := root.Stat(path)
		if err != nil {
			return fmt.Errorf("checking component input: %w", err)
		}

		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", errBadInput, path)
		}
	}

	return nil
}

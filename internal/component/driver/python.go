package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	dirPerm  = 0o750
	filePerm = 0o644

	dependenciesDir = "dependencies"
	sdkDir          = "sdk"
	witDir          = "wit"
	witDepsDir      = "deps"
	pythonSDKDir    = "python-sdk"
	sitePackagesDir = "site-packages"

	// PythonPackagesDir is the package directory in a prepared dependency cache.
	PythonPackagesDir = dependenciesDir + "/" + sitePackagesDir

	// Python/Tarantool 0.1.0 corrects a socket-errno mapping in the
	// pinned componentize-py after stripping the component.
	pythonSocketPatch = "twr_python_sdk/_guest/patch_wasi_socket_errors.py"
)

var (
	errMissingDeps  = errors.New("missing Wasm dependencies; run tt wasm fetch")
	errNotComponent = errors.New("build output is not a WebAssembly component")
)

func bindPython(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	args := append(witOptions(req, wit), req.Args...)

	args = append(args, bindingsCommand, filepath.Join(req.Work, BindingsDir))

	return run.command(ctx, componentize, args...)
}

func buildPython(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	source, err := projectPath(req.Dir, req.Source)
	if err != nil {
		return err
	}

	if filepath.Ext(source) != ".py" {
		return fmt.Errorf("%w: Python source %q must end in .py", errPath, req.Source)
	}

	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("checking Python source: %w", err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: Python source %q must be a file", errPath, req.Source)
	}

	deps := filepath.Join(req.Cache, dependenciesDir)
	sdk := filepath.Join(deps, pythonSDKDir)
	out := filepath.Join(req.Work, "raw.wasm")

	// componentize-py generates its own runtime bindings from this WIT.
	// The separate bindings operation produces stubs for the editor.
	args := append(witOptions(req, wit), req.Args...)

	args = append(args, componentizeCommand,
		"--python-path", filepath.Dir(source),
		"--python-path", sdk,
		"--python-path", filepath.Join(deps, sitePackagesDir),
		outputOption, out,
		"--", strings.TrimSuffix(filepath.Base(source), ".py"),
	)

	err = run.command(ctx, componentize, args...)
	if err != nil {
		return err
	}

	output := filepath.Join(req.Work, OutputFile)

	err = run.command(ctx, wasmTools, "strip", "--all", out, "-o", output)
	if err == nil {
		err = run.command(ctx, python, "-I", "-B", filepath.Join(sdk, pythonSocketPatch), output)
	}

	if err == nil {
		err = validateComponent(ctx, run, output)
	}

	return err
}

func witOptions(req Request, wit string) []string {
	return []string{"--wit-path=" + wit, "--world=" + req.World}
}

func validateComponent(ctx context.Context, run executor, output string) error {
	file, err := os.Open(output)
	if err != nil {
		return fmt.Errorf("reading build output: %w", err)
	}

	defer func() { _ = file.Close() }()

	const preamble = "\x00asm\x0d\x00\x01\x00"

	var header [len(preamble)]byte

	_, err = io.ReadFull(file, header[:])
	if err != nil || string(header[:]) != preamble {
		return fmt.Errorf("%w: %s", errNotComponent, output)
	}

	return run.command(ctx, wasmTools, "validate", output)
}

func buildWIT(req Request) (string, error) {
	deps := filepath.Join(req.Cache, dependenciesDir, witDir)

	_, err := os.Stat(deps)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", errMissingDeps, deps)
		}

		return "", fmt.Errorf("checking fetched WIT: %w", err)
	}

	_, err = stageWIT(req)
	if err != nil {
		return "", err
	}

	err = os.CopyFS(filepath.Join(req.Work, witDir, witDepsDir), os.DirFS(deps))
	if err != nil {
		return "", fmt.Errorf("copying fetched WIT: %w", err)
	}

	return filepath.Join(req.Work, witDir), nil
}

// stageWIT copies the selected WIT input into Work, where wkg may write a
// dependency closure. It leaves the user and SDK deps to the fetch step.
func stageWIT(req Request) (string, error) {
	source, err := projectPath(req.Dir, req.WIT)
	if err != nil {
		return "", err
	}

	target := filepath.Join(req.Work, witDir)

	err = os.MkdirAll(target, dirPerm)
	if err != nil {
		return "", fmt.Errorf("creating WIT directory: %w", err)
	}

	staged, err := os.OpenRoot(target)
	if err != nil {
		return "", fmt.Errorf("opening the staged WIT directory: %w", err)
	}

	defer func() { _ = staged.Close() }()

	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("checking WIT: %w", err)
	}

	if !info.IsDir() {
		err = copyWITFile(source, staged, filepath.Base(source))

		return filepath.Dir(source), err
	}

	entries, err := os.ReadDir(source)
	if err != nil {
		return "", fmt.Errorf("reading WIT directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".wit" {
			continue
		}

		input, err := projectPath(req.Dir, filepath.Join(req.WIT, name))
		if err != nil {
			return "", err
		}

		err = copyWITFile(input, staged, name)
		if err != nil {
			return "", err
		}
	}

	return source, nil
}

// projectPath resolves links before passing the path to a tool. Local
// builds and Docker builds both read the component's directory alone.
func projectPath(dir, name string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolving the component directory: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		return "", fmt.Errorf("resolving project path %q: %w", name, err)
	}

	relative, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("%w: %s leaves the component directory", errPath, name)
	}

	return resolved, nil
}

func copyWITFile(source string, target *os.Root, name string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("reading WIT: %w", err)
	}

	err = target.WriteFile(name, data, filePerm)
	if err != nil {
		return fmt.Errorf("copying WIT: %w", err)
	}

	return nil
}

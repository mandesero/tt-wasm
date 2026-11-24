// Package driver runs the tools of a Wasm language. Both the local runner
// and the builder inside a Docker image use the same operation.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// Python is the language name of the Python driver.
	Python = "python"
	// Fetch downloads the pinned dependencies into the cache.
	Fetch = "fetch"
	// Bind generates bindings in the work directory.
	Bind = "bind"
	// Build generates runtime bindings and builds the component in Work.
	Build = "build"

	// BindingsDir is the name of the generated bindings inside Work.
	BindingsDir = "bindings"
	// OutputFile is the name of the generated component inside Work.
	OutputFile = "component.wasm"

	componentize = "componentize-py"
	wasmTools    = "wasm-tools"
	wkg          = "wkg"
	python       = "python3"
	checkTimeout = 15 * time.Second
	toolWait     = 5 * time.Second
)

var (
	errLanguage  = errors.New("unsupported Wasm language")
	errOperation = errors.New("unknown Wasm operation")
	errToolArgs  = errors.New("argument is managed by tt wasm")
	errPath      = errors.New("invalid Wasm driver path")
	errTool      = errors.New("incompatible Wasm toolchain")
)

// Request describes one operation. Dir, Work and Cache are absolute paths
// in the execution environment. Source and WIT are relative to Dir, which
// the driver reads only. Work is disposable. Cache stages a new set of SDK
// dependencies during fetch and is read-only during bind and build.
type Request struct {
	Language  string   `json:"language"`
	Operation string   `json:"operation"`
	Dir       string   `json:"dir"`
	Work      string   `json:"work"`
	Cache     string   `json:"cache"`
	Source    string   `json:"source"`
	WIT       string   `json:"wit"`
	World     string   `json:"world"`
	Args      []string `json:"args,omitempty"`
	BindArgs  []string `json:"bind_args,omitempty"`
	Help      bool     `json:"help,omitempty"`
}

// Run executes the requested operation. Help invokes only the tool's help.
func Run(ctx context.Context, req Request, stdout, stderr io.Writer) error {
	driver, err := driverFor(req.Language)
	if err != nil {
		return err
	}

	run := executor{dir: "", stdout: stdout, stderr: stderr, env: nil}

	if req.Help {
		argv := driver.help(req.Operation)
		if len(argv) == 0 {
			return fmt.Errorf("%w: %q", errOperation, req.Operation)
		}

		return run.command(ctx, argv[0], argv[1:]...)
	}

	err = ValidateArgs(req.Language, req.Operation, req.Args)
	if err == nil && req.Operation == Build && len(req.BindArgs) != 0 {
		err = ValidateArgs(req.Language, Bind, req.BindArgs)
	}

	if err != nil {
		return err
	}

	run.dir = req.Work

	for _, dir := range []string{req.Dir, req.Work, req.Cache} {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("%w: %q must be absolute", errPath, dir)
		}
	}

	if !filepath.IsLocal(req.WIT) || !filepath.IsLocal(req.Source) {
		return fmt.Errorf("%w: wit and source must be relative to the component", errPath)
	}

	tmp := filepath.Join(req.Work, "tmp")

	err = os.MkdirAll(tmp, dirPerm)
	if err != nil {
		return fmt.Errorf("creating the Wasm work directory: %w", err)
	}

	run.env = []string{"TMPDIR=" + tmp}

	switch req.Operation {
	case Fetch:
		return driver.fetch(ctx, run, req)
	case Bind:
		return driver.bind(ctx, run, req)
	case Build:
		return driver.build(ctx, run, req)
	default:
		return fmt.Errorf("%w: %q", errOperation, req.Operation)
	}
}

// ValidateArgs prevents extra tool arguments from overriding the inputs,
// output and dependency cache selected by the driver.
func ValidateArgs(language, operation string, args []string) error {
	driver, err := driverFor(language)
	if err != nil {
		return err
	}

	var names []string

	switch operation {
	case Fetch:
		names = []string{"--cache", configOption, "--wit-dir", outputOption, "--update", "-d", "-o"}
		if language == CSharp {
			names = driver.managed(operation)
		}
	case Bind, Build:
		names = driver.managed(operation)
	default:
		return fmt.Errorf("%w: %q", errOperation, operation)
	}

	// Go/TinyGo and dotnet options do not treat a longer switch such as
	// -opt as the short option -o followed by an attached operand.
	var attached []string

	switch {
	case operation == Build && (language == CLanguage || language == CPP):
		attached = []string{"-o", "-x"}
	case operation == Fetch && language != CSharp,
		language == Python, language == JavaScript, language == CLanguage, language == CPP:
		attached = names
	}

	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if name == "--" || name == helpOption || name == "-h" ||
			slices.Contains(names, name) || shortOption(name, attached) {
			return fmt.Errorf("%w: %q", errToolArgs, arg)
		}
	}

	return nil
}

func shortOption(arg string, names []string) bool {
	if len(names) == 0 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}

	// Quiet is the only short global switch that can precede a managed
	// short switch in a componentize-py cluster.
	arg = "-" + strings.TrimLeft(strings.TrimPrefix(arg, "-"), "q")
	if arg == "-h" {
		return true
	}

	for _, name := range names {
		if len(name) == 2 && strings.HasPrefix(name, "-") && strings.HasPrefix(arg, name) {
			return true
		}
	}

	return false
}

var versionNumber = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

// Check reports missing or incompatible tools without installing them.
func Check(ctx context.Context, language string) error {
	driver, err := driverFor(language)
	if err != nil {
		return err
	}

	return errors.Join(
		checkTools(ctx, toolVersion{name: wasmTools, version: "1.261.0", args: nil},
			toolVersion{name: wkg, version: "0.16.1", args: nil}),
		driver.check(ctx),
	)
}

type toolVersion struct {
	name    string
	version string
	args    []string
}

func checkTools(ctx context.Context, tools ...toolVersion) error {
	var issues error

	for _, tool := range tools {
		args := tool.args
		if len(args) == 0 {
			args = []string{versionOption}
		}

		out, err := checkOutput(ctx, tool.name, args...)
		if err != nil {
			issues = errors.Join(issues, fmt.Errorf(
				"%w: %s %s: %w", errTool, tool.name, tool.version, err))

			continue
		}

		got := versionNumber.FindString(string(out))
		if !validVersion(tool.name, got, tool.version) {
			issues = errors.Join(issues, fmt.Errorf("%w: %s %s required, got %s",
				errTool, tool.name, tool.version, strings.TrimSpace(string(out))))
		}
	}

	return issues
}

func checkPython(ctx context.Context) error {
	issues := checkTools(ctx, toolVersion{name: componentize, version: "0.25.1", args: nil},
		toolVersion{name: python, version: "3.10.0", args: nil})

	_, err := checkOutput(ctx, python, "-m", "pip", versionOption)
	if err != nil {
		issues = errors.Join(issues, fmt.Errorf("%w: %s -m pip: %w", errTool, python, err))
	}

	return issues
}

func validVersion(tool, got, expected string) bool {
	if tool != python || got == "" {
		return got == expected
	}

	parts := strings.Split(got, ".")
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])

	return major == 3 && minor >= 10
}

func checkOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	cmd := toolCommand(ctx, name, args...)

	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return out, nil
}

type executor struct {
	dir    string
	stdout io.Writer
	stderr io.Writer
	env    []string
}

func (r executor) command(ctx context.Context, name string, args ...string) error {
	cmd := toolCommand(ctx, name, args...)

	cmd.Dir = r.dir
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr

	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1", "PYTHONPATH=")

	cmd.Env = append(cmd.Env, r.env...)

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

func toolCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = toolWait
	isolateTool(cmd)

	return cmd
}

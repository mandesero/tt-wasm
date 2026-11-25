package runner

import (
	"context"
	"errors"
	"io"
	"path"
)

const (
	developmentMount = "/tt-dev"
	seedMount        = "/tt-seed"
)

var errEmptyTool = errors.New("no tool command supplied")

const preparePython = `
python3 -m venv /tt-dev/venv
site=$(/tt-dev/venv/bin/python -Ic 'import sysconfig; print(sysconfig.get_path("purelib"))')
cp -R /tt-seed/. "$site/"
printf '%s\n' "${site#/tt-dev/}" >/tt-dev/site-packages
`

// PreparePython initializes a venv at a stable container path using the
// already verified packages. The caller publishes the prepared directory.
func PreparePython(
	ctx context.Context, settings Settings, development, seed string, stdout, stderr io.Writer,
) error {
	args := append(settings.runArgs(true),
		"--mount", bindMount(development, developmentMount, false),
		"--mount", bindMount(seed, seedMount, true),
		"--entrypoint", "/bin/sh", "--", settings.Image, "-ec", preparePython,
	)

	return runDocker(settings.command(ctx, args...), stdout, stderr, "preparing the Python development environment")
}

// Tool describes a command run in the component directory. State is the
// directory with locked build state. Development is a writable Python venv
// directory when the component uses Python.
type Tool struct {
	Dir         string
	State       string
	Development string
	Language    string
	Argv        []string
	TTY         bool
}

// RunTool uses the toolchain image but delegates directly to the command.
func RunTool(
	ctx context.Context, settings Settings, tool Tool, in io.Reader, stdout, stderr io.Writer,
) error {
	if len(tool.Argv) == 0 || tool.Argv[0] == "" {
		return errEmptyTool
	}

	args := append(settings.runArgs(false), "--init", "--workdir", containerComponent,
		"--mount", bindMount(tool.Dir, containerComponent, false),
		"--mount", bindMount(tool.State, path.Join(containerComponent, ".tt-wasm"), true),
	)
	if in != nil {
		args = append(args, "--interactive")
	}

	if tool.TTY {
		args = append(args, "--tty")
	}

	entrypoint, command := tool.Argv[0], tool.Argv[1:]
	if tool.Development != "" {
		args = append(args, "--mount", bindMount(tool.Development, developmentMount, false))
		entrypoint = "/bin/sh"
		command = append([]string{"-ec", `. /tt-dev/venv/bin/activate
exec "$@"`, "tt-wasm-tool"}, tool.Argv...)
	}

	if tool.Language == "csharp" {
		args = append(args, "--env", "DOTNET_CLI_HOME=/tmp")
	}

	args = append(args, "--entrypoint", entrypoint, "--", settings.Image)
	cmd := settings.command(ctx, append(args, command...)...)
	cmd.Stdin = in

	return runDocker(cmd, stdout, stderr, "running "+tool.Argv[0]+" in "+settings.Image)
}

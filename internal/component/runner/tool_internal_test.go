package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandesero/tt-wasm/internal/component/driver"
)

func TestToolKeepsLockedStateReadOnlyAndStreamsInput(t *testing.T) {
	// Docker is represented by a small executable that emits its arguments
	// followed by stdin, using NUL delimiters to preserve the original argv.
	docker := writeDockerTrace(t)

	t.Setenv("PATH", docker+string(os.PathListSeparator)+os.Getenv("PATH"))

	component := filepath.Join(t.TempDir(), "guest with a comma, and a space")
	state := filepath.Join(component, ".tt-wasm")
	tool := Tool{
		Dir: component, State: state, Language: driver.JavaScript,
		Argv: []string{"npm", "install", "an argument with spaces; $(false)", "--ignore-scripts"},
	}
	settings := Settings{Mode: Docker, Image: "builder@sha256:example", Platform: "linux/arm64", Context: "dev"}

	var output bytes.Buffer

	err := RunTool(t.Context(), settings, tool, strings.NewReader("piped input"), &output, io.Discard)
	require.NoError(t, err)

	args := strings.Split(output.String(), "\x00")
	require.Equal(t, []string{"--context", "dev", "run"}, args[:3])
	require.Contains(t, args, "linux/arm64")
	require.Contains(t, args, bindMount(component, containerComponent, false))
	require.Contains(t, args, bindMount(state, containerComponent+"/.tt-wasm", true))
	require.Contains(t, args, "--interactive")
	require.NotContains(t, args, "--tty")

	want := append([]string{"--entrypoint", "npm", "--", settings.Image}, tool.Argv[1:]...)
	want = append(want, "piped input")
	require.Equal(t, want, args[len(args)-len(want):])
}

func TestOfflineDevelopmentOnlyReplacesPythonPackages(t *testing.T) {
	docker := writeDockerTrace(t)

	t.Setenv("PATH", docker+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	packages := filepath.Join(dir, "dev", "venv", "lib", "python3.12", "site-packages")
	settings := Settings{Mode: Docker, Image: "builder@sha256:example"}
	request := driver.Request{
		Operation: driver.Build, Language: driver.Python,
		Dir: filepath.Join(dir, "guest"), Work: filepath.Join(dir, "work"), Cache: filepath.Join(dir, "cache"),
	}

	var output bytes.Buffer

	err := RunDevelopment(t.Context(), settings, request, packages, &output, io.Discard)
	require.NoError(t, err)

	args := strings.Split(output.String(), "\x00")
	overlay := bindMount(packages, containerCache+"/"+driver.PythonPackagesDir, true)
	require.Contains(t, args, "--network")
	require.Contains(t, args, "none")
	require.Contains(t, args, bindMount(request.Cache, containerCache, true))
	require.Contains(t, args, overlay)

	output.Reset()

	err = Run(t.Context(), settings, request, &output, io.Discard)
	require.NoError(t, err)
	require.NotContains(t, strings.Split(output.String(), "\x00"), overlay)
}

func writeDockerTrace(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	const script = `#!/bin/sh
printf '%s\000' "$@"
cat
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700))

	return dir
}

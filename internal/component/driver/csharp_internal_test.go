package driver

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCSharpRestoreUsesTheLockedCacheOffline(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, nugetLock)
	log := filepath.Join(dir, "restore.log")

	require.NoError(t, os.WriteFile(lock, []byte("{}\n"), filePerm))

	dotnet := filepath.Join(dir, dotnetTool)

	const tool = `#!/bin/sh
if [ "$1" = "--version" ]; then
    printf '%s\n' 10.0.402
else
    printf '%s\n' "$@" > "$TT_NUGET_TEST_LOG"
fi
`

	require.NoError(t, os.WriteFile(dotnet, []byte(tool), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TT_NUGET_TEST_LOG", log)

	project := csharpProject{
		run:    executor{dir: dir, stdout: io.Discard, stderr: io.Discard},
		source: filepath.Join(dir, "Component.csproj"), lock: lock, cache: dir,
	}

	require.NoError(t, project.restore(t.Context(), false, nil))

	args, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Contains(t, string(args), "--locked-mode\n")
	assert.Contains(t, string(args), "--source\n"+filepath.Join(dir, nugetPackages)+"\n")
	assert.Contains(t, string(args), "-p:RestoreAdditionalProjectSources=\n")
	assert.NotContains(t, string(args), "--configfile")

	current, err := os.ReadFile(lock)
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(current))
}

func TestCSharpRestoreUsesTheSelectedProjectsInputs(t *testing.T) {
	wasi := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(wasi, "VERSION"), []byte(csharpWASI+"\n"), filePerm))
	require.NoError(t, os.Mkdir(filepath.Join(wasi, "bin"), dirPerm))
	require.NoError(t, os.WriteFile(filepath.Join(wasi, "bin", "clang"), nil, 0o755))
	t.Setenv("WASI_SDK_PATH", wasi)

	dir, work := t.TempDir(), t.TempDir()
	relative := filepath.Join("guest", "Component.csproj")

	require.NoError(t, os.Mkdir(filepath.Join(dir, "guest"), dirPerm))

	for _, name := range []string{"Component.csproj", nugetLock, nugetConfig, "global.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "guest", name), []byte(name), filePerm))
	}

	for _, name := range []string{"bin", "obj"} {
		generated := filepath.Join(dir, "guest", name)
		require.NoError(t, os.Mkdir(generated, dirPerm))
		require.NoError(t, os.WriteFile(filepath.Join(generated, "previous-build"), nil, filePerm))
	}

	project, err := stageCSharp(executor{}, Request{Dir: dir, Work: work, Source: relative})
	require.NoError(t, err)

	staged := filepath.Join(work, "project", "guest")
	assert.Equal(t, staged, project.run.dir)
	assert.Equal(t, filepath.Join(staged, "Component.csproj"), project.source)
	assert.Equal(t, filepath.Join(staged, nugetLock), project.lock)
	assert.Equal(t, filepath.Join(staged, nugetConfig), project.config)
	assert.NoDirExists(t, filepath.Join(staged, "bin"))
	assert.NoDirExists(t, filepath.Join(staged, "obj"))

	inputs := LockInputs(CSharp, relative)
	for _, name := range []string{"Component.csproj", nugetLock, nugetConfig, "global.json"} {
		assert.Contains(t, inputs, filepath.Join("guest", name))
	}
}

func TestCSharpRejectsArgumentsThatChangeRestoreOrOutput(t *testing.T) {
	for _, arg := range []string{
		"--source=https://example.org", "--output=/tmp", "-p:RestoreLockedMode=false",
		"-p:PublishDir=/tmp", "@outside.rsp",
	} {
		require.ErrorIs(t, checkCSharpArgs(Build, []string{arg}), errToolArgs)
	}

	args := []string{"--verbosity", "minimal", "--no-incremental"}
	require.NoError(t, checkCSharpArgs(Build, args))
}

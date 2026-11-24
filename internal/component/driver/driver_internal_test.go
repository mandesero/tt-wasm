package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateArgs(t *testing.T) {
	t.Run("rejects managed options", func(t *testing.T) {
		for _, test := range []struct {
			operation string
			arg       string
		}{
			{operation: Bind, arg: "--world=other"},
			{operation: Bind, arg: "-dother.wit"},
			{operation: Build, arg: "--output=other.wasm"},
			{operation: Build, arg: "-oother.wasm"},
			{operation: Build, arg: "-qoother.wasm"},
			{operation: Build, arg: "--stub-wasi"},
			{operation: Fetch, arg: "--cache=elsewhere"},
			{operation: Fetch, arg: "--"},
		} {
			err := ValidateArgs(Python, test.operation, []string{test.arg})
			require.ErrorIs(t, err, errToolArgs)
		}
	})

	t.Run("accepts tool options", func(t *testing.T) {
		require.NoError(t, ValidateArgs(Python, Bind, []string{"-q", "--features", "database"}))
	})
}

func TestRunHelpNeedsNoWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, componentize, "#!/bin/sh\nprintf '%s\\n' \"$@\"\n")

	var out bytes.Buffer

	req := Request{
		Language: Python, Operation: Build, Help: true,
		Work: filepath.Join(dir, "does-not-exist"),
	}
	require.NoError(t, Run(t.Context(), req, &out, t.Output()))
	require.Equal(t, "componentize\n--help\n", out.String())
	require.NoDirExists(t, req.Work)
}

func TestFetchWkgKeepsProjectLock(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, wkg, "#!/bin/sh\nprintf 'different\\n' > wkg.lock\n")

	project := t.TempDir()
	work := t.TempDir()
	lockFile := filepath.Join(project, "wkg.lock")
	lock := []byte("version = 1\n")

	require.NoError(t, os.WriteFile(lockFile, lock, 0o644))

	run := executor{dir: work, stdout: t.Output(), stderr: t.Output()}
	req := Request{Dir: project, Work: work, Cache: t.TempDir(), Operation: Fetch}
	require.ErrorIs(t, fetchWkg(t.Context(), run, req), errWITLock)

	after, err := os.ReadFile(lockFile)
	require.NoError(t, err)
	require.Equal(t, lock, after)
}

func TestLockedPackages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requirements.lock")

	for _, fixture := range []struct {
		content string
		want    bool
		valid   bool
	}{
		{content: "# no dependencies\n", want: false, valid: true},
		{content: "package==1.2 \\\n    --hash=sha256:5e\n", want: true, valid: true},
		{content: "-r ../external.txt\n", want: false, valid: false},
		{content: "--no-binary=:all:\n", want: false, valid: false},
		{content: "p @file:///tmp/package.whl\n", want: false, valid: false},
	} {
		require.NoError(t, os.WriteFile(path, []byte(fixture.content), 0o644))

		found, err := lockedPackages(path)
		if !fixture.valid {
			require.ErrorIs(t, err, errPipLock)

			continue
		}

		require.NoError(t, err)
		require.Equal(t, fixture.want, found)
	}
}

func TestStageProjectKeepsExplicitSourceUnderDist(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	staged := filepath.Join(t.TempDir(), "project")

	for _, file := range []string{
		"dist/src/main.js", "dist/shared.js", "target/stale.wasm", ".tt-wasm/cache/fetched",
	} {
		path := filepath.Join(project, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, nil, 0o644))
	}

	require.NoError(t, stageProject(project, staged, "dist/src/main.js"))
	require.FileExists(t, filepath.Join(staged, "dist", "src", "main.js"))
	require.FileExists(t, filepath.Join(staged, "dist", "shared.js"))
	require.NoDirExists(t, filepath.Join(staged, "target"))
	require.NoDirExists(t, filepath.Join(staged, ".tt-wasm"))
}

func writeExecutable(t *testing.T, dir, name, script string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755))
}

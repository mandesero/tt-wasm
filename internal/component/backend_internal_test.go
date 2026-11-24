package component

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tarantool/tt/sdk/build"
)

func TestWasmBackendDeclaresDataOutput(t *testing.T) {
	backend := Backend()
	require.NoError(t, backend.Validate())

	config, unknown, err := backend.Decode(map[string]any{
		"language": "python", "source": "main.py", "wit": "wit", "world": "calc",
		"profile": "python-ttwasm-0.1.0-r1", "args": map[string]any{
			"build": []any{"--quiet"},
		},
	})
	require.NoError(t, err)
	require.Empty(t, unknown)

	common := build.Common{Output: []build.OutputFile{{Path: "dist/calc.wasm", To: build.Data}}}
	backendBuild, err := backend.New(config, build.Component{}, common)
	require.NoError(t, err)
	require.NoError(t, backend.ValidateBuild(backendBuild))

	checked, ok := backendBuild.(*Build)
	require.True(t, ok)
	require.Equal(t, "dist/calc.wasm", checked.Output)
	require.Equal(t, []string{"--quiet"}, checked.Args.Build)

	common.Output[0].Path = "../outside.wasm"
	_, err = backend.New(config, build.Component{}, common)
	require.ErrorContains(t, err, "within the component")
}

func TestWasmOutputKeepsLastValidatedCopy(t *testing.T) {
	dirs := build.Dirs{Component: t.TempDir(), Share: t.TempDir()}
	artifact := filepath.Join(t.TempDir(), "component.wasm")
	output := filepath.Join("dist", "calc.wasm")
	first := componentHeader + "first"
	second := componentHeader + "second"

	require.NoError(t, os.WriteFile(artifact, []byte(first), 0o600))
	require.NoError(t, publishOutput(dirs, "calc", output, artifact))

	require.NoError(t, os.WriteFile(artifact, []byte("invalid"), 0o600))
	require.ErrorIs(t, publishOutput(dirs, "calc", output, artifact), errNotComponent)

	for _, dir := range []string{dirs.Component, dirs.Share} {
		data, err := os.ReadFile(filepath.Join(dir, output))
		require.NoError(t, err)
		require.Equal(t, first, string(data))
	}

	require.NoError(t, os.WriteFile(artifact, []byte(second), 0o600))
	require.NoError(t, publishOutput(dirs, "calc", output, artifact))

	data, err := os.ReadFile(filepath.Join(dirs.Component, output))
	require.NoError(t, err)
	require.Equal(t, second, string(data))

	require.NoError(t, cleanOutputs(dirs.Component, "calc"))
	require.NoFileExists(t, filepath.Join(dirs.Component, output))
}

func TestWasmOutputPreservesUnownedFiles(t *testing.T) {
	dirs := build.Dirs{Component: t.TempDir(), Share: t.TempDir()}
	artifact := filepath.Join(t.TempDir(), "component.wasm")
	output := filepath.Join("dist", "calc.wasm")
	destination := filepath.Join(dirs.Component, output)
	userContent := []byte("hand edited")

	require.NoError(t, os.WriteFile(artifact, []byte(componentHeader), 0o600))
	require.NoError(t, publishOutput(dirs, "calc", output, artifact))
	require.NoError(t, os.WriteFile(destination, userContent, 0o600))

	require.ErrorIs(t, publishOutput(dirs, "calc", output, artifact), errNotRecordedOutput)
	require.NoError(t, cleanOutputs(dirs.Component, "calc"))

	data, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, userContent, data)
}

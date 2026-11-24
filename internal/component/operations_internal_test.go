package component

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mandesero/tt-wasm/internal/component/runner"
)

func TestDependencyKeyTracksVendoredWIT(t *testing.T) {
	dir := t.TempDir()
	wit := filepath.Join(dir, "wit")
	deps := filepath.Join(wit, "deps")

	require.NoError(t, os.MkdirAll(deps, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wit, "app.wit"), []byte("world app {}"), 0o644))

	cfg := &Config{WIT: "wit"}
	pin := profilePin{Profile: "python-ttwasm-0.1.0-r1", Runtime: "0.1.0-1"}
	settings := runner.Settings{Mode: runner.Local}

	original, err := dependencyKey(dir, cfg, pin, settings)
	require.NoError(t, err)

	changedWIT := []byte("world app { export ping: func(); }")

	require.NoError(t, os.WriteFile(filepath.Join(wit, "app.wit"), changedWIT, 0o644))

	worldChanged, err := dependencyKey(dir, cfg, pin, settings)
	require.NoError(t, err)
	assert.Equal(t, original, worldChanged)

	depWIT := []byte("package example:foo;")

	require.NoError(t, os.WriteFile(filepath.Join(deps, "foo.wit"), depWIT, 0o644))

	depsChanged, err := dependencyKey(dir, cfg, pin, settings)
	require.NoError(t, err)
	assert.NotEqual(t, original, depsChanged)
}

func TestDependencyKeyTracksRunner(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Language: "js", Source: "main.js", WIT: "app.wit"}
	pin := profilePin{Profile: "js-ttwasm-0.1.0-r1", Runtime: "0.1.0-1", Image: "builder:js"}

	require.NoError(t, os.WriteFile(filepath.Join(dir, cfg.WIT), []byte("world app {}"), 0o644))

	key := func(settings runner.Settings) string {
		t.Helper()

		digest, err := dependencyKey(dir, cfg, pin, settings)
		require.NoError(t, err)

		return digest
	}

	docker := runner.Settings{Mode: runner.Docker}
	dockerKey := key(docker)

	docker.Platform = runner.DefaultPlatform
	assert.Equal(t, dockerKey, key(docker))

	docker.Context = "other"
	assert.Equal(t, dockerKey, key(docker))

	docker.Platform = "linux/arm64"
	assert.NotEqual(t, dockerKey, key(docker))

	local := runner.Settings{Mode: runner.Local}
	localKey := key(local)
	assert.NotEqual(t, dockerKey, localKey)

	pin.Image = ""
	assert.Equal(t, localKey, key(local))
}

func TestDependencyKeyTracksNestedCSharpLock(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Language: "csharp", Source: "src/Component.csproj", WIT: "app.wit"}
	pin := profilePin{Profile: "csharp-ttwasm-0.1.0-r1", Runtime: "0.1.0-1"}
	settings := runner.Settings{Mode: runner.Docker}

	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cfg.WIT), []byte("world app {}"), 0o644))

	lock := filepath.Join(dir, "src", "packages.lock.json")
	require.NoError(t, os.WriteFile(lock, []byte(`{"version":1}`), 0o644))

	original, err := dependencyKey(dir, cfg, pin, settings)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(lock, []byte(`{"version":2}`), 0o644))

	changed, err := dependencyKey(dir, cfg, pin, settings)
	require.NoError(t, err)
	assert.NotEqual(t, original, changed)
}

func TestBindingsHashOnlyWITSources(t *testing.T) {
	dir := t.TempDir()
	wit := filepath.Join(dir, "app.wit")
	source := filepath.Join(dir, "main.py")
	record := componentState{}
	cfg := &Config{WIT: ".", World: "app"}

	require.NoError(t, os.WriteFile(wit, []byte("world app {}"), 0o644))

	original, err := bindingInputs(dir, cfg, record, nil)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(source, []byte("print('hello')"), 0o644))

	actual, err := bindingInputs(dir, cfg, record, nil)
	require.NoError(t, err)
	assert.Equal(t, original, actual)
}

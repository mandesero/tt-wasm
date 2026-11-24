package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSReportsInvalidWITBeforeCallingJco(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, bin, wasmTools, "#!/bin/sh\necho 'unknown type missing-type' >&2\nexit 1\n")
	writeExecutable(t, bin, jco, "#!/bin/sh\necho 'jco called' >&2\nexit 1\n")
	dir := t.TempDir()
	cache := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "world.wit"),
		[]byte("package example:calc; world calc { export add: func() -> missing-type; }"), filePerm))
	require.NoError(t, os.MkdirAll(filepath.Join(cache, dependenciesDir, witDir), dirPerm))

	for _, operation := range []string{Bind, Build} {
		t.Run(operation, func(t *testing.T) {
			var stderr bytes.Buffer
			req := Request{
				Language: JavaScript, Operation: operation, Dir: dir,
				Work: t.TempDir(), Cache: cache, Source: "main.js",
				WIT: "world.wit", World: "calc",
			}
			err := Run(t.Context(), req, t.Output(), &stderr)
			require.ErrorContains(t, err, "checking JavaScript WIT")
			require.Contains(t, stderr.String(), "unknown type missing-type")
			require.NotContains(t, stderr.String(), "jco called")
		})
	}
}

func TestJSToolchainRequiresLockedNPMTarballs(t *testing.T) {
	t.Parallel()

	const npmDependencies = `{
		"lockfileVersion": 3,
		"packages": {
			"": {"name": "tt-wasm-component", "version": "0.0.0"},
			"node_modules/example": {
				"resolved": "https://registry.npmjs.org/example/-/example-1.0.0.tgz",
				"integrity": "sha512-supported-by-npm"
			}
		}
	}`

	require.NoError(t, checkNPMDeps([]byte(npmDependencies)))

	for _, faulty := range []string{
		`{"lockfileVersion":1,"packages":{}}`,
		`{"lockfileVersion":3,"packages":{"node_modules/example":{"link":true}}}`,
		`{"lockfileVersion":3,"packages":{"node_modules/twr-js-sdk":{}}}`,
		`{"lockfileVersion":3,"packages":{"node_modules/example":{"resolved":"file:example.tgz"}}}`,
		`{"lockfileVersion":3,"packages":{` +
			`"node_modules/example":{"resolved":"https://npm.test/a.tgz"}}}`,
	} {
		require.ErrorIs(t, checkNPMDeps([]byte(faulty)), errJSLock)
	}
}

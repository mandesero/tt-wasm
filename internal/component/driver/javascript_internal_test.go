package driver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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

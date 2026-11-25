package wasm

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tarantool/tt/sdk/fetch"
)

func TestImportSDKIsContentAddressedAndCheckedAgain(t *testing.T) {
	t.Parallel()

	archive := makeTarGz(t, []tarEntry{
		{name: "sdk/wit/python/deps/example.wit", body: "package sdk:example;", kind: tar.TypeReg},
		{name: "sdk/packages/python/twr_python_sdk-0.1.0-py3-none-any.whl", body: "wheel", kind: tar.TypeReg},
	})
	source := filepath.Join(t.TempDir(), "checkout-sdk.tar.gz")
	require.NoError(t, os.WriteFile(source, archive, filePerm))

	f := Fetcher{CacheDir: t.TempDir()}
	sum, err := f.ImportSDK(source)
	require.NoError(t, err)
	require.Equal(t, fetch.SHA256(archive), sum)
	require.NoError(t, os.Remove(source))

	output := t.TempDir()
	require.NoError(t, f.FetchLocalSDK(sum, output))
	wit, err := os.ReadFile(filepath.Join(output, "wit", "python", "deps", "example.wit"))
	require.NoError(t, err)
	assert.Equal(t, "package sdk:example;", string(wit))

	cached := filepath.Join(f.CacheDir, sum)
	require.NoError(t, os.WriteFile(cached, []byte("corrupt"), filePerm))
	require.ErrorIs(t, f.FetchLocalSDK(sum, t.TempDir()), ErrChecksum)
	require.NoError(t, os.Remove(cached))
	require.ErrorContains(t, f.FetchLocalSDK(sum, t.TempDir()), "tt wasm prepare --lang LANG --sdk-from ARCHIVE")
	require.ErrorContains(t, f.FetchLocalSDK("../../outside", t.TempDir()), "invalid local SDK SHA-256")
}

func TestImportSDKRejectsInvalidArchivesBeforeCaching(t *testing.T) {
	t.Parallel()

	for name, archive := range map[string][]byte{
		"not gzip": []byte("invalid SDK"),
		"no WIT": makeTarGz(t, []tarEntry{
			{name: "sdk/README", body: "no WIT", kind: tar.TypeReg},
		}),
		"links": makeTarGz(t, []tarEntry{
			{name: "sdk/wit/example.wit", body: "package sdk:example;", kind: tar.TypeReg},
			{name: "sdk/wit/link.wit", link: "example.wit", kind: tar.TypeSymlink},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sdk.tar.gz")
			require.NoError(t, os.WriteFile(path, archive, filePerm))

			f := Fetcher{CacheDir: t.TempDir()}
			_, err := f.ImportSDK(path)
			require.Error(t, err)
			assert.NoFileExists(t, filepath.Join(f.CacheDir, fetch.SHA256(archive)))
		})
	}
}

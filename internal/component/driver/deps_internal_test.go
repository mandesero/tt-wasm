package driver

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSDKPackageLookupWithBracketsInCachePath(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "component [sample]", "cache")
	packages := filepath.Join(cache, sdkDir, "packages")

	t.Run(Python, func(t *testing.T) {
		dir := filepath.Join(packages, Python)
		require.NoError(t, os.MkdirAll(dir, dirPerm))
		file, err := os.Create(filepath.Join(dir, "twr_python_sdk-0.1.0-py3-none-any.whl"))
		require.NoError(t, err)

		const name = "twr_python_sdk/__init__.py"
		content := []byte("# Python SDK\n")
		wheel := zip.NewWriter(file)
		member, err := wheel.Create(name)
		require.NoError(t, err)
		_, err = member.Write(content)
		require.NoError(t, err)
		require.NoError(t, wheel.Close())
		require.NoError(t, file.Close())

		dest := t.TempDir()
		require.NoError(t, unpackSDK(cache, dest))
		actual, err := os.ReadFile(filepath.Join(dest, name))
		require.NoError(t, err)
		require.Equal(t, content, actual)
	})

	t.Run(JavaScript, func(t *testing.T) {
		dir := filepath.Join(packages, JavaScript)
		require.NoError(t, os.MkdirAll(dir, dirPerm))
		file, err := os.Create(filepath.Join(dir, jsSDK+"-0.1.0.tgz"))
		require.NoError(t, err)

		content := []byte("export {};\n")
		compressed := gzip.NewWriter(file)
		archive := tar.NewWriter(compressed)
		require.NoError(t, archive.WriteHeader(&tar.Header{
			Name: "package/index.js", Typeflag: tar.TypeReg, Size: int64(len(content)),
		}))
		_, err = archive.Write(content)
		require.NoError(t, err)
		require.NoError(t, archive.Close())
		require.NoError(t, compressed.Close())
		require.NoError(t, file.Close())

		dest := t.TempDir()
		req := Request{Language: JavaScript, Cache: cache}
		require.NoError(t, installJSSDK(req, dest))
		actual, err := os.ReadFile(filepath.Join(dest, "index.js"))
		require.NoError(t, err)
		require.Equal(t, content, actual)
	})
}

package driver

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTinyGoOptions(t *testing.T) {
	require.NoError(t, ValidateArgs(goTool, Build, []string{"-opt=2", "-tags", "demo"}))
	require.ErrorIs(t, ValidateArgs(goTool, Build, []string{"-o=other.wasm"}), errToolArgs)
	require.ErrorIs(t, ValidateArgs(goTool, Build, []string{"--target=wasip1"}), errToolArgs)
	require.ErrorIs(t, ValidateArgs(goTool, Build, []string{"-panic-unwind=none"}), errToolArgs)
}

func TestSDKArchivePath(t *testing.T) {
	dir := t.TempDir()

	for _, test := range []struct {
		name string
		path string
		link bool
	}{
		{name: "regular", path: "sdk/src/lib.rs", link: false},
		{name: "escaping", path: "sdk/../outside", link: false},
		{name: "symlink", path: "sdk/src/lib.rs", link: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(dir, test.name+".tar.gz")
			dest := filepath.Join(dir, test.name)
			content := []byte("pub struct Guest;")
			entry := &tar.Header{Name: test.path, Typeflag: tar.TypeReg, Size: int64(len(content))}

			if test.link {
				entry.Typeflag = tar.TypeSymlink
				entry.Linkname = "../other"
				entry.Size = 0
				content = nil
			}

			file, err := os.Create(archive)
			require.NoError(t, err)

			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)

			require.NoError(t, writer.WriteHeader(entry))

			_, err = writer.Write(content)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			require.NoError(t, compressed.Close())
			require.NoError(t, file.Close())

			err = unpackSDKArchive(archive, dest)
			if test.name == "regular" {
				require.NoError(t, err)

				actual, err := os.ReadFile(filepath.Join(dest, "src", "lib.rs"))
				require.NoError(t, err)
				assert.Equal(t, content, actual)
			} else {
				require.ErrorIs(t, err, errSDKArchive)
			}
		})
	}
}

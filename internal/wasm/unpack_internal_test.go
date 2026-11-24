package wasm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tarEntry is one entry of a test archive.
type tarEntry struct {
	name, body, link string
	kind             byte
	mode             int64
}

// makeTarGz packs entries into a .tar.gz.
func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buffer bytes.Buffer

	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)

	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}

		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: entry.name, Linkname: entry.link, Typeflag: entry.kind,
			Mode: mode, Size: int64(len(entry.body)),
		}))

		_, err := writer.Write([]byte(entry.body))
		require.NoError(t, err)
	}

	require.NoError(t, writer.Close())
	require.NoError(t, compressed.Close())

	return buffer.Bytes()
}

func TestUnpackTreeStripsTopAndOverlays(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "tree")

	require.NoError(t, unpackTree(makeTarGz(t, []tarEntry{
		{name: "tinygo/", kind: tar.TypeDir},
		{name: "tinygo/bin/tinygo", body: "tinygo", kind: tar.TypeReg, mode: 0o755},
		{name: "tinygo/bin/wasm-opt", body: "old", kind: tar.TypeReg, mode: 0o755},
		{name: "tinygo/lib/libc.a", body: "lib", kind: tar.TypeReg},
		{name: "tinygo/lib/link.a", link: "libc.a", kind: tar.TypeSymlink},
	}), dest))
	require.NoError(t, unpackTree(makeTarGz(t, []tarEntry{
		{name: "binaryen-version_133/bin/wasm-opt", body: "new", kind: tar.TypeReg, mode: 0o755},
	}), dest))

	data, err := os.ReadFile(filepath.Join(dest, "bin", "wasm-opt"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data), "the overlay replaces what the tree had")
	info, err := os.Stat(filepath.Join(dest, "bin", "tinygo"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	target, err := os.Readlink(filepath.Join(dest, "lib", "link.a"))
	require.NoError(t, err)
	assert.Equal(t, "libc.a", target)
}

func TestUnpackTreeRefusesEscapes(t *testing.T) {
	t.Parallel()

	for name, entries := range map[string][]tarEntry{
		"dotdot":   {{name: "top/../../evil", body: "x", kind: tar.TypeReg}},
		"absolute": {{name: "/etc/evil", body: "x", kind: tar.TypeReg}},
		"symlink":  {{name: "top/bin/evil", link: "../../../etc/passwd", kind: tar.TypeSymlink}},
		"absolute symlink": {
			{name: "top/bin/evil", link: "/etc/passwd", kind: tar.TypeSymlink},
		},
		"hardlink": {{name: "top/evil", link: "top/../../outside", kind: tar.TypeLink}},
	} {
		err := unpackTree(makeTarGz(t, entries), filepath.Join(t.TempDir(), "tree"))
		require.ErrorIs(t, err, errUnsafeEntry, name)
	}
}

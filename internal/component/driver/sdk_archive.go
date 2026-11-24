package driver

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errSDKArchive = errors.New("invalid SDK archive")

// unpackSDKArchive extracts a pinned SDK package with its single enclosing
// directory stripped. It rejects links and special entries.
func unpackSDKArchive(source, dest string) error {
	archive, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening SDK archive: %w", err)
	}

	defer func() { _ = archive.Close() }()

	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("unpacking SDK archive: %w", err)
	}

	defer func() { _ = compressed.Close() }()

	err = os.MkdirAll(dest, dirPerm)
	if err != nil {
		return fmt.Errorf("creating the extracted SDK directory: %w", err)
	}

	root, err := os.OpenRoot(dest)
	if err != nil {
		return fmt.Errorf("opening the extracted SDK directory: %w", err)
	}

	defer func() { _ = root.Close() }()

	reader := tar.NewReader(compressed)

	for {
		entry, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("reading SDK archive: %w", err)
		}

		err = writeSDKEntry(root, entry, reader)
		if err != nil {
			return err
		}
	}
}

func writeSDKEntry(root *os.Root, entry *tar.Header, data io.Reader) error {
	path := strings.TrimSuffix(entry.Name, "/")
	_, name, nested := strings.Cut(path, "/")

	if !filepath.IsLocal(path) || nested && !filepath.IsLocal(name) {
		return fmt.Errorf("%w: invalid filename %q", errSDKArchive, entry.Name)
	}

	if entry.Typeflag == tar.TypeDir {
		if nested {
			err := root.MkdirAll(name, dirPerm)
			if err != nil {
				return fmt.Errorf("creating SDK directory %s: %w", name, err)
			}
		}

		return nil
	}

	if !nested || entry.Typeflag != tar.TypeReg {
		return fmt.Errorf("%w: unsupported entry %q", errSDKArchive, entry.Name)
	}

	err := root.MkdirAll(filepath.Dir(name), dirPerm)
	if err != nil {
		return fmt.Errorf("creating the parent of SDK file %s: %w", name, err)
	}

	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("creating SDK file %s: %w", name, err)
	}

	_, err = io.Copy(file, data)

	err = errors.Join(err, file.Close())
	if err != nil {
		return fmt.Errorf("extracting SDK file %s: %w", name, err)
	}

	return nil
}

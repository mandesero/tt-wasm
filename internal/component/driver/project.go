package driver

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// buildPathAliases gives tools that split paths at whitespace a spelling
// of the same disposable build directories without whitespace.
func buildPathAliases(req Request) (Request, func(), error) {
	if !strings.ContainsAny(req.Work+req.Cache, " \t\r\n") {
		return req, func() {}, nil
	}

	dir, err := os.MkdirTemp("/tmp", "tt-wasm-paths-*")
	if err != nil {
		return Request{}, nil, fmt.Errorf("creating Wasm build path aliases: %w", err)
	}

	release := func() { _ = os.RemoveAll(dir) }
	work, cache := filepath.Join(dir, "work"), filepath.Join(dir, "cache")

	err = os.Symlink(req.Work, work)
	if err == nil {
		err = os.Symlink(req.Cache, cache)
	}

	if err != nil {
		release()

		return Request{}, nil, fmt.Errorf("creating Wasm build path aliases: %w", err)
	}

	req.Work = work
	req.Cache = cache

	return req, release, nil
}

// stageProject copies source to a disposable work directory. Tool caches
// and staged bindings are recreated by the driver for the current operation.
func stageProject(source, destination, entrypoint string, excludedDirs ...string) error {
	relative, err := filepath.Rel(source, destination)
	if err != nil || relative == "." {
		return fmt.Errorf("%w: cannot stage %s in %s", errPath, source, destination)
	}

	relative = filepath.ToSlash(relative)
	entrypoint = filepath.ToSlash(filepath.Clean(entrypoint))

	err = os.MkdirAll(destination, dirPerm)
	if err != nil {
		return fmt.Errorf("creating the staged project: %w", err)
	}

	root, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("opening the staged project: %w", err)
	}

	defer func() { _ = root.Close() }()

	files := os.DirFS(source)

	err = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if name == "." {
			return nil
		}

		if strings.HasPrefix(entry.Name(), ".git") && entry.Name() != ".gitignore" {
			if entry.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		if entry.IsDir() {
			if name == relative || slices.Contains(excludedDirs, name) || skipProjectDir(name, entrypoint) {
				return fs.SkipDir
			}

			err = root.MkdirAll(filepath.FromSlash(name), dirPerm)
			if err != nil {
				return fmt.Errorf("creating staged directory: %w", err)
			}

			return nil
		}

		if !entry.Type().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular project file", errPath, name)
		}

		return copyProjectFile(root, files, name)
	})
	if err != nil {
		return fmt.Errorf("staging component source: %w", err)
	}

	return nil
}

// skipProjectDir keeps internal state out of the staged project and allows
// a tool output directory to be used as an explicit component input.
func skipProjectDir(name, entrypoint string) bool {
	if strings.ContainsRune(name, '/') {
		return false
	}

	switch name {
	case ".tt-wasm", ".rocks", ".tarantool", "_build", "_runtime", "node_modules":
		return true
	case "dist", "target", "obj", "bin":
		return entrypoint != name && !strings.HasPrefix(entrypoint, name+"/")
	default:
		return false
	}
}

func copyProjectFile(root *os.Root, files fs.FS, name string) error {
	input, err := files.Open(name)
	if err != nil {
		return fmt.Errorf("reading project file %s: %w", name, err)
	}

	defer func() { _ = input.Close() }()

	path := filepath.FromSlash(name)

	output, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("creating staged file %s: %w", name, err)
	}

	_, err = io.Copy(output, input)

	err = errors.Join(err, output.Close())
	if err != nil {
		return fmt.Errorf("copying project file %s: %w", name, err)
	}

	return nil
}

package driver

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tarantool/tt/sdk/fetch"
)

const (
	// The Python SDK 0.1.0 uses the portable implementation of msgpack.
	msgpackURL = "https://files.pythonhosted.org/packages/61/3c/" +
		"2206f39880d38ca7ad8ac1b28d2d5ca81632d163b2d68ef90e46409ca057/msgpack-1.0.3.tar.gz"
	msgpackSum = "51fdc7fb93615286428ee7758cecc2f374d5ff363bdd884c7ea622a7a327a81e"
	msgpackTop = "msgpack-1.0.3/"
)

var (
	errSDK      = errors.New("incomplete Python SDK")
	errWITLock  = errors.New("WIT dependencies differ from wkg.lock")
	errFetchArg = errors.New("extra wkg arguments require wkg.lock")
	errPipLock  = errors.New("requirements.lock must list named packages and SHA-256 hashes")
)

func fetchPython(ctx context.Context, run executor, req Request) error {
	staged := filepath.Join(req.Cache, dependenciesDir)

	err := fetchWIT(ctx, run, req, staged)
	if err == nil {
		err = unpackSDK(req.Cache, filepath.Join(staged, pythonSDKDir))
	}

	if err == nil {
		err = fetchMsgpack(ctx, req.Cache, filepath.Join(staged, pythonSDKDir, "msgpack"))
	}

	if err == nil {
		err = installPackages(ctx, run, req, filepath.Join(staged, sitePackagesDir))
	}

	return err
}

func fetchWIT(ctx context.Context, run executor, req Request, staged string) error {
	srcDir, err := stageWIT(req)
	if err != nil {
		return err
	}

	target := filepath.Join(req.Work, witDir, witDepsDir)
	language := req.Language

	if language == CLanguage || language == CPP {
		language = Python // The release shares its Tarantool guest world with C.
	}

	sdkDeps := filepath.Join(req.Cache, sdkDir, witDir, language, witDepsDir)

	err = os.CopyFS(target, os.DirFS(sdkDeps))
	if err != nil {
		return fmt.Errorf("copying SDK WIT dependencies: %w", err)
	}

	err = copyProjectWITDeps(target, req.Dir, filepath.Join(srcDir, witDepsDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("copying project WIT dependencies: %w", err)
	}

	err = fetchWkg(ctx, run, req)
	if err != nil {
		return err
	}

	check := run

	check.stdout = io.Discard

	err = check.command(ctx, wasmTools, componentCommand, "wit", filepath.Join(req.Work, witDir))
	if err != nil {
		return fmt.Errorf("checking the fetched WIT dependencies: %w", err)
	}

	err = os.CopyFS(filepath.Join(staged, witDir), os.DirFS(target))
	if err != nil {
		return fmt.Errorf("caching WIT dependencies: %w", err)
	}

	return nil
}

func copyProjectWITDeps(target, component, source string) error {
	root, err := filepath.EvalSymlinks(component)
	if err != nil {
		return fmt.Errorf("resolving the component directory: %w", err)
	}

	rel, err := filepath.Rel(root, source)
	if err != nil {
		return fmt.Errorf("finding WIT dependencies within the component: %w", err)
	}

	source, err = projectPath(root, rel)
	if err != nil {
		return err
	}

	files := os.DirFS(source)

	err = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: linked WIT dependency %s", errPath, name)
		}

		return err
	})
	if err != nil {
		return fmt.Errorf("checking project WIT dependencies: %w", err)
	}

	err = os.CopyFS(target, files)
	if err != nil {
		return fmt.Errorf("copying project WIT: %w", err)
	}

	return nil
}

func fetchWkg(ctx context.Context, run executor, req Request) error {
	const lockName = "wkg.lock"

	path, err := projectPath(req.Dir, lockName)
	if errors.Is(err, fs.ErrNotExist) {
		if len(req.Args) > 0 {
			return errFetchArg
		}

		return nil
	}

	if err != nil {
		return fmt.Errorf("reading %s: %w", lockName, err)
	}

	lock, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", lockName, err)
	}

	work, err := os.OpenRoot(req.Work)
	if err != nil {
		return fmt.Errorf("opening the Wasm work directory: %w", err)
	}

	defer func() { _ = work.Close() }()

	err = work.WriteFile(lockName, lock, filePerm)
	if err != nil {
		return fmt.Errorf("staging %s: %w", lockName, err)
	}

	args := slices.Concat([]string{
		Fetch, filepath.Join(req.Work, witDir), "--cache", filepath.Join(req.Cache, "wkg"),
	}, req.Args)

	err = run.command(ctx, wkg, args...)
	if err != nil {
		return err
	}

	after, err := work.ReadFile(lockName)
	if err != nil || !bytes.Equal(lock, after) {
		return errWITLock
	}

	return nil
}

func unpackSDK(cache, dest string) error {
	pattern := filepath.Join(cache, sdkDir, "packages", Python, "twr_python_sdk-*.whl")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("looking up Python SDK: %w", err)
	}

	if len(matches) != 1 {
		return fmt.Errorf("%w: expected one wheel in %s", errSDK, filepath.Dir(pattern))
	}

	wheel, err := zip.OpenReader(matches[0])
	if err != nil {
		return fmt.Errorf("opening Python SDK wheel: %w", err)
	}

	defer func() { _ = wheel.Close() }()

	err = os.CopyFS(dest, wheel)
	if err != nil {
		return fmt.Errorf("extracting Python SDK wheel: %w", err)
	}

	return nil
}

func fetchMsgpack(ctx context.Context, cache, dest string) error {
	const maxSize = 1 << 20

	fetcher := fetch.Fetcher{
		CacheDir: filepath.Join(cache, "downloads"),
		Client:   fetch.Client{HTTP: nil, Limit: maxSize},
	}

	data, err := fetcher.Fetch(ctx, msgpackURL, msgpackSum)
	if err != nil {
		return fmt.Errorf("fetching Python SDK dependency msgpack: %w", err)
	}

	err = os.MkdirAll(dest, dirPerm)
	if err != nil {
		return fmt.Errorf("creating msgpack directory: %w", err)
	}

	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening msgpack source archive: %w", err)
	}

	defer func() { _ = compressed.Close() }()

	archive := tar.NewReader(compressed)

	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("reading msgpack source archive: %w", err)
		}

		name, ok := strings.CutPrefix(entry.Name, msgpackTop+"msgpack/")
		if entry.Name == msgpackTop+"COPYING" {
			name, ok = "COPYING", true
		}

		if !ok || name != filepath.Base(name) || entry.Typeflag != tar.TypeReg ||
			(filepath.Ext(name) != ".py" && name != "COPYING") {
			continue
		}

		content, err := io.ReadAll(archive)
		if err != nil {
			return fmt.Errorf("reading msgpack source %s: %w", name, err)
		}

		err = os.WriteFile(filepath.Join(dest, name), content, filePerm)
		if err != nil {
			return fmt.Errorf("extracting msgpack source %s: %w", name, err)
		}
	}
}

func installPackages(ctx context.Context, run executor, req Request, dest string) error {
	err := os.MkdirAll(dest, dirPerm)
	if err != nil {
		return fmt.Errorf("creating site-packages: %w", err)
	}

	lock, err := projectPath(req.Dir, "requirements.lock")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("checking Python requirements: %w", err)
	}

	packages, err := lockedPackages(lock)
	if err != nil || !packages {
		return err
	}

	return run.command(ctx, python, "-I", "-m", "pip", "--disable-pip-version-check",
		"--no-cache-dir", "install", targetOption, dest, "--require-hashes", "--no-deps",
		"--only-binary=:all:", "--platform", "any", "--implementation", "py",
		"--abi", "none", "--python-version", "3.14", "--requirement", lock)
}

// lockedPackages leaves requirement syntax and hash checking to pip,
// but refuses directives that could include another file or override
// the selected distribution type and package source.
func lockedPackages(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading Python requirements: %w", err)
	}

	var (
		packages bool
		number   int
	)

	for line := range strings.Lines(string(data)) {
		number++

		entry, _, _ := strings.Cut(line, "#")

		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}

		name := fields[0]

		named := !strings.HasPrefix(name, "-")

		switch {
		case named && (strings.ContainsAny(name, "@/\\:") || !strings.Contains(entry, "==")):
			return false, fmt.Errorf("%w: line %d", errPipLock, number)
		case !named && !strings.HasPrefix(name, "--hash=sha256:"):
			return false, fmt.Errorf("%w: line %d", errPipLock, number)
		}

		for _, option := range fields[1:] {
			if strings.HasPrefix(option, "@") ||
				strings.HasPrefix(option, "--") && !strings.HasPrefix(option, "--hash=sha256:") {
				return false, fmt.Errorf("%w: line %d", errPipLock, number)
			}
		}

		packages = packages || named
	}

	return packages, nil
}

package component

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"

	"github.com/tarantool/tt/sdk/build"
	"github.com/tarantool/tt/sdk/fetch"
)

const (
	bindingsName   = "bindings"
	bindRecordName = ".tt-bindings.json"
)

var (
	errStaleBindings     = errors.New("missing or changed bindings; run tt wasm bind")
	errUnmanagedBindings = errors.New("bindings directory contains a file not created by tt")
	errGeneratedFile     = errors.New("expected a regular file or directory")
)

type bindingRecord struct {
	Inputs string            `json:"inputs"`
	Files  map[string]string `json:"files"`
}

func bindingInputs(dir string, cfg *Config, state componentState, args []string) (string, error) {
	wit, err := witHashes(filepath.Join(dir, cfg.WIT))
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(struct {
		Cache string            `json:"cache"`
		World string            `json:"world"`
		WIT   map[string]string `json:"wit"`
		Args  []string          `json:"args"`
	}{Cache: filepath.Base(state.cache), World: cfg.World, WIT: wit, Args: args})
	if err != nil {
		return "", fmt.Errorf("encoding bindings inputs: %w", err)
	}

	return fetch.SHA256(data), nil
}

// witHashes matches the WIT sources the driver stages. The dependency
// cache key already accounts for the WIT dependency tree.
func witHashes(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("checking %s: %w", path, err)
	}

	if !info.IsDir() {
		return fileHashes(path, "")
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	hashes := make(map[string]string)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".wit" {
			continue
		}

		digest, err := hashFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}

		hashes[entry.Name()] = digest
	}

	return hashes, nil
}

func checkBindings(ctx context.Context, c *build.Context, configured *Build) error {
	state, err := stateForBuild(ctx, c, &configured.Config)
	if err != nil {
		return err
	}

	if !state.fetched() {
		return errNotFetched
	}

	return bindingCurrent(c.Dirs.Component, &configured.Config, state)
}

func bindingCurrent(dir string, cfg *Config, state componentState) error {
	dirname := filepath.Join(dir, stateName, bindingsName)

	record, err := readBindingRecord(dirname)
	if err != nil {
		return err
	}

	if record.Inputs == "" {
		return errStaleBindings
	}

	inputs, err := bindingInputs(dir, cfg, state, cfg.Args.Bind)
	if err != nil {
		return err
	}

	files, err := fileHashes(dirname, bindRecordName)
	if err != nil {
		return err
	}

	if record.Inputs != inputs || !maps.Equal(record.Files, files) {
		return errStaleBindings
	}

	return nil
}

func readBindingRecord(dir string) (bindingRecord, error) {
	err := stateDirectory(dir)
	if err != nil {
		return bindingRecord{}, err
	}

	var record bindingRecord

	err = readOptionalJSON(filepath.Join(dir, bindRecordName), &record)

	return record, err
}

func publishBindings(
	dir string, cfg *Config, state componentState, work string, args []string,
) error {
	target := filepath.Join(dir, stateName, bindingsName)

	err := checkBindingOwner(target)
	if err != nil {
		return err
	}

	source := filepath.Join(work, bindingsName)

	files, err := fileHashes(source, "")
	if err != nil {
		return err
	}

	if _, exists := files[bindRecordName]; exists {
		return fmt.Errorf("%w: %s", errUnmanagedBindings, bindRecordName)
	}

	inputs, err := bindingInputs(dir, cfg, state, args)
	if err != nil {
		return err
	}

	staged, err := os.MkdirTemp(filepath.Dir(target), ".bindings-")
	if err != nil {
		return fmt.Errorf("staging bindings: %w", err)
	}

	defer func() { _ = os.RemoveAll(staged) }()

	err = os.CopyFS(staged, os.DirFS(source))
	if err != nil {
		return fmt.Errorf("staging bindings: %w", err)
	}

	record := bindingRecord{Inputs: inputs, Files: files}

	err = writeJSON(filepath.Join(staged, bindRecordName), record, lockPerm)
	if err != nil {
		return err
	}

	return replaceTree(target, staged)
}

func checkBindingOwner(dir string) error {
	record, err := readBindingRecord(dir)
	if err != nil {
		return err
	}

	matches, err := fileHashes(dir, bindRecordName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	for name := range matches {
		if _, owned := record.Files[name]; !owned {
			return fmt.Errorf("%w: %s", errUnmanagedBindings, filepath.Join(dir, name))
		}
	}

	return nil
}

func cleanBindings(c *build.Context) error {
	dir := filepath.Join(c.Dirs.Component, stateName)

	err := stateDirectory(dir)
	if err != nil {
		return err
	}

	target := filepath.Join(dir, bindingsName)

	record, err := readBindingRecord(target)
	if err != nil || record.Inputs == "" {
		return err
	}

	files, err := fileHashes(target, bindRecordName)
	if err != nil {
		return err
	}

	if maps.Equal(record.Files, files) {
		err = os.RemoveAll(target)
		if err != nil {
			return fmt.Errorf("removing generated bindings: %w", err)
		}

		return nil
	}

	for name, digest := range files {
		if record.Files[name] != digest {
			continue
		}

		err = os.Remove(filepath.Join(target, name))
		if err != nil {
			return fmt.Errorf("removing generated binding %s: %w", name, err)
		}
	}

	return nil
}

// fileHashes takes a path-relative inventory of a tree, ignoring only the
// named record at its root. It never follows a generated symlink.
func fileHashes(root, ignore string) (map[string]string, error) {
	files := make(map[string]string)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		name, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("naming %s relative to %s: %w", path, root, err)
		}

		if name == ignore {
			return nil
		}

		if !entry.Type().IsRegular() {
			return fmt.Errorf("%w: %s", errGeneratedFile, path)
		}

		digest, err := hashFile(path)
		if err != nil {
			return err
		}

		files[name] = digest

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading files under %s: %w", root, err)
	}

	return files, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()

	_, err = io.Copy(hash, file)
	if err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

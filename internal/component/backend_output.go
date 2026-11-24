package component

import (
	"bytes"
	"crypto/rand"
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
	"slices"

	"github.com/tarantool/tt/sdk/build"
)

const (
	// outputDirPerm is the mode of a generated directory.
	outputDirPerm fs.FileMode = 0o750
	// outputFilePerm is the mode of a generated file.
	outputFilePerm fs.FileMode = 0o644
	// componentHeader is the binary magic, version and layer of a component.
	componentHeader = "\x00asm\x0d\x00\x01\x00"
)

var (
	// errOutputNotRegular reports a path replaced with something other than a file.
	errOutputNotRegular  = errors.New("not a regular file")
	errInvalidRecord     = errors.New("invalid wasm output record")
	errNotComponent      = errors.New("not a WebAssembly component")
	errNotRecordedOutput = errors.New("not a recorded build output")
)

// outputs remembers the hashes of the files a component generated. An output
// may have both its old and new hash while a verified build is replacing it.
type outputs map[string][]string

func outputRecord(component string) string {
	digest := sha256.Sum256([]byte(component))

	return filepath.Join(stateName, "outputs", hex.EncodeToString(digest[:])+".json")
}

func readOutputs(root *os.Root, record string) (outputs, error) {
	info, err := root.Lstat(record)
	if errors.Is(err, fs.ErrNotExist) {
		return make(outputs), nil
	}

	if err != nil {
		return nil, fmt.Errorf("checking the wasm output record: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("wasm output record %s: %w", record, errOutputNotRegular)
	}

	data, err := root.ReadFile(record)
	if err != nil {
		return nil, fmt.Errorf("reading the wasm output record: %w", err)
	}

	known := make(outputs)

	err = json.Unmarshal(data, &known)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %w", errInvalidRecord, record, err)
	}

	if known == nil {
		return nil, fmt.Errorf("%w %s", errInvalidRecord, record)
	}

	return known, nil
}

// publishOutput writes the validated component into the package stage and
// replaces the source tree's copy last. A user file is never overwritten.
func publishOutput(dirs build.Dirs, component, path, artifact string) error {
	source, err := os.OpenRoot(dirs.Component)
	if err != nil {
		return fmt.Errorf("opening the component directory: %w", err)
	}

	defer func() { _ = source.Close() }()

	record := outputRecord(component)

	known, err := readOutputs(source, record)
	if err != nil {
		return err
	}

	previous, err := outputDigest(source, path)
	if err != nil {
		return err
	}

	if previous != "" && !slices.Contains(known[path], previous) {
		return fmt.Errorf("refusing to replace %s: %w", path, errNotRecordedOutput)
	}

	file, err := openArtifact(artifact)
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	stage, err := os.OpenRoot(dirs.Share)
	if err != nil {
		return fmt.Errorf("opening the package share directory: %w", err)
	}

	defer func() { _ = stage.Close() }()

	digest := sha256.New()

	err = writeOutput(stage, path, io.TeeReader(file, digest))
	if err != nil {
		return err
	}

	current := hex.EncodeToString(digest.Sum(nil))

	known[path] = []string{current}

	if previous != "" && previous != current {
		known[path] = append(known[path], previous)
	}

	data, err := json.Marshal(known)
	if err != nil {
		return fmt.Errorf("encoding the wasm output record: %w", err)
	}

	err = writeOutput(source, record, bytes.NewReader(data))
	if err != nil {
		return err
	}

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return fmt.Errorf("rewinding the wasm component: %w", err)
	}

	return writeOutput(source, path, file)
}

func openArtifact(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("checking the built component: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("built component %s: %w", path, errOutputNotRegular)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the built component: %w", err)
	}

	var header [len(componentHeader)]byte

	_, err = io.ReadFull(file, header[:])
	if err != nil || string(header[:]) != componentHeader {
		_ = file.Close()

		return nil, fmt.Errorf("%s: %w", path, errNotComponent)
	}

	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		_ = file.Close()

		return nil, fmt.Errorf("rewinding the wasm component: %w", err)
	}

	return file, nil
}

// writeOutput writes beside the destination and renames the complete file.
func writeOutput(root *os.Root, path string, src io.Reader) error {
	dir := filepath.Dir(path)

	err := root.MkdirAll(dir, outputDirPerm)
	if err != nil {
		return fmt.Errorf("creating the parent directory of %s: %w", path, err)
	}

	temp := filepath.Join(dir, ".wasm-"+rand.Text())

	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, outputFilePerm)
	if err != nil {
		return fmt.Errorf("creating a temporary output for %s: %w", path, err)
	}

	defer func() { _ = root.Remove(temp) }()

	_, err = io.Copy(file, src)

	err = errors.Join(err, file.Close())
	if err == nil {
		err = root.Rename(temp, path)
	}

	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

func outputDigest(root *os.Root, path string) (string, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("checking generated output: %w", err)
	}

	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("output %s: %w", path, errOutputNotRegular)
	}

	file, err := root.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening generated output: %w", err)
	}

	defer func() { _ = file.Close() }()

	digest := sha256.New()

	_, err = io.Copy(digest, file)
	if err != nil {
		return "", fmt.Errorf("hashing generated output: %w", err)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}

func cleanOutputs(dir, component string) error {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("opening the component directory: %w", err)
	}

	defer func() { _ = root.Close() }()

	record := outputRecord(component)

	known, err := readOutputs(root, record)
	if err != nil {
		return err
	}

	for _, path := range slices.Sorted(maps.Keys(known)) {
		if !validOutput(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%w: invalid output path %q", errInvalidRecord, path)
		}

		sum, err := outputDigest(root, path)
		if errors.Is(err, errOutputNotRegular) {
			continue
		}

		if err != nil {
			return err
		}

		if sum != "" && slices.Contains(known[path], sum) {
			err = root.Remove(path)
			if err != nil {
				return fmt.Errorf("removing generated output: %w", err)
			}
		}
	}

	err = root.Remove(record)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the wasm output record: %w", err)
	}

	return nil
}

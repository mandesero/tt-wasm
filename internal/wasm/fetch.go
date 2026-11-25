package wasm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/tarantool/tt/sdk/fetch"
)

// maxAssetSize bounds a downloaded asset: the native modules are tens of
// megabytes, a toolchain such as the wasi-sdk a few hundred, and a server
// that keeps sending is not one tt should trust.
const maxAssetSize = 1 << 30

// Fetcher downloads release assets, keeping each one, once its checksum is
// right, in a cache directory, so that preparing another project, or the same
// one again, does not download it again. The downloads and the cache are
// those of sdk/fetch.
type Fetcher struct {
	// CacheDir holds the assets, each named by its sha256; empty disables
	// the cache.
	CacheDir string
	// Client is the HTTP client; nil is one with fetch.DefaultTimeout.
	Client *http.Client
}

// DefaultCacheDir is where assets are cached: <user cache>/tt/wasm, or
// nowhere when the system names no cache directory.
func DefaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}

	return filepath.Join(dir, "tt", "wasm")
}

// Fetch returns the content of the release asset named asset, checked against
// sum, the sha256 tt holds the release to. A cached copy is used only when its
// checksum is right; a download is checked before it is cached or returned.
func (f Fetcher) Fetch(ctx context.Context, release Release, asset, sum string) ([]byte, error) {
	return f.FetchURL(ctx, release.AssetURL(asset), sum)
}

// FetchSDK unpacks the pinned SDK of release into a new, empty directory.
func (f Fetcher) FetchSDK(ctx context.Context, release Release, dir string) error {
	archive, err := f.Fetch(ctx, release, release.SDKAsset, release.SDKSHA256)
	if err != nil {
		return err
	}

	return unpackTree(archive, dir)
}

// ImportSDK checks a local SDK archive and stores its bytes in the host cache.
func (f Fetcher) ImportSDK(path string) (string, error) {
	if f.CacheDir == "" {
		return "", errors.New("no host cache directory for a local Tarantool Wasm SDK")
	}

	archive, err := readSDKArchive(path)
	if err != nil {
		return "", fmt.Errorf("reading SDK archive %s: %w", path, err)
	}

	stage, err := os.MkdirTemp("", "tt-wasm-sdk-*")
	if err != nil {
		return "", fmt.Errorf("staging local SDK: %w", err)
	}

	defer func() { _ = os.RemoveAll(stage) }()

	err = unpackLocalSDK(archive, stage)
	if err != nil {
		return "", fmt.Errorf("checking SDK archive %s: %w", path, err)
	}

	sum := fetch.SHA256(archive)
	err = storeSDKArchive(f.CacheDir, sum, archive)
	if err != nil {
		return "", fmt.Errorf("caching local SDK: %w", err)
	}

	return sum, nil
}

// FetchLocalSDK unpacks only the local archive previously imported at sum.
func (f Fetcher) FetchLocalSDK(sum, dir string) error {
	digest, err := hex.DecodeString(sum)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != sum {
		return fmt.Errorf("invalid local SDK SHA-256 %q in wasm.lock.json", sum)
	}

	if f.CacheDir == "" {
		return fmt.Errorf("local SDK %s has no host cache; run tt wasm prepare --lang LANG --sdk-from ARCHIVE", sum)
	}

	archive, err := readSDKArchive(filepath.Join(f.CacheDir, sum))
	if err == nil && fetch.SHA256(archive) != sum {
		err = ErrChecksum
	}

	if err != nil {
		return fmt.Errorf("local SDK %s is missing or invalid in the host cache; "+
			"run tt wasm prepare --lang LANG --sdk-from ARCHIVE: %w", sum, err)
	}

	return unpackLocalSDK(archive, dir)
}

func readSDKArchive(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular SDK archive file")
	}

	if info.Size() > maxAssetSize {
		return nil, fetch.ErrTooLarge
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = file.Close() }()

	archive, err := io.ReadAll(io.LimitReader(file, maxAssetSize+1))
	if err != nil {
		return nil, err
	}

	if len(archive) > maxAssetSize {
		return nil, fetch.ErrTooLarge
	}

	return archive, nil
}

func storeSDKArchive(dir, sum string, archive []byte) error {
	err := os.MkdirAll(dir, dirPerm)
	if err != nil {
		return err
	}

	file, err := os.CreateTemp(dir, ".sdk-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(file.Name()) }()

	_, err = file.Write(archive)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}

	return os.Rename(file.Name(), filepath.Join(dir, sum))
}

// FetchURL returns the content at url, checked against sum, its sha256, as
// Fetch does: the cache is keyed by sum.
func (f Fetcher) FetchURL(ctx context.Context, url, sum string) ([]byte, error) {
	fetcher := fetch.Fetcher{
		Client:   fetch.Client{HTTP: f.Client, Limit: maxAssetSize},
		CacheDir: f.CacheDir,
	}

	return fetcher.Fetch(ctx, url, sum)
}

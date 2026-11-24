package wasm

import (
	"errors"

	"github.com/tarantool/tt/sdk/fetch"
)

var (
	// ErrUnknownRelease reports a tarantool-wasm version tt has no table entry
	// for.
	ErrUnknownRelease = errors.New("unknown release")
	// ErrUnsupportedPlatform reports a host the release has no native build
	// for.
	ErrUnsupportedPlatform = errors.New("unsupported platform")
	// ErrChecksum reports a downloaded asset whose checksum differs from the
	// one tt holds the release to. It is fetch.ErrChecksum.
	ErrChecksum = fetch.ErrChecksum
	// ErrNoManifest reports a directory that is not a tt project.
	ErrNoManifest = errors.New("not a tt project")
	// ErrModuleName reports a platform whose module is named other than
	// Tarantool loads it by.
	ErrModuleName = errors.New("bad module name")
)

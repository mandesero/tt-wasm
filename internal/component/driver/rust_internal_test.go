package driver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRustWITMatchesComponentManifest(t *testing.T) {
	req := Request{WIT: "wit", World: "custom-world"}

	var metadata rustComponentMetadata

	metadata.Target.Path = "wit"
	metadata.Target.World = req.World
	metadata.Target.Dependencies = map[string]rustWITDependency{
		"example:dep": {Path: "wit/deps/example-dep"},
	}

	err := checkRustWIT(metadata, req)
	if err != nil {
		t.Fatalf("valid local WIT dependencies: %v", err)
	}

	metadata.Target.World = "other"

	err = checkRustWIT(metadata, req)
	if !errors.Is(err, errRustTarget) {
		t.Fatalf("mismatched world: %v", err)
	}

	metadata.Target.World = req.World
	metadata.Target.Dependencies["example:dep"] = rustWITDependency{Path: "../../outside"}

	err = checkRustWIT(metadata, req)
	if !errors.Is(err, errRustWITDep) {
		t.Fatalf("external WIT dependency: %v", err)
	}
}

func TestStageRustSDKSupportsBothScaffolds(t *testing.T) {
	source := t.TempDir()
	manifest := []byte("[package]\nname = \"tarantool-wasm\"\n")
	if err := os.WriteFile(filepath.Join(source, "Cargo.toml"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	stage := t.TempDir()
	if err := stageRustSDK(stage, source); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{".tt-wasm/bindings/tarantool-sdk", ".tt-wasm-sdk-rust"} {
		got, err := os.ReadFile(filepath.Join(stage, path, "Cargo.toml"))
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != string(manifest) {
			t.Fatalf("%s: got Cargo.toml %q, want %q", path, got, manifest)
		}
	}
}

package driver

import (
	"errors"
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

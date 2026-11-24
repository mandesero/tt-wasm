package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	goTool        = "go"
	tinyGo        = "tinygo"
	witBindgenGo  = "wit-bindgen-go"
	goBindings    = ".tt-wasm/bindings"
	goSDK         = "tarantool-sdk"
	goModuleCache = dependenciesDir + "/go/modules"
	goSDKCache    = dependenciesDir + "/go/" + goSDK
	goMod         = "go.mod"
	goSum         = "go.sum"
)

var (
	errGoDependencyLock = errors.New("module locks changed or are missing")
	errGoModule         = errors.New("invalid Go module")
)

func goDriver() languageDriver {
	return languageDriver{
		fetch:   fetchGo,
		bind:    bindGo,
		build:   buildGo,
		check:   checkGoTools,
		help:    goHelp,
		managed: goManaged,
		locks:   []string{goMod, goSum},
		cache:   []string{goModuleCache, goSDKCache},
	}
}

func goHelp(operation string) []string {
	switch operation {
	case Fetch:
		return []string{wkg, Fetch, helpOption}
	case Bind:
		return []string{witBindgenGo, "generate", helpOption}
	default:
		return []string{tinyGo, Build, "-h"}
	}
}

func goManaged(operation string) []string {
	switch operation {
	case Fetch:
		return nil
	case Bind:
		return []string{
			worldOption, outOption, "--package-root", "--cm", "--dry-run", "-w", "-o", "-p",
		}
	default:
		return []string{"-target", targetOption, "-o", "--o",
			"-wit-package", "--wit-package", "-wit-world", "--wit-world",
			"-scheduler", "--scheduler", "-panic-unwind", "--panic-unwind",
			"-mod", "--mod", "-modfile", "--modfile", "-overlay", "--overlay", "-C"}
	}
}

func checkGoTools(ctx context.Context) error {
	issues := checkTools(ctx,
		toolVersion{name: tinyGo, version: "0.43.0", args: []string{"version"}},
		toolVersion{name: witBindgenGo, version: "0.7.0", args: nil},
	)

	out, err := checkOutput(ctx, goTool, "version")
	if err != nil {
		issues = errors.Join(issues, fmt.Errorf("%w: go 1.27.x: %w", errTool, err))
	} else if !strings.HasPrefix(versionNumber.FindString(string(out)), "1.27.") {
		issues = errors.Join(issues, fmt.Errorf("%w: go 1.27.x required, got %q", errTool,
			strings.TrimSpace(string(out))))
	}

	out, err = checkOutput(ctx, "wasm-opt", versionOption)
	if err != nil {
		return errors.Join(issues, fmt.Errorf("%w: wasm-opt 133: %w", errTool, err))
	}

	words := strings.Fields(string(out))
	if len(words) < 3 || words[0] != "wasm-opt" || words[1] != "version" || words[2] != "133" {
		return errors.Join(issues, fmt.Errorf("%w: wasm-opt 133 required, got %q", errTool,
			strings.TrimSpace(string(out))))
	}

	return issues
}

func fetchGo(ctx context.Context, run executor, req Request) error {
	err := fetchWIT(ctx, run, req, filepath.Join(req.Cache, dependenciesDir))
	if err != nil {
		return err
	}

	packages := filepath.Join(req.Cache, sdkDir, "packages", goTool)

	archives, err := fs.Glob(os.DirFS(packages), "twr-go-sdk-*.tar.gz")
	if err != nil || len(archives) != 1 {
		return fmt.Errorf("%w: expected one Go SDK archive in %s", errSDKArchive, packages)
	}

	err = unpackSDKArchive(filepath.Join(packages, archives[0]),
		filepath.Join(req.Cache, goSDKCache))
	if err != nil {
		return err
	}

	moduleCache := filepath.Join(req.Cache, goModuleCache)

	err = os.MkdirAll(moduleCache, dirPerm)
	if err != nil {
		return fmt.Errorf("creating the Go module cache: %w", err)
	}

	run, _, err = prepareGoProject(ctx, run, req, false)
	if err != nil {
		return err
	}

	err = run.command(ctx, goTool, "mod", "download", "all")
	if err != nil {
		return err
	}

	return checkGoLocks(req.Dir, run.dir)
}

func bindGo(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	run, module, err := prepareGoProject(ctx, run, req, true)
	if err != nil {
		return err
	}

	err = generateGo(ctx, run, req, wit, module, req.Args)
	if err != nil {
		return err
	}

	bindings := filepath.Join(run.dir, goBindings)

	err = os.Rename(bindings, filepath.Join(req.Work, BindingsDir))
	if err != nil {
		return fmt.Errorf("staging Go bindings: %w", err)
	}

	return nil
}

func buildGo(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	run, module, err := prepareGoProject(ctx, run, req, true)
	if err != nil {
		return err
	}

	err = generateGo(ctx, run, req, wit, module, req.BindArgs)
	if err != nil {
		return err
	}

	output := filepath.Join(req.Work, OutputFile)

	source := filepath.Clean(req.Source)
	if source != "." {
		source = "./" + filepath.ToSlash(source)
	}

	flags := []string{Build, "-target=wasip2", "-scheduler=none", "-panic-unwind=explicit",
		"--wit-package", wit, "--wit-world", req.World, "-o", output}

	args := slices.Concat(flags, req.Args, []string{source})

	err = run.command(ctx, tinyGo, args...)
	if err != nil {
		return err
	}

	err = checkGoLocks(req.Dir, run.dir)
	if err != nil {
		return err
	}

	return validateComponent(ctx, run, output)
}

func generateGo(
	ctx context.Context, run executor, req Request, wit, module string, extra []string,
) error {
	flags := []string{"generate", worldOption, req.World,
		outOption, filepath.Join(run.dir, goBindings), "--package-root", module + "/" + goBindings}

	args := slices.Concat(flags, extra, []string{wit})

	return run.command(ctx, witBindgenGo, args...)
}

// prepareGoProject gives the compiler its own source tree. SDK replacement
// paths and package imports agree with the bindings published by tt wasm bind.
func prepareGoProject(
	ctx context.Context, run executor, req Request, offline bool,
) (executor, string, error) {
	project := filepath.Join(req.Work, "project")

	err := stageProject(req.Dir, project, req.Source)
	if err != nil {
		return run, "", err
	}

	sdk := filepath.Join(req.Cache, goSDKCache)

	err = os.CopyFS(filepath.Join(project, goBindings, goSDK), os.DirFS(sdk))
	if err != nil {
		return run, "", fmt.Errorf("copying the staged Go SDK: %w", err)
	}

	run.dir = project
	run.env = append(run.env, "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local",
		"GOFLAGS=-mod=readonly", "GOMODCACHE="+filepath.Join(req.Cache, goModuleCache),
		"GOCACHE="+filepath.Join(req.Work, "go-build-cache"), "GOTELEMETRY=off")

	if offline {
		run.env = append(run.env, "GOPROXY=off", "GOSUMDB=off", "GONOPROXY=none", "GOVCS=*:off")
	}

	module, err := readGoModule(ctx, run)

	return run, module, err
}

func readGoModule(ctx context.Context, run executor) (string, error) {
	var output bytes.Buffer

	run.stdout = &output

	err := run.command(ctx, goTool, "mod", "edit", "-json")
	if err != nil {
		return "", err
	}

	var info struct {
		Module struct {
			Path string `json:"path"`
		} `json:"module"`
		Replace []struct {
			New struct {
				Path    string `json:"path"`
				Version string `json:"version"`
			} `json:"new"`
		} `json:"replace"`
	}

	err = json.Unmarshal(output.Bytes(), &info)
	if err != nil || info.Module.Path == "" {
		return "", fmt.Errorf("%w: go.mod must declare a module path", errGoModule)
	}

	for _, replacement := range info.Replace {
		if replacement.New.Version == "" && !filepath.IsLocal(replacement.New.Path) {
			return "", fmt.Errorf("%w: replace path %q must stay in the component",
				errGoModule, replacement.New.Path)
		}
	}

	return info.Module.Path, nil
}

// checkGoLocks rejects resolver updates even though commands only touch their
// staging copy. An empty go.sum may be absent for a module with no imports.
func checkGoLocks(source, project string) error {
	for _, name := range []string{goMod, goSum} {
		before, err := os.ReadFile(filepath.Join(source, name))
		if err != nil && (name != goSum || !errors.Is(err, fs.ErrNotExist)) {
			return fmt.Errorf("reading %s: %w", name, err)
		}

		after, err := os.ReadFile(filepath.Join(project, name))
		if err != nil && (name != goSum || !errors.Is(err, fs.ErrNotExist)) {
			return fmt.Errorf("reading staged %s: %w", name, err)
		}

		if !bytes.Equal(before, after) {
			return fmt.Errorf("%w: update %s before running tt wasm fetch",
				errGoDependencyLock, name)
		}
	}

	return nil
}

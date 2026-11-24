package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	jco                    = "jco"
	npm                    = "npm"
	node                   = "node"
	jsDependenciesDir      = "js"
	nodeModules            = "node_modules"
	packageJSON            = "package.json"
	packageLock            = "package-lock.json"
	jsSDK                  = "twr-js-sdk"
	jsMinimumNode          = 24
	jsWorldOption          = "--world-name"
	jsJcoVersion           = "1.37.0"
	jsComponentizerVersion = "0.23.0"
	jsShimVersion          = "0.17.8"
)

var (
	errJSLock = errors.New("JavaScript dependencies require an up-to-date npm lockfile")
	errJSSDK  = errors.New("missing or ambiguous JavaScript SDK archive")
)

func jsDriver() languageDriver {
	return languageDriver{
		fetch:   fetchJS,
		bind:    bindJS,
		build:   buildJS,
		check:   checkJS,
		help:    jsHelp,
		managed: jsManaged,
		locks:   []string{packageJSON, packageLock},
		cache:   []string{filepath.Join(dependenciesDir, jsDependenciesDir, nodeModules)},
	}
}

func jsHelp(operation string) []string {
	switch operation {
	case Fetch:
		return []string{wkg, Fetch, helpOption}
	case Bind:
		return []string{jco, "guest-types", helpOption}
	default:
		return []string{jco, componentizeCommand, helpOption}
	}
}

func jsManaged(operation string) []string {
	switch operation {
	case Fetch:
		return nil
	case Bind:
		return []string{"-n", "-o", jsWorldOption, outDirOption, "--name"}
	default:
		return []string{
			"-w", "-n", "-o", "--wit", jsWorldOption, outOption, "--cwd", "--bundle",
			"--bundle-config", "--preview2-adapter", "--wizer-bin", "--weval-bin",
			"--with-nodejs-vfs-wasi-config", "--engine", "--backend",
			"--debug-bindings-dir", "--debug-binary-path",
		}
	}
}

func checkJS(ctx context.Context) error {
	err := checkTools(ctx, toolVersion{name: jco, version: jsJcoVersion, args: nil})
	if err != nil {
		return err
	}

	version, err := checkOutput(ctx, node, versionOption)
	if err != nil {
		return fmt.Errorf("checking Node.js: %w", err)
	}

	major, _, _ := strings.Cut(versionNumber.FindString(string(version)), ".")

	nodeMajor, err := strconv.Atoi(major)
	if err != nil || nodeMajor < jsMinimumNode {
		return fmt.Errorf("%w: Node.js %d or newer required, got %s", errTool,
			jsMinimumNode, strings.TrimSpace(string(version)))
	}

	_, err = checkOutput(ctx, npm, versionOption)
	if err != nil {
		return fmt.Errorf("checking npm: %w", err)
	}

	jcoPath, err := exec.LookPath(jco)
	if err != nil {
		return fmt.Errorf("locating %s: %w", jco, err)
	}

	// Resolve packages from Jco and ComponentizeJS, the locations Node uses
	// during componentization. A global npm installation can also contain
	// versions of these packages that these tools never load.
	const script = `
const {createRequire} = require("node:module");
const {readFileSync, realpathSync} = require("node:fs");
const {join} = require("node:path");
function locate(from, name) {
    for (const base of createRequire(realpathSync(from)).resolve.paths(name)) {
        const path = join(base, name, "package.json");
        try {
            return [path, JSON.parse(readFileSync(path, "utf8")).version];
        } catch (err) {
            if (err.code !== "ENOENT") throw err;
        }
    }
    throw Error("missing " + name);
}
const [path, componentize] = locate(process.argv[1], "@bytecodealliance/componentize-js");
const [, shim] = locate(path, "@bytecodealliance/preview2-shim");
console.log(componentize, shim);
`

	metadata, err := checkOutput(ctx, node, "-e", script, "--", jcoPath)
	if err != nil {
		return fmt.Errorf("checking Jco packages: %w", err)
	}

	versions := strings.Fields(string(metadata))
	if !slices.Equal(versions, []string{jsComponentizerVersion, jsShimVersion}) {
		return fmt.Errorf("%w: Jco requires componentize-js %s and its preview2-shim %s, got %q",
			errTool, jsComponentizerVersion, jsShimVersion, strings.TrimSpace(string(metadata)))
	}

	return nil
}

func fetchJS(ctx context.Context, run executor, req Request) error {
	deps := filepath.Join(req.Cache, dependenciesDir)

	err := fetchWIT(ctx, run, req, deps)
	if err != nil {
		return err
	}

	run.dir = filepath.Join(deps, jsDependenciesDir)
	run.env = append(slices.Clone(run.env), "TMPDIR="+filepath.Join(req.Work, "tmp"))

	err = os.MkdirAll(run.dir, dirPerm)
	if err != nil {
		return fmt.Errorf("creating npm dependency directory: %w", err)
	}

	err = fetchNPM(ctx, run, req)
	if err != nil {
		return err
	}

	return installJSSDK(req, filepath.Join(run.dir, nodeModules, jsSDK))
}

func fetchNPM(ctx context.Context, run executor, req Request) error {
	manifest, err := projectPath(req.Dir, packageJSON)
	if errors.Is(err, fs.ErrNotExist) {
		_, lockErr := projectPath(req.Dir, packageLock)
		if errors.Is(lockErr, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("%w: missing %s", errJSLock, packageJSON)
	}

	if err != nil {
		return err
	}

	lock, err := projectPath(req.Dir, packageLock)
	if err != nil {
		return fmt.Errorf("%w: %w", errJSLock, err)
	}

	data, err := os.ReadFile(lock)
	if err != nil {
		return fmt.Errorf("reading npm lockfile: %w", err)
	}

	err = checkNPMDeps(data)
	if err != nil {
		return err
	}

	metadata, err := os.ReadFile(manifest)
	if err != nil {
		return fmt.Errorf("reading package.json: %w", err)
	}

	project, err := os.OpenRoot(run.dir)
	if err != nil {
		return fmt.Errorf("opening the npm dependency directory: %w", err)
	}

	defer func() { _ = project.Close() }()

	err = project.WriteFile(packageJSON, metadata, filePerm)
	if err == nil {
		err = project.WriteFile(packageLock, data, filePerm)
	}

	if err != nil {
		return fmt.Errorf("staging npm metadata: %w", err)
	}

	return run.command(ctx, npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund",
		"--omit=dev", "--no-bin-links", "--cache", filepath.Join(req.Work, "npm-cache"))
}

func checkNPMDeps(data []byte) error {
	var lock struct {
		Version  int `json:"lockfileVersion"` //nolint:tagliatelle // This is npm's field.
		Packages map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
			Link      bool   `json:"link"`
			InBundle  bool   `json:"inBundle"` //nolint:tagliatelle // This is npm's field.
		} `json:"packages"`
	}

	err := json.Unmarshal(data, &lock)
	if err != nil {
		return fmt.Errorf("%w: %w", errJSLock, err)
	}

	if lock.Version < 2 || lock.Packages == nil {
		return fmt.Errorf("%w: expected npm lockfileVersion 2 or newer", errJSLock)
	}

	for name, dependency := range lock.Packages {
		if name == "" {
			continue
		}

		if name == filepath.ToSlash(filepath.Join(nodeModules, jsSDK)) {
			return fmt.Errorf("%w: %s is provided by the Tarantool SDK", errJSLock, jsSDK)
		}

		if !fs.ValidPath(name) || !strings.HasPrefix(name, nodeModules+"/") || dependency.Link {
			return fmt.Errorf("%w: unsupported npm dependency %q", errJSLock, name)
		}

		if dependency.InBundle && dependency.Resolved == "" && dependency.Integrity == "" {
			continue
		}

		address, err := url.Parse(dependency.Resolved)
		if err != nil || address.Scheme != "https" || address.Host == "" ||
			dependency.Integrity == "" {
			return fmt.Errorf("%w: %s must have an HTTPS source and package integrity",
				errJSLock, name)
		}
	}

	return nil
}

func installJSSDK(req Request, dest string) error {
	packages := filepath.Join(req.Cache, sdkDir, "packages", req.Language)

	matches, err := fs.Glob(os.DirFS(packages), jsSDK+"-*.tgz")
	if err != nil || len(matches) != 1 {
		return fmt.Errorf("%w: %s", errJSSDK, packages)
	}

	return unpackSDKArchive(filepath.Join(packages, matches[0]), dest)
}

func bindJS(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	err = checkWIT(ctx, run, wit)
	if err != nil {
		return fmt.Errorf("checking JavaScript WIT: %w", err)
	}

	options := []string{"guest-types", wit, jsWorldOption, req.World,
		outDirOption, filepath.Join(req.Work, BindingsDir)}

	return run.command(ctx, jco, slices.Concat(options, req.Args)...)
}

func buildJS(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	err = checkWIT(ctx, run, wit)
	if err != nil {
		return fmt.Errorf("checking JavaScript WIT: %w", err)
	}

	project := filepath.Join(req.Work, "project")

	err = stageProject(req.Dir, project, req.Source)
	if err != nil {
		return err
	}

	source, err := projectPath(project, req.Source)
	if err != nil {
		return err
	}

	deps := filepath.Join(req.Cache, dependenciesDir, jsDependenciesDir, nodeModules)

	err = os.Symlink(deps, filepath.Join(project, nodeModules))
	if err != nil {
		return fmt.Errorf("linking npm dependencies: %w", err)
	}

	output := filepath.Join(req.Work, OutputFile)
	options := []string{componentizeCommand, source, "--bundle", "--cwd", project,
		"--wit", wit, jsWorldOption, req.World, outOption, output}

	args := slices.Concat(options, req.Args)

	if !slices.ContainsFunc(req.Args, jsSelectsFeatures) {
		args = append(args, "--disable", "clocks", "http", "random", "fetch-event")
	}

	// ComponentizeJS passes its temporary initializer path to Wizer as
	// part of a whitespace-separated argument string.
	paths, release, err := buildPathAliases(req)
	if err != nil {
		return err
	}

	defer release()

	run.env = append(run.env, "TMPDIR="+filepath.Join(paths.Work, "tmp"))

	err = run.command(ctx, jco, args...)
	if err != nil {
		return err
	}

	return validateComponent(ctx, run, output)
}

func jsSelectsFeatures(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")

	return name == "--debug" || name == "--disable" || name == "--enable" || name == "-d" ||
		strings.HasPrefix(name, "-d") && !strings.HasPrefix(name, "--")
}

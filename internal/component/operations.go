package component

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mandesero/tt-wasm/internal/component/driver"
	"github.com/mandesero/tt-wasm/internal/component/runner"
	"github.com/mandesero/tt-wasm/internal/wasm"
	"github.com/tarantool/tt/sdk"
	"github.com/tarantool/tt/sdk/build"
	"github.com/tarantool/tt/sdk/fetch"
)

const (
	stateName   = ".tt-wasm"
	cacheName   = "cache"
	fetchedName = "fetched"
	stateMode   = 0o755
	markerPerm  = 0o600
)

var (
	errNotFetched             = errors.New("missing or stale dependencies; run tt wasm fetch")
	errUnsafeState            = errors.New("wasm state path is not a real directory")
	errUnknownComponent       = errors.New("no declared Wasm component")
	errUnknownProfileLanguage = errors.New("no Wasm profile for language")
)

type runnerOverrideKey struct{}

// WithRunner overrides the local runner selection for the operation in ctx.
func WithRunner(ctx context.Context, mode string) context.Context {
	if mode == "" {
		return ctx
	}

	return context.WithValue(ctx, runnerOverrideKey{}, mode)
}

type componentState struct {
	pin      profilePin
	settings runner.Settings
	cache    string
	language string
	localSDK bool
}

// Status reports the state of a component without preparing it.
type Status struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Profile  string `json:"profile"`
	Runner   string `json:"runner"`
	Fetched  bool   `json:"fetched"`
	Bindings bool   `json:"bindings"`
	Problem  string `json:"problem,omitempty"`
}

// Inspect reads the component state without creating a core build context or
// preparing a toolchain. checkTools asks the selected runner about its tools.
func Inspect(
	ctx context.Context, project sdk.Project, name string, checkTools bool,
) (Status, error) {
	root, err := project.Root()
	if err != nil {
		return Status{}, fmt.Errorf("finding the wasm project: %w", err)
	}

	manifest, err := project.Manifest()
	if err != nil {
		return Status{}, fmt.Errorf("reading the project manifest: %w", err)
	}

	declared := manifest.Components[name]
	if declared.Build == nil || declared.Build.Backend != Name {
		return Status{}, fmt.Errorf("%w: %s", errUnknownComponent, name)
	}

	decoded, err := declared.Build.Decode(Backend())
	if err != nil {
		return Status{}, fmt.Errorf("component %s: %w", name, err)
	}

	cfg := decoded.(*Config) //nolint:forcetypeassert // Backend.Config returns *Config.
	dir := filepath.Join(root, declared.Path)
	report := Status{Name: name, Language: cfg.Language, Profile: cfg.Profile, Runner: "",
		Fetched: false, Bindings: false, Problem: ""}

	state, err := readComponentState(ctx, root, dir, name, cfg)
	if err == nil {
		report.Runner = state.settings.Mode
		report.Fetched = state.fetched()
		report.Bindings = report.Fetched && bindingCurrent(dir, cfg, state) == nil

		var version string

		version, err = lockedRuntime(project)
		if err == nil {
			err = state.checkRuntime(version)
		}

		if err == nil && checkTools {
			err = runner.Check(ctx, state.settings, cfg.Language)
		}

		if err == nil && !report.Fetched {
			err = errNotFetched
		}
	}

	if err != nil {
		report.Problem = err.Error()
	}

	return report, nil
}

func readComponentState(
	ctx context.Context, project, dir, name string, cfg *Config,
) (componentState, error) {
	locked, local, err := readProfiles(project)
	if err != nil {
		return componentState{}, err
	}

	pin, found := locked.Components[name]
	if !found {
		return componentState{}, fmt.Errorf("%w: %s", errNotPrepared, name)
	}

	profile, err := Profile(pin.Runtime, cfg.Language)
	if err != nil {
		return componentState{}, err
	}

	release, err := lookupRelease(pin.Runtime)
	if err != nil {
		return componentState{}, err
	}

	sdkSum, localSDK := locked.sdkFor(cfg.Language, release)
	if pin.Profile != profile || cfg.Profile != profile || pin.SDK != sdkSum {
		return componentState{}, fmt.Errorf("%w: %s", errProfileChange, name)
	}

	settings := local[cfg.Language]
	if mode, ok := ctx.Value(runnerOverrideKey{}).(string); ok {
		settings.Mode = mode
	}

	switch settings.Mode {
	case "":
		settings.Mode = runner.Docker
	case runner.Docker, runner.Local:
	default:
		return componentState{}, fmt.Errorf("%w: %q", errInvalidRunner, settings.Mode)
	}

	if settings.Mode == runner.Docker && pin.Image == "" {
		return componentState{}, errNoImage
	}

	settings.Image = pin.Image

	key, err := dependencyKey(dir, cfg, pin, settings)
	if err != nil {
		return componentState{}, err
	}

	dir = filepath.Join(dir, stateName)

	err = stateDirectory(dir)
	if err != nil {
		return componentState{}, err
	}

	cache := filepath.Join(dir, cacheName)

	err = stateDirectory(cache)
	if err != nil {
		return componentState{}, err
	}

	return componentState{
		pin: pin, settings: settings, cache: filepath.Join(cache, key), language: cfg.Language,
		localSDK: localSDK,
	}, nil
}

func dependencyKey(dir string, cfg *Config, pin profilePin, settings runner.Settings) (string, error) {
	lockInputs := append([]string{cfg.Source, cfg.WIT}, cfg.Args.Fetch...)

	for _, name := range driver.LockInputs(cfg.Language, cfg.Source) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("reading %s: %w", name, err)
		}

		lockInputs = append(lockInputs, name, fetch.SHA256(data))
	}

	witDir := filepath.Join(dir, cfg.WIT)

	info, err := os.Stat(witDir)
	if err != nil {
		return "", fmt.Errorf("checking the WIT path: %w", err)
	}

	if !info.IsDir() {
		witDir = filepath.Dir(witDir)
	}

	witDeps, err := fileHashes(filepath.Join(witDir, "deps"), "")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	if settings.Mode == runner.Local {
		pin.Image = ""
	}

	record := struct {
		Pin      profilePin        `json:"pin"`
		Runner   string            `json:"runner"`
		Platform string            `json:"platform"`
		Inputs   []string          `json:"inputs"`
		WIT      map[string]string `json:"wit"`
	}{Pin: pin, Runner: settings.Mode, Platform: settings.EffectivePlatform(),
		Inputs: lockInputs, WIT: witDeps}

	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encoding dependencies: %w", err)
	}

	return fetch.SHA256(data), nil
}

// stateDirectory refuses generated state redirected by a symlink. A
// missing directory is allowed because inspect and preflight only read.
func stateDirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("checking %s: %w", path, err)
	case !info.IsDir():
		return fmt.Errorf("%w: %s", errUnsafeState, path)
	default:
		return nil
	}
}

func (s componentState) checkRuntime(version string) error {
	if s.pin.Runtime != version {
		return fmt.Errorf("%w: wasm.lock.json has runtime %s, app.manifest.lock pins %s",
			errProfileChange, s.pin.Runtime, version)
	}

	return nil
}

func (s componentState) fetched() bool {
	if stateDirectory(s.cache) != nil {
		return false
	}

	info, err := os.Stat(filepath.Join(s.cache, fetchedName))

	if err != nil || !info.Mode().IsRegular() {
		return false
	}

	return driver.CacheReady(s.language, s.cache)
}

func stateForBuild(ctx context.Context, c *build.Context, cfg *Config) (componentState, error) {
	return readComponentState(ctx, c.Dirs.Project, c.Dirs.Component, c.Component.Name, cfg)
}

func checkBuild(ctx context.Context, c *build.Context, b *Build) error {
	state, err := stateForBuild(ctx, c, &b.Config)
	if err != nil {
		return err
	}

	// Preflight precedes resolution; a non-locked build may not yet see the
	// selected product's pin. Run checks it once the core has resolved it.
	if version, pinned := c.LockedVersion(wasm.RockName); pinned {
		err = state.checkRuntime(version)
	}

	if err == nil {
		_, err = state.developmentPackages(ctx, c)
	}

	if err == nil && !state.fetched() {
		err = errNotFetched
	}

	return err
}

func operate(
	ctx context.Context, c *build.Context, configured *Build, operation, work string, args []string,
	help bool, stdout, stderr io.Writer,
) error {
	state, err := stateForBuild(ctx, c, &configured.Config)
	if err != nil {
		return err
	}

	version, pinned := c.LockedVersion(wasm.RockName)
	if !pinned && !help {
		return errNoRuntime
	}

	if !help {
		err = state.checkRuntime(version)
		if err != nil {
			return err
		}
	}

	req := driver.Request{
		Language: configured.Language, Operation: operation, Dir: c.Dirs.Component,
		Work: work, Cache: state.cache, Source: configured.Source,
		WIT: configured.WIT, World: configured.World,
		Args: args, BindArgs: configured.Args.Bind, Help: help,
	}

	if help {
		return runner.Run(ctx, state.settings, req, stdout, stderr)
	}

	packages, err := state.developmentPackages(ctx, c)
	if err != nil {
		return err
	}

	if operation == driver.Fetch {
		return fetchDependencies(ctx, state, req, stdout, stderr)
	}

	if !state.fetched() {
		return errNotFetched
	}

	if operation == driver.Build && packages != "" {
		return runner.RunDevelopment(ctx, state.settings, req, packages, stdout, stderr)
	}

	err = runner.Run(ctx, state.settings, req, stdout, stderr)
	if err != nil {
		return err
	}

	if operation == driver.Bind {
		return publishBindings(c.Dirs.Component, &configured.Config, state, work, args)
	}

	return nil
}

func fetchDependencies(
	ctx context.Context, state componentState, req driver.Request, stdout, stderr io.Writer,
) error {
	if state.fetched() && len(req.Args) == 0 {
		return nil
	}

	err := stateDirectory(state.cache)
	if err != nil {
		return err
	}

	parent := filepath.Dir(state.cache)

	err = os.MkdirAll(parent, stateMode)
	if err != nil {
		return fmt.Errorf("creating the Wasm dependency cache: %w", err)
	}

	staged, err := os.MkdirTemp(parent, ".fetch-")
	if err != nil {
		return fmt.Errorf("staging Wasm dependencies: %w", err)
	}

	defer func() { _ = os.RemoveAll(staged) }()

	err = state.fetchSDK(ctx, filepath.Join(staged, "sdk"))
	if err != nil {
		return fmt.Errorf("fetching the Tarantool Wasm SDK: %w", err)
	}

	req.Cache = staged

	err = runner.Run(ctx, state.settings, req, stdout, stderr)
	if err != nil {
		return err
	}

	err = os.WriteFile(filepath.Join(staged, fetchedName), nil, markerPerm)
	if err != nil {
		return fmt.Errorf("recording fetched dependencies: %w", err)
	}

	return replaceTree(state.cache, staged)
}

func (s componentState) fetchSDK(ctx context.Context, dir string) error {
	fetcher := wasm.Fetcher{CacheDir: wasm.DefaultCacheDir(), Client: nil}
	if s.localSDK {
		return fetcher.FetchLocalSDK(s.pin.SDK, dir)
	}

	release, err := lookupRelease(s.pin.Runtime)
	if err != nil {
		return err
	}

	return fetcher.FetchSDK(ctx, release, dir)
}

// replaceTree publishes a complete tree under the project lock, retaining
// the previous tree if publishing fails.
func replaceTree(target, staged string) error {
	parent := filepath.Dir(target)

	backup, err := os.MkdirTemp(parent, ".previous-")
	if err != nil {
		return fmt.Errorf("staging the previous %s: %w", target, err)
	}

	old := filepath.Join(backup, "previous")

	err = os.Rename(target, old)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(backup)

		return fmt.Errorf("moving %s aside: %w", target, err)
	}

	existed := err == nil

	err = os.Rename(staged, target)
	if err == nil {
		_ = os.RemoveAll(backup)

		return nil
	}

	if existed {
		restore := os.Rename(old, target)
		if restore != nil {
			return errors.Join(fmt.Errorf("publishing %s: %w", target, err),
				fmt.Errorf("restoring %s from %s: %w", target, old, restore))
		}
	}

	_ = os.Remove(backup)

	return fmt.Errorf("publishing %s: %w", target, err)
}

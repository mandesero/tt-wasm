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
	"slices"
	"strconv"
	"strings"

	"github.com/mandesero/tt-wasm/internal/component/driver"
	"github.com/mandesero/tt-wasm/internal/component/runner"
	"github.com/mandesero/tt-wasm/internal/wasm"
	"github.com/tarantool/tt/sdk"
)

const (
	lockName  = "wasm.lock.json"
	localName = "wasm.local.json"
	lockPerm  = 0o644
	localPerm = 0o600
)

var (
	errNoRuntime     = errors.New("app.manifest.lock needs a wasm runtime pin; run tt wasm prepare")
	errNoImage       = errors.New("the Docker image is not set; run tt wasm prepare with --image")
	errNotPrepared   = errors.New("the component profile is not prepared; run tt wasm prepare")
	errProfileChange = errors.New("the locked profile changed; run tt wasm prepare --update")
	errInvalidRunner = errors.New("runner must be local or docker")
)

// PrepareOptions select the runtime and the local toolchain.
type PrepareOptions struct {
	Version  string
	Language string
	Runner   string
	Platform string
	Context  string
	Image    string
	SDKFrom  string
	Update   bool
}

type profilePin struct {
	Profile string `json:"profile"`
	Runtime string `json:"runtime"`
	SDK     string `json:"sdk_sha256"`
	Image   string `json:"image,omitempty"`
}

type profileLock struct {
	Components map[string]profilePin `json:"components"`
	LocalSDKs  map[string]string     `json:"local_sdks,omitempty"`
}

type localRunners map[string]runner.Settings

// Profile names the supported profile of a component. version may include
// the LuaRocks revision recorded in app.manifest.lock.
func Profile(version, language string) (string, error) {
	release, err := lookupRelease(version)
	if err != nil {
		return "", err
	}

	if !slices.Contains(driver.Languages(), language) {
		return "", fmt.Errorf("%w: %s", errUnknownProfileLanguage, language)
	}

	return language + "-ttwasm-" + release.Version + "-r1", nil
}

func lookupRelease(version string) (wasm.Release, error) {
	release, err := wasm.LookupRelease(version)
	if err == nil {
		return release, nil
	}

	base, revision, found := strings.CutLast(version, "-")
	if found {
		_, revisionErr := strconv.ParseUint(revision, 10, 32)
		if revisionErr == nil {
			return wasm.LookupRelease(base)
		}
	}

	return wasm.Release{}, err
}

// Prepare makes the selected toolchains available and pins the profiles of
// the declared components. The runtime is resolved by tt before this call.
func Prepare(
	ctx context.Context, project sdk.Project, opts PrepareOptions, stdout, stderr io.Writer,
) error {
	ctx, unlock, err := project.Lock(ctx)
	if err != nil {
		return fmt.Errorf("locking the wasm project: %w", err)
	}

	defer unlock()

	root, err := project.Root()
	if err != nil {
		return fmt.Errorf("finding the wasm project: %w", err)
	}

	manifest, err := project.Manifest()
	if err != nil {
		return fmt.Errorf("reading the project manifest: %w", err)
	}

	components, languages, err := configuredComponents(manifest, opts.Language)
	if err != nil {
		return err
	}

	if len(languages) == 0 {
		if opts.SDKFrom != "" {
			return errors.New("--sdk-from requires --lang or a declared Wasm component")
		}

		return nil
	}

	version, err := lockedRuntime(project)
	if err != nil {
		return err
	}

	release, err := lookupRelease(version)
	if err != nil {
		return err
	}

	if opts.Version != "" && opts.Version != release.Version && opts.Version != version {
		return fmt.Errorf("%w: requested runtime %s, app.manifest.lock pins %s",
			errProfileChange, opts.Version, version)
	}

	lock, local, err := readProfiles(root)
	if err != nil {
		return err
	}

	if opts.SDKFrom != "" {
		err = lock.prepareLocalSDK(opts, languages)
		if err != nil {
			return err
		}
	}

	for _, language := range languages {
		profile, err := Profile(version, language)
		if err != nil {
			return err
		}

		settings, image, err := prepareSettings(ctx, local[language], lock, components,
			language, opts, stdout, stderr)
		if err != nil {
			return err
		}

		local[language] = settings
		sdkSum, _ := lock.sdkFor(language, release)

		for name, cfg := range components {
			if cfg.Language != language {
				continue
			}

			if cfg.Profile != profile {
				return fmt.Errorf("component %s: %w: manifest names %q, runtime requires %q",
					name, errProfileChange, cfg.Profile, profile)
			}

			pin := profilePin{
				Profile: profile, Runtime: version, SDK: sdkSum, Image: image,
			}

			old := lock.Components[name]
			if pin.Image == "" {
				pin.Image = old.Image
			}

			if old.Image == "" {
				old.Image = pin.Image
			}

			if old.Profile != "" && !opts.Update && old != pin {
				return fmt.Errorf("component %s: %w", name, errProfileChange)
			}

			lock.Components[name] = pin
		}
	}

	if len(components) != 0 || opts.SDKFrom != "" {
		err = writeJSON(filepath.Join(root, lockName), lock, lockPerm)
		if err != nil {
			return err
		}
	}

	return writeJSON(filepath.Join(root, localName), local, localPerm)
}

func (lock *profileLock) prepareLocalSDK(opts PrepareOptions, languages []string) error {
	fetcher := wasm.Fetcher{CacheDir: wasm.DefaultCacheDir()}
	sum, err := fetcher.ImportSDK(opts.SDKFrom)
	if err != nil {
		return err
	}

	return lock.pinLocalSDK(sum, languages, opts.Update)
}

func (lock *profileLock) pinLocalSDK(sum string, languages []string, update bool) error {
	if !update {
		for _, language := range languages {
			if old, pinned := lock.LocalSDKs[language]; pinned && old != sum {
				return fmt.Errorf("%s SDK: %w", language, errProfileChange)
			}
		}
	}

	if lock.LocalSDKs == nil {
		lock.LocalSDKs = make(map[string]string)
	}

	for _, language := range languages {
		lock.LocalSDKs[language] = sum
	}

	return nil
}

func (lock profileLock) sdkFor(language string, release wasm.Release) (sum string, local bool) {
	if sum, local := lock.LocalSDKs[language]; local {
		return sum, true
	}

	return release.SDKSHA256, false
}

func configuredComponents(
	manifest sdk.Manifest, language string,
) (map[string]*Config, []string, error) {
	components := make(map[string]*Config)
	languages := make(map[string]bool)

	if language != "" {
		languages[language] = true
	}

	outputs := make(map[string]string)

	for name, component := range manifest.Components {
		if component.Build == nil || component.Build.Backend != Name {
			continue
		}

		decoded, err := component.Build.Decode(Backend())
		if err != nil {
			return nil, nil, fmt.Errorf("component %s: %w", name, err)
		}

		cfg := decoded.(*Config) //nolint:forcetypeassert // Backend.Config returns *Config.

		if len(component.Build.Output) == 1 {
			output := filepath.Join(component.Namespace, component.Build.Output[0].Path)
			if other, found := outputs[output]; found {
				return nil, nil, fmt.Errorf("%w: components %s and %s both produce %s",
					errOutputConflict, other, name, output)
			}

			outputs[output] = name
		}

		if language == "" || cfg.Language == language {
			languages[cfg.Language] = true
			components[name] = cfg
		}
	}

	selected := make([]string, 0, len(languages))
	for name := range languages {
		selected = append(selected, name)
	}

	slices.Sort(selected)

	return components, selected, nil
}

var errOutputConflict = errors.New("conflicting Wasm outputs")

func prepareSettings(
	ctx context.Context, settings runner.Settings, lock profileLock, components map[string]*Config,
	language string, opts PrepareOptions, stdout, stderr io.Writer,
) (runner.Settings, string, error) {
	if opts.Runner != "" {
		settings.Mode = opts.Runner
	}

	if settings.Mode == "" {
		settings.Mode = runner.Docker
	}

	if opts.Platform != "" {
		settings.Platform = opts.Platform
	}

	if opts.Context != "" {
		settings.Context = opts.Context
	}

	if opts.Image != "" {
		settings.Image = opts.Image
	}

	if settings.Mode == runner.Docker && settings.Image == "" {
		for name, cfg := range components {
			if cfg.Language == language && lock.Components[name].Image != "" {
				settings.Image = lock.Components[name].Image

				break
			}
		}

		if settings.Image == "" {
			return settings, "", errNoImage
		}
	}

	err := runner.Prepare(ctx, settings, language, stdout, stderr)
	if err != nil {
		return settings, "", fmt.Errorf("preparing %s: %w", language, err)
	}

	if settings.Mode == runner.Local {
		return settings, "", nil
	}

	if !strings.Contains(settings.Image, "@sha256:") {
		settings.Image, err = runner.ImageID(ctx, settings)
		if err != nil {
			return settings, "", err
		}
	}

	return settings, settings.Image, nil
}

func lockedRuntime(project sdk.Project) (string, error) {
	lock, err := project.ReadLock()
	if err != nil {
		return "", fmt.Errorf("reading the runtime pin: %w", err)
	}

	runtime, pinned := lock.Locked(wasm.RockName)
	if !lock.Fresh || !pinned {
		return "", errNoRuntime
	}

	return runtime.Version, nil
}

func readProfiles(project string) (profileLock, localRunners, error) {
	lock := profileLock{Components: nil, LocalSDKs: nil}

	err := readOptionalJSON(filepath.Join(project, lockName), &lock)
	if err != nil {
		return profileLock{}, nil, err
	}

	if lock.Components == nil {
		lock.Components = make(map[string]profilePin)
	}

	var local localRunners

	err = readOptionalJSON(filepath.Join(project, localName), &local)
	if local == nil {
		local = make(localRunners)
	}

	return lock, local, err
}

func readOptionalJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err == nil {
		err = json.Unmarshal(data, target)
	}

	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	return nil
}

func writeJSON(path string, value any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}

	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	defer func() { _ = os.Remove(file.Name()) }()

	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Chmod(mode)
	}

	err = errors.Join(err, file.Close())
	if err == nil {
		err = os.Rename(file.Name(), path)
	}

	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

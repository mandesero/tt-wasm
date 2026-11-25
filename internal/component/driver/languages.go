package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	// CLanguage is the language name of the C driver.
	CLanguage = "c"
	// CPP is the language name of the C++ driver.
	CPP = "cpp"
	// CSharp is the language name of the C# driver.
	CSharp = "csharp"
	// GoLanguage is the language name of the TinyGo driver.
	GoLanguage = "go"
	// JavaScript is the language name of the JavaScript driver.
	JavaScript = "js"
	// Rust is the language name of the Rust driver.
	Rust = "rust"

	helpOption          = "--help"
	versionOption       = "--version"
	worldOption         = "--world"
	outputOption        = "--output"
	outDirOption        = "--out-dir"
	outOption           = "--out"
	configOption        = "--config"
	targetOption        = "--target"
	componentCommand    = "component"
	componentizeCommand = "componentize"
	bindingsCommand     = "bindings"
)

type phase func(context.Context, executor, Request) error

type languageDriver struct {
	fetch   phase
	bind    phase
	build   phase
	check   func(context.Context) error
	help    func(string) []string
	managed func(string) []string
	locks   []string
	cache   []string
}

// Languages returns the supported manifest language names.
func Languages() []string {
	return []string{CLanguage, CPP, CSharp, GoLanguage, JavaScript, Python, Rust}
}

func driverFor(language string) (languageDriver, error) {
	switch language {
	case Python:
		return pythonDriver(), nil
	case CLanguage:
		return cDriver(), nil
	case CPP:
		return cppDriver(), nil
	case CSharp:
		return csharpDriver(), nil
	case GoLanguage:
		return goDriver(), nil
	case JavaScript:
		return jsDriver(), nil
	case Rust:
		return rustDriver(), nil
	default:
		return languageDriver{}, fmt.Errorf("%w: %q", errLanguage, language)
	}
}

func pythonDriver() languageDriver {
	return languageDriver{
		fetch: fetchPython, bind: bindPython, build: buildPython, check: checkPython,
		help: func(operation string) []string {
			switch operation {
			case Fetch:
				return []string{wkg, Fetch, helpOption}
			case Bind:
				return []string{componentize, bindingsCommand, helpOption}
			case Build:
				return []string{componentize, componentizeCommand, helpOption}
			default:
				return nil
			}
		},
		managed: func(string) []string {
			return []string{"--wit-path", worldOption, "--world-module", "--python-path",
				"--module-worlds", "--intersect-world", outputOption, "--stub-wasi",
				"-d", "-w", "-p", "-m", "-i", "-o", "-s"}
		},
		locks: []string{"requirements.lock"},
		cache: []string{"dependencies/python-sdk", PythonPackagesDir},
	}
}

// LockInputs returns the component-relative dependency files of the language.
func LockInputs(language, source string) []string {
	driver, err := driverFor(language)
	if err != nil {
		return nil
	}

	inputs := append([]string{"wkg.lock"}, driver.locks...)
	if language != CSharp || !filepath.IsLocal(source) {
		return inputs
	}

	inputs = append(inputs, source)
	for dir := filepath.Dir(filepath.Clean(source)); dir != "."; dir = filepath.Dir(dir) {
		for _, name := range driver.locks {
			inputs = append(inputs, filepath.Join(dir, name))
		}
	}

	return inputs
}

// CacheReady checks the paths a complete driver fetch leaves in cache.
func CacheReady(language, cache string) bool {
	driver, err := driverFor(language)
	if err != nil {
		return false
	}

	for _, name := range slices.Concat([]string{"sdk", "dependencies/wit"}, driver.cache) {
		info, err := os.Lstat(filepath.Join(cache, name))
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return false
		}
	}

	return true
}

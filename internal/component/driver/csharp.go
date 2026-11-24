package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	runtimeOption        = "--runtime"
	dotnetTool           = "dotnet"
	dotnetSDK            = "10.0.402"
	csharpWASI           = "29.0"
	csharpBindgen        = "wit-bindgen"
	csharpBindgenVersion = "0.58.0"

	nugetLock     = "packages.lock.json"
	nugetConfig   = "nuget.config"
	nugetPackages = "nuget"
)

var (
	errCSharpPlatform = errors.New("the C# toolchain needs Linux amd64; use Docker")
	errCSharpSDK      = errors.New("WASI_SDK_PATH must name WASI SDK " + csharpWASI)
	errCSharpProject  = errors.New("source must be a C# project (.csproj file)")
	errCSharpLock     = errors.New("locked NuGet restore changed " + nugetLock)
	errCSharpOutput   = errors.New("expected one C# WebAssembly component")
)

func csharpDriver() languageDriver {
	return languageDriver{
		fetch:   fetchCSharp,
		bind:    bindCSharp,
		build:   buildCSharp,
		check:   checkCSharp,
		help:    csharpHelp,
		managed: csharpManaged,
		locks: []string{
			nugetLock, nugetConfig, "global.json", "Directory.Packages.props",
			"Directory.Build.props", "Directory.Build.targets",
		},
		cache: []string{nugetPackages},
	}
}

func checkCSharp(ctx context.Context) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errCSharpPlatform
	}

	_, err := csharpWASISDK()
	if err != nil {
		return err
	}

	return checkTools(ctx,
		toolVersion{name: dotnetTool, version: dotnetSDK, args: nil},
		toolVersion{name: csharpBindgen, version: csharpBindgenVersion, args: nil},
	)
}

func csharpHelp(operation string) []string {
	switch operation {
	case Fetch:
		return []string{dotnetTool, "restore", helpOption}
	case Bind:
		return []string{csharpBindgen, "c-sharp", helpOption}
	case Build:
		return []string{dotnetTool, Build, helpOption}
	default:
		return nil
	}
}

func csharpManaged(operation string) []string {
	switch operation {
	case Bind:
		return []string{
			worldOption, runtimeOption, outDirOption, "--with-wit-results", "--generate-stub",
		}
	case Fetch:
		return []string{
			"--source", "-s", "--packages", "--configfile", "--use-lock-file", "--locked-mode",
		}
	default:
		return []string{
			outputOption, "-o", "--configuration", "-c", runtimeOption, "-r", "--framework", "-f",
		}
	}
}

func csharpWASISDK() (string, error) {
	path := os.Getenv("WASI_SDK_PATH")
	if !filepath.IsAbs(path) {
		return "", errCSharpSDK
	}

	root, err := os.OpenRoot(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCSharpSDK, err)
	}

	defer func() { _ = root.Close() }()

	data, err := root.ReadFile("VERSION")
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCSharpSDK, err)
	}

	version, _, _ := strings.Cut(string(data), "\n")
	if strings.TrimSpace(version) != csharpWASI {
		return "", fmt.Errorf("%w, found %q", errCSharpSDK, version)
	}

	info, err := root.Stat("bin/clang")
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCSharpSDK, err)
	}

	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errCSharpSDK
	}

	return path, nil
}

func fetchCSharp(ctx context.Context, run executor, req Request) error {
	err := checkCSharpArgs(Fetch, req.Args)
	if err != nil {
		return err
	}

	witReq := req

	witReq.Args = nil

	err = fetchWIT(ctx, run, witReq, filepath.Join(req.Cache, dependenciesDir))
	if err != nil {
		return err
	}

	project, err := stageCSharp(run, req)
	if err != nil {
		return err
	}

	return project.restore(ctx, true, req.Args)
}

func bindCSharp(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	return generateCSharp(ctx, run, req.World, wit, filepath.Join(req.Work, BindingsDir), req.Args)
}

func generateCSharp(
	ctx context.Context, run executor, world, wit, output string, args []string,
) error {
	err := checkCSharpArgs(Bind, args)
	if err != nil {
		return err
	}

	options := []string{"c-sharp", wit, worldOption, world, runtimeOption, "native-aot",
		"--with-wit-results", outDirOption, output}

	return run.command(ctx, csharpBindgen, slices.Concat(options, args)...)
}

func buildCSharp(ctx context.Context, run executor, req Request) error {
	err := checkCSharpArgs(Build, req.Args)
	if err != nil {
		return err
	}

	req, release, err := csharpBuildPaths(req)
	if err != nil {
		return err
	}

	defer release()

	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	bindings := filepath.Join(req.Work, BindingsDir)

	err = generateCSharp(ctx, run, req.World, wit, bindings, req.BindArgs)
	if err != nil {
		return err
	}

	project, err := stageCSharp(run, req)
	if err != nil {
		return err
	}

	err = project.restore(ctx, false, nil)
	if err != nil {
		return err
	}

	publish := filepath.Join(req.Work, "publish")
	options := []string{
		Build, project.source, "--no-restore", "--disable-build-servers", "-maxcpucount:1",
		"-p:BuildInParallel=false", "-p:WasiSdkRoot=" + project.wasi,
		"-p:Configuration=Release", "-p:PublishDir=" + publish + "/",
		"-p:TTWasmBindingsDir=" + bindings,
	}

	err = project.run.command(ctx, dotnetTool, slices.Concat(options, req.Args)...)
	if err != nil {
		return err
	}

	outputs, err := filepath.Glob(filepath.Join(publish, "*.wasm"))
	if err != nil {
		return fmt.Errorf("looking for C# build output: %w", err)
	}

	if len(outputs) != 1 {
		return fmt.Errorf("%w in %s, found %d", errCSharpOutput, publish, len(outputs))
	}

	err = validateComponent(ctx, run, outputs[0])
	if err != nil {
		return err
	}

	err = os.Rename(outputs[0], filepath.Join(req.Work, OutputFile))
	if err != nil {
		return fmt.Errorf("publishing C# component: %w", err)
	}

	return nil
}

// NativeAOT expands an unquoted response-file path. Docker already uses
// /work; local projects with spaces need a short alias for the same files.
func csharpBuildPaths(req Request) (Request, func(), error) {
	if !strings.ContainsAny(req.Work+req.Cache, " \t") {
		return req, func() {}, nil
	}

	dir, err := os.MkdirTemp("/tmp", "tt-wasm-csharp-*")
	if err != nil {
		return Request{}, nil, fmt.Errorf("creating C# work alias: %w", err)
	}

	release := func() { _ = os.RemoveAll(dir) }
	work, cache := filepath.Join(dir, "work"), filepath.Join(dir, "cache")

	err = os.Symlink(req.Work, work)
	if err == nil {
		err = os.Symlink(req.Cache, cache)
	}

	if err != nil {
		release()

		return Request{}, nil, fmt.Errorf("creating C# work alias: %w", err)
	}

	req.Work = work
	req.Cache = cache

	return req, release, nil
}

type csharpProject struct {
	run    executor
	source string
	lock   string
	config string
	cache  string
	wasi   string
}

func stageCSharp(run executor, req Request) (csharpProject, error) {
	wasi, err := csharpWASISDK()
	if err != nil {
		return csharpProject{}, err
	}

	source, err := projectPath(req.Dir, req.Source)
	if err != nil {
		return csharpProject{}, err
	}

	if filepath.Ext(source) != ".csproj" {
		return csharpProject{}, fmt.Errorf("%w: %s", errCSharpProject, req.Source)
	}

	project := filepath.Join(req.Work, "project")
	sourceDir := filepath.Dir(req.Source)

	err = stageProject(req.Dir, project, req.Source,
		filepath.ToSlash(filepath.Join(sourceDir, "bin")),
		filepath.ToSlash(filepath.Join(sourceDir, "obj")))
	if err != nil {
		return csharpProject{}, err
	}

	source = filepath.Join(project, req.Source)
	run.dir = filepath.Dir(source)
	run.env = append(slices.Clone(run.env),
		"TMPDIR="+filepath.Join(req.Work, "tmp"),
		"DOTNET_CLI_HOME="+filepath.Join(req.Work, "home"),
		"DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1", "MSBuildEnableWorkloadResolver=false",
		"NUGET_PACKAGES="+filepath.Join(req.Cache, nugetPackages),
		"NUGET_HTTP_CACHE_PATH="+filepath.Join(req.Work, "http-cache"),
	)

	return csharpProject{
		run: run, source: source,
		lock:   filepath.Join(run.dir, nugetLock),
		config: filepath.Join(run.dir, nugetConfig), cache: req.Cache, wasi: wasi,
	}, nil
}

// restore creates project.assets.json in the disposable project. The
// disconnected build supplies only the already installed local NuGet cache.
func (p csharpProject) restore(ctx context.Context, online bool, args []string) error {
	lock, err := os.ReadFile(p.lock)
	if err != nil {
		return fmt.Errorf("reading C# dependency lock: %w", err)
	}

	err = p.checkSDK(ctx)
	if err != nil {
		return err
	}

	options := []string{"restore", p.source, "--locked-mode", "--disable-build-servers",
		"-p:NuGetLockFilePath=" + p.lock, "-p:NuGetAudit=false", "-p:WasiSdkRoot=" + p.wasi,
		"-p:Configuration=Release"}

	if online {
		feed := filepath.Join(p.cache, sdkDir, "packages", CSharp)

		options = append(options, "--configfile", p.config,
			"-p:RestoreAdditionalProjectSources="+feed)
	} else {
		options = append(options, "--source", filepath.Join(p.cache, nugetPackages),
			"-p:RestoreAdditionalProjectSources=", "-p:RestoreFallbackFolders=")
	}

	err = p.run.command(ctx, dotnetTool, append(options, args...)...)
	if err != nil {
		return err
	}

	after, err := os.ReadFile(p.lock)
	if err != nil {
		return fmt.Errorf("%w: %w", errCSharpLock, err)
	}

	if !bytes.Equal(lock, after) {
		return errCSharpLock
	}

	return nil
}

func (p csharpProject) checkSDK(ctx context.Context) error {
	var version bytes.Buffer

	run := p.run

	run.stdout = &version

	err := run.command(ctx, dotnetTool, versionOption)
	if err != nil {
		return err
	}

	got := strings.TrimSpace(version.String())
	if got != dotnetSDK {
		return fmt.Errorf("%w: project selects .NET SDK %s, want %s", errTool, got, dotnetSDK)
	}

	return nil
}

// checkCSharpArgs limits arguments to binding visibility and build
// diagnostics. MSBuild properties and response files can override an
// otherwise locked restore or redirect its output outside the work tree.
func checkCSharpArgs(operation string, args []string) error {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if operation == Bind && arg == "--internal" {
			continue
		}

		if operation != Bind && (arg == "-v" || arg == "--verbosity") &&
			index+1 < len(args) && csharpVerbosity(args[index+1]) {
			index++

			continue
		}

		if operation != Bind {
			level, ok := strings.CutPrefix(arg, "--verbosity=")
			if !ok {
				level, ok = strings.CutPrefix(arg, "-v:")
			}

			if ok && csharpVerbosity(level) {
				continue
			}
		}

		if operation == Build && arg == "--no-incremental" {
			continue
		}

		return fmt.Errorf("%w: %q", errToolArgs, arg)
	}

	return nil
}

func csharpVerbosity(level string) bool {
	switch level {
	case "q", "quiet", "m", "minimal", "n", "normal", "d", "detailed", "diag", "diagnostic":
		return true
	default:
		return false
	}
}

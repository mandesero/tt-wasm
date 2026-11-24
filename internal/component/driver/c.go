package driver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	witBindgen = "wit-bindgen"
	wasiCC     = "wasm32-wasip2-clang"
	wasiCXX    = "wasm32-wasip2-clang++"
	wasiLLVM   = "23.1.0"
)

var errCBindings = errors.New("wit-bindgen did not generate exactly one C source")

func cDriver() languageDriver {
	return cFamilyDriver(wasiCC)
}

func cppDriver() languageDriver {
	return cFamilyDriver(wasiCXX)
}

func cFamilyDriver(compiler string) languageDriver {
	return languageDriver{
		fetch: func(ctx context.Context, run executor, req Request) error {
			return fetchWIT(ctx, run, req, filepath.Join(req.Cache, dependenciesDir))
		},
		bind: generateCBindings,
		build: func(ctx context.Context, run executor, req Request) error {
			return buildCComponent(ctx, run, req, compiler)
		},
		check: func(ctx context.Context) error {
			tools := []toolVersion{
				{name: witBindgen, version: "0.62.0", args: nil},
				{name: wasiCC, version: wasiLLVM, args: nil},
			}
			if compiler == wasiCXX {
				tools = append(tools, toolVersion{name: wasiCXX, version: wasiLLVM, args: nil})
			}

			return checkTools(ctx, tools...)
		},
		help: func(op string) []string {
			switch op {
			case Fetch:
				return []string{wkg, Fetch, helpOption}
			case Bind:
				return []string{witBindgen, CLanguage, helpOption}
			case Build:
				return []string{compiler, helpOption}
			default:
				return nil
			}
		},
		managed: managedCArgs,
		locks:   nil,
		cache:   nil,
	}
}

func managedCArgs(op string) []string {
	switch op {
	case Bind:
		return []string{
			worldOption, "-w", outDirOption, "--rename-world", "--check", "--no-object-file",
		}
	case Build:
		return []string{
			"-o", "-x", "-target", targetOption, "-mexec-model", "-fuse-ld", "--sysroot",
			"-isysroot", configOption, "-c", "-E", "-S", "--analyze", "-fsyntax-only",
			"-shared", "-r", "-Xclang", "-Xlinker",
		}
	default:
		// Fetch arguments belong to the shared WIT fetch operation.
		return nil
	}
}

func generateCBindings(ctx context.Context, run executor, req Request) error {
	wit, err := buildWIT(req)
	if err != nil {
		return err
	}

	args := append([]string{
		CLanguage, worldOption, req.World, outDirOption, filepath.Join(req.Work, BindingsDir),
	}, req.Args...)

	return run.command(ctx, witBindgen, append(args, "--", wit)...)
}

func buildCComponent(ctx context.Context, run executor, req Request, compiler string) error {
	source, err := projectPath(req.Dir, req.Source)
	if err != nil {
		return err
	}

	switch filepath.Ext(source) {
	case ".c":
		if compiler != wasiCC {
			return fmt.Errorf("%w: expected C++ source, got %s", errPath, req.Source)
		}
	case ".cpp", ".cxx", ".cc", ".C":
		if compiler != wasiCXX {
			return fmt.Errorf("%w: expected C source, got %s", errPath, req.Source)
		}
	default:
		return fmt.Errorf("%w: unsupported C/C++ file %s", errPath, req.Source)
	}

	bind := req

	bind.Args = req.BindArgs

	err = generateCBindings(ctx, run, bind)
	if err != nil {
		return err
	}

	dir := filepath.Join(req.Work, BindingsDir)

	generated, err := fs.Glob(os.DirFS(dir), "*.c")
	if err != nil {
		return fmt.Errorf("finding C bindings: %w", err)
	}

	if len(generated) != 1 {
		return errCBindings
	}

	stem := strings.TrimSuffix(generated[0], ".c")
	wrapper := filepath.Join(dir, generated[0])
	metadata := filepath.Join(dir, stem+"_component_type.o")

	if compiler == wasiCXX {
		// The generated wrapper is C. Link its object with the C++ source.
		obj := filepath.Join(req.Work, "bindings.o")

		err = run.command(ctx, wasiCC, "-c", wrapper, "-o", obj)
		if err != nil {
			return err
		}

		wrapper = obj
	}

	output := filepath.Join(req.Work, OutputFile)
	args := append([]string{"-mexec-model=reactor", "-I", dir}, req.Args...)

	err = run.command(ctx, compiler, append(args, "-o", output, "--", source, wrapper, metadata)...)
	if err != nil {
		return err
	}

	return validateComponent(ctx, run, output)
}

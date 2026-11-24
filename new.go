package wasm

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/mandesero/tt-wasm/internal/component"
	"github.com/mandesero/tt-wasm/internal/component/driver"
	"github.com/mandesero/tt-wasm/internal/wasm"
	"github.com/tarantool/tt/sdk"
)

var (
	errComponentExists = errors.New("component already declared")
	errRuntimeNotReady = errors.New("the runtime must be locked: run tt wasm prepare")
	componentName      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	witName            = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*$`)
)

//go:embed templates
var templates embed.FS

const (
	scaffoldDirMode  = 0o755
	scaffoldFileMode = 0o644
)

type newCmd struct {
	services   sdk.Services
	language   string
	dir        string
	world      string
	template   string
	registries []string
}

func newNewCmd(services sdk.Services) *cobra.Command {
	add := &newCmd{
		services: services, language: "", dir: "", world: "", template: "", registries: nil,
	}

	cmd := &cobra.Command{
		Use:   "new NAME",
		Short: "Create a WebAssembly component and declare it in the package",
		Args:  cobra.ExactArgs(1),
		RunE:  add.run,
	}

	flags := cmd.Flags()
	flags.StringVar(&add.language, "lang", "",
		"the language of the component: "+strings.Join(driver.Languages(), ", "))
	flags.StringVar(&add.dir, "dir", "", "the directory of the component (default components/NAME)")
	flags.StringVar(&add.world, "world", "", "the WIT world of the component (default NAME)")
	flags.StringVar(&add.template, "template", "", "start from the Python call or http example")
	flags.StringArrayVar(&add.registries, "registry", nil, sdk.RegistryFlagUsage)

	_ = cmd.MarkFlagRequired("lang")

	return cmd
}

func (c *newCmd) run(cmd *cobra.Command, args []string) error {
	name := args[0]
	if !componentName.MatchString(name) {
		return sdk.Errorf(sdk.ExitFailure, "invalid component name %q: use letters, digits, - or _",
			name)
	}

	world := c.world
	if world == "" {
		world = name
	}

	if !witName.MatchString(world) {
		return sdk.Errorf(sdk.ExitFailure, "invalid WIT world %q: name one with --world", world)
	}

	dir := c.dir
	if dir == "" {
		dir = filepath.Join("components", name)
	}

	if !filepath.IsLocal(dir) || filepath.Clean(dir) == "." {
		return sdk.Errorf(sdk.ExitFailure, "component directory %q must be inside the project", dir)
	}

	project := c.services.Project()

	ctx, release, err := project.Lock(cmd.Context())
	if err != nil {
		return err
	}
	defer release()

	projectDir, err := project.Root()
	if err != nil {
		return err
	}

	man, err := readManifest(project, projectDir)
	if err != nil {
		return err
	}

	if _, exists := man.Components[name]; exists {
		return fmt.Errorf("%w: %s", errComponentExists, name)
	}

	lock, err := project.ReadLock()
	if err != nil {
		return fmt.Errorf("%w: %w", errRuntimeNotReady, err)
	}

	runtime, ok := lock.Locked(wasm.RockName)
	if !ok || !lock.Fresh {
		return errRuntimeNotReady
	}

	profile, err := component.Profile(runtime.Version, c.language)
	if err != nil {
		return err
	}

	dir = filepath.Clean(dir)

	output := "dist/" + name + ".wasm"

	scaffold, err := newScaffold(c.language, c.template, world, output)
	if err != nil {
		return err
	}

	declaration := componentDeclaration(name, dir, world, c.language, profile, output)

	err = addComponent(ctx, project, projectDir, declaration, scaffold)
	if err != nil {
		return err
	}

	change, err := project.Resolve(ctx, sdk.ResolveOptions{Registries: c.registries})
	if err != nil {
		return fmt.Errorf("component %s is declared, but the lock could not be resolved: %w",
			name, err)
	}

	out := c.services.Streams().IO().Out

	printMoves(out, change.Moves)

	_, _ = fmt.Fprintf(out, "added component %s in %s\n", name, dir)

	if len(scaffold.module) != 0 {
		_, _ = fmt.Fprintln(out, "\nPlace this Lua module at the root of the package namespace.")
		_, _ = fmt.Fprintf(out, "\n%s", scaffold.module)
	}

	return nil
}

func componentDeclaration(name, dir, world, language, profile, output string) sdk.Component {
	return sdk.Component{
		Name:    name,
		Comment: "A WebAssembly component, made by tt wasm new.",
		Path:    filepath.ToSlash(dir),
		Include: nil,
		Build: &sdk.ComponentBuild{Backend: component.Name, Fields: []sdk.BuildField{
			{Key: "language", Value: language},
			{Key: "source", Value: componentSource(language)},
			{Key: "wit", Value: "wit"},
			{Key: "world", Value: world},
			{Key: "profile", Value: profile},
			{Key: "output", Value: output},
		}},
	}
}

// addComponent makes the scaffold and declares it under the same project lock.
// A failed declaration only removes the directory this call just created.
func addComponent(ctx context.Context, project sdk.Project, projectDir string,
	declaration sdk.Component, scaffold scaffoldFiles,
) error {
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return fmt.Errorf("opening the project: %w", err)
	}

	defer func() { _ = root.Close() }()

	dir := filepath.FromSlash(declaration.Path)

	err = root.MkdirAll(filepath.Dir(dir), scaffoldDirMode)
	if err == nil {
		err = root.Mkdir(dir, scaffoldDirMode)
	}

	if err != nil {
		return fmt.Errorf("creating component directory %q: %w", dir, err)
	}

	err = writeScaffold(root, dir, scaffold)
	if err == nil {
		err = project.AddComponent(ctx, declaration)
	}

	if err != nil {
		err = errors.Join(err, root.RemoveAll(dir))
	}

	return err
}

type scaffoldFiles struct {
	files  map[string][]byte
	module []byte
}

func componentSource(language string) string {
	switch language {
	case driver.CLanguage:
		return "main.c"
	case driver.CPP:
		return "main.cpp"
	case driver.CSharp:
		return "Component.csproj"
	case driver.GoLanguage:
		return "."
	case driver.JavaScript:
		return "main.js"
	case driver.Rust:
		return "src/lib.rs"
	default:
		return "main.py"
	}
}

func newScaffold(language, name, world, output string) (scaffoldFiles, error) {
	switch {
	case name == "":
		name = "default"
	case language == driver.Python && (name == "call" || name == "http"):
	default:
		return scaffoldFiles{}, sdk.Errorf(sdk.ExitFailure,
			"unknown %s template %q", language, name)
	}

	dir := path.Join("templates", language, name)
	scaffold := scaffoldFiles{files: make(map[string][]byte), module: nil}
	values := struct {
		Package     string
		World       string
		WorldSnake  string
		WorldPascal string
		Output      string
	}{
		Package: "example:" + world + "@0.1.0", World: world,
		WorldSnake:  strings.ReplaceAll(world, "-", "_"),
		WorldPascal: pascalWorld(world), Output: output,
	}

	err := fs.WalkDir(templates, dir, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		relative := strings.TrimSuffix(strings.TrimPrefix(file, dir+"/"), ".tmpl")

		data, err := templates.ReadFile(file)
		if err != nil {
			return fmt.Errorf("reading scaffold file %s: %w", relative, err)
		}

		if bytes.Contains(data, []byte("{{.")) {
			tmpl, err := template.New(relative).Parse(string(data))
			if err != nil {
				return fmt.Errorf("parsing scaffold file %s: %w", relative, err)
			}

			var rendered bytes.Buffer

			err = tmpl.Execute(&rendered, values)
			if err != nil {
				return fmt.Errorf("rendering scaffold file %s: %w", relative, err)
			}

			data = rendered.Bytes()
		}

		switch relative {
		case "world.wit":
			scaffold.files["wit/app.wit"] = data
		case "module.lua":
			scaffold.module = data
		default:
			scaffold.files[relative] = data
		}

		return nil
	})
	if err != nil {
		return scaffoldFiles{}, fmt.Errorf("loading %s template %q: %w", language, name, err)
	}

	scaffold.files[".gitignore"] = []byte(scaffoldIgnore(language))
	if language == driver.Python {
		scaffold.files["requirements.lock"] =
			[]byte("# Pin each Python dependency to an exact version and hash.\n")
	}

	return scaffold, nil
}

func pascalWorld(world string) string {
	var result strings.Builder

	for word := range strings.SplitSeq(world, "-") {
		result.WriteString(strings.ToUpper(word[:1]))
		result.WriteString(word[1:])
	}

	return result.String()
}

func scaffoldIgnore(language string) string {
	const working = "/.tt-wasm/\n/dist/\n"

	switch language {
	case driver.Python:
		return working + "/__pycache__/\n"
	case driver.CSharp:
		return working + "/bin/\n/obj/\n"
	case driver.JavaScript:
		return working + "/node_modules/\n"
	case driver.Rust:
		return working + "/target/\n"
	default:
		return working
	}
}

func writeScaffold(root *os.Root, dir string, scaffold scaffoldFiles) error {
	for relative, data := range scaffold.files {
		filename := filepath.Join(dir, filepath.FromSlash(relative))

		err := root.MkdirAll(filepath.Dir(filename), scaffoldDirMode)
		if err == nil {
			err = writeNewFile(root, filename, data)
		}

		if err != nil {
			return fmt.Errorf("writing %s: %w", filename, err)
		}
	}

	return nil
}

func writeNewFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, scaffoldFileMode)
	if err != nil {
		return fmt.Errorf("creating scaffold file: %w", err)
	}

	_, err = file.Write(data)

	return errors.Join(err, file.Close())
}

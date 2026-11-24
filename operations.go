package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/mandesero/tt-wasm/internal/component"
	"github.com/mandesero/tt-wasm/internal/wasm"
	"github.com/tarantool/tt/sdk"
)

const (
	opFetch = "fetch"
	opBind  = "bind"
	opBuild = "build"
)

type operationCmd struct {
	services sdk.Services
	name     string
	runner   string
	check    bool
	rebuild  bool
	jobs     int
}

func newOperationCmd(services sdk.Services, name, description string) *cobra.Command {
	operation := &operationCmd{
		services: services, name: name, runner: "", check: false, rebuild: false, jobs: 0,
	}

	use := name + " [NAME]"
	if name == opBuild {
		use = name + " NAME"
	}

	cmd := &cobra.Command{
		Use:   use + " [-- TOOL_ARG...]",
		Short: description,
		Args:  operation.validateArgs,
		RunE:  operation.run,
	}

	flags := cmd.Flags()
	flags.StringVar(&operation.runner, "runner", "",
		"override the runner for this invocation: local or docker")

	switch name {
	case opBind:
		flags.BoolVar(&operation.check, "check", false, "check that bindings are up to date")
	case opBuild:
		flags.BoolVar(&operation.rebuild, "rebuild", false, "start with an empty working directory")
		sdk.AddJobsFlag(cmd, &operation.jobs)
	}

	return cmd
}

func (c *operationCmd) validateArgs(cmd *cobra.Command, args []string) error {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		if c.name == opBuild {
			return cobra.ExactArgs(1)(cmd, args)
		}

		return cobra.MaximumNArgs(1)(cmd, args)
	}

	if dash != 1 {
		return sdk.Errorf(sdk.ExitFailure, "a single component name is required before --")
	}

	if c.check && len(args) > dash {
		return sdk.Errorf(sdk.ExitFailure, "--check cannot be combined with tool arguments")
	}

	return nil
}

func (c *operationCmd) run(cmd *cobra.Command, args []string) error {
	dash := cmd.ArgsLenAtDash()

	var toolArgs []string

	if dash >= 0 {
		args, toolArgs = args[:dash], args[dash:]
	}

	switch c.runner {
	case "", "local", "docker":
	default:
		return sdk.Errorf(sdk.ExitFailure, "unknown runner %q: use local or docker", c.runner)
	}

	help := slices.Equal(toolArgs, []string{"--help"})
	project := c.services.Project()

	ctx, release, err := project.Lock(cmd.Context())
	if err != nil {
		return err
	}
	defer release()

	if c.runner != "" {
		ctx = component.WithRunner(ctx, c.runner)
	}

	man, err := projectManifest(project)
	if err != nil {
		return err
	}

	names, err := componentNames(man, nameArg(args))
	if err != nil {
		return err
	}

	if c.name == opBuild && !help {
		_, err := project.Build(ctx, sdk.BuildOptions{
			Product: "", Components: names, Locked: true, Rebuild: c.rebuild,
			Registries: nil, ShowOutput: true, Args: toolArgs, Jobs: c.jobs,
		})

		return err
	}

	for _, name := range names {
		err = c.runComponent(ctx, project, name, toolArgs, help)
		if err != nil {
			return fmt.Errorf("component %s: %w", name, err)
		}
	}

	return nil
}

func (c *operationCmd) runComponent(ctx context.Context, project sdk.Project, name string,
	args []string, help bool,
) error {
	facts, configured, err := project.BuildContext(ctx, sdk.BuildContextOptions{
		Product: "", Component: name, Locked: true, Args: args, Jobs: c.jobs,
	})
	if err != nil {
		return err
	}

	wasmBuild, ok := configured.(*component.Build)
	if !ok {
		return sdk.Errorf(sdk.ExitFailure, "component has no WebAssembly backend")
	}

	if c.check {
		return wasmBuild.CheckBindings(ctx, facts)
	}

	streams := c.services.Streams().IO()

	return wasmBuild.Operate(ctx, facts, c.name, args, help, streams.Out, streams.Err)
}

func newCleanCmd(services sdk.Services) *cobra.Command {
	return &cobra.Command{
		Use:   "clean [NAME]",
		Short: "Remove generated files of WebAssembly components",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := services.Project()

			ctx, release, err := project.Lock(cmd.Context())
			if err != nil {
				return err
			}
			defer release()

			man, err := projectManifest(project)
			if err != nil {
				return err
			}

			names, err := componentNames(man, nameArg(args))
			if err != nil || len(names) == 0 {
				return err
			}

			_, err = project.Clean(ctx, sdk.CleanOptions{Components: names, DryRun: false})

			return err
		},
	}
}

func newStatusCmd(services sdk.Services) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the runtime dependency, locked profiles and generated files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return inspect(cmd.Context(), services, "", false, false)
		},
	}
}

func newDoctorCmd(services sdk.Services) *cobra.Command {
	var (
		name   string
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check WebAssembly project and toolchain readiness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return inspect(cmd.Context(), services, name, true, asJSON)
		},
	}

	cmd.Flags().StringVar(&name, "component", "", "check one WebAssembly component")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write the report in JSON")

	return cmd
}

func inspect(
	ctx context.Context, services sdk.Services, name string, checkTools, asJSON bool,
) error {
	project := services.Project()

	man, err := projectManifest(project)
	if err != nil {
		return err
	}

	names, err := componentNames(man, name)
	if err != nil {
		return err
	}

	statuses := make([]component.Status, 0, len(names))
	for _, name := range names {
		status, err := component.Inspect(ctx, project, name, checkTools)
		if err != nil {
			return err
		}

		statuses = append(statuses, status)
	}

	dependency, declared := man.Dependencies[wasm.RockName]
	out := services.Streams().IO().Out

	if asJSON {
		report := struct {
			Runtime    string             `json:"runtime"`
			Components []component.Status `json:"components"`
		}{Runtime: dependency.Version, Components: statuses}

		err = json.NewEncoder(out).Encode(report)
		if err != nil {
			return fmt.Errorf("writing the report: %w", err)
		}
	} else {
		printStatuses(out, dependency, declared, statuses)
	}

	if !declared {
		return sdk.Errorf(sdk.ExitFailure,
			"the package declares no wasm runtime; run tt wasm prepare")
	}

	for _, status := range statuses {
		if status.Problem != "" {
			return sdk.Errorf(sdk.ExitFailure, "%s: %s", status.Name, status.Problem)
		}
	}

	if !asJSON {
		_, _ = fmt.Fprintln(out, "ready")
	}

	return nil
}

func printStatuses(out io.Writer, dependency sdk.ManifestDependency, declared bool,
	statuses []component.Status,
) {
	if declared {
		registry := dependency.Registry
		if registry == "" {
			registry = "configured rock servers"
		}

		_, _ = fmt.Fprintf(out, "wasm:       %s from %s\n", dependency.Version, registry)
	}

	for _, status := range statuses {
		_, _ = fmt.Fprintf(out, "component:  %s (%s, %s, %s)\n", status.Name,
			status.Language, status.Profile, status.Runner)
		_, _ = fmt.Fprintf(out, "  dependencies fetched: %t\n", status.Fetched)
		_, _ = fmt.Fprintf(out, "  bindings current:     %t\n", status.Bindings)

		if status.Problem != "" {
			_, _ = fmt.Fprintf(out, "  %s\n", status.Problem)
		}
	}
}

func projectManifest(project sdk.Project) (sdk.Manifest, error) {
	dir, err := project.Root()
	if err != nil {
		return sdk.Manifest{}, err
	}

	return readManifest(project, dir)
}

func nameArg(args []string) string {
	if len(args) == 0 {
		return ""
	}

	return args[0]
}

// componentNames selects only the components that use this module's backend.
func componentNames(man sdk.Manifest, name string) ([]string, error) {
	if name != "" {
		declared, ok := man.Components[name]
		if !ok || declared.Build == nil || declared.Build.Backend != component.Name {
			return nil, sdk.Errorf(sdk.ExitFailure, "%q is not a WebAssembly component", name)
		}

		return []string{name}, nil
	}

	var names []string

	for name, declared := range man.Components {
		if declared.Build != nil && declared.Build.Backend == component.Name {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names, nil
}

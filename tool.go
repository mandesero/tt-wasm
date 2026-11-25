package wasm

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mandesero/tt-wasm/internal/component"
	"github.com/tarantool/tt/sdk"
	"github.com/tarantool/tt/sdk/build"
)

func newToolCmd(services sdk.Services) *cobra.Command {
	var runner string

	cmd := &cobra.Command{
		Use:   "tool NAME -- COMMAND [ARG...]",
		Short: "Run a command in the component's Docker toolchain",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 1 || len(args) < 2 || args[0] == "" || args[1] == "" {
				return sdk.Errorf(sdk.ExitFailure, "a component name and a command after -- are required")
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			switch runner {
			case "":
			case "docker":
				ctx = component.WithRunner(ctx, runner)
			default:
				return sdk.Errorf(sdk.ExitFailure, "tool requires the Docker runner")
			}

			streams := services.Streams().IO()
			facts, wasmBuild, err := prepareTool(ctx, services.Project(), args[0], streams.Err)
			if err != nil {
				return err
			}

			// Docker combines the output streams when it allocates a terminal.
			tty := toolTerminal(streams.In) && toolTerminal(streams.Out) && toolTerminal(streams.Err)

			return wasmBuild.RunTool(ctx, facts, args[1:], streams.In, streams.Out, streams.Err, tty)
		},
	}

	cmd.Flags().StringVar(&runner, "runner", "", "override the configured runner with Docker")

	return cmd
}

// prepareTool holds the project lock while bootstrapping the tool environment.
// The command itself may be an interactive shell and runs after release.
func prepareTool(ctx context.Context, project sdk.Project, name string, progress io.Writer,
) (*build.Context, *component.Build, error) {
	locked, release, err := project.Lock(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer release()

	facts, wasmBuild, err := wasmBuildContext(locked, project, sdk.BuildContextOptions{
		Component: name, Locked: true,
	})
	if err != nil {
		return nil, nil, err
	}

	return facts, wasmBuild, wasmBuild.PrepareTool(locked, facts, progress, progress)
}

func toolTerminal(stream any) bool {
	file, ok := stream.(*os.File)

	return ok && term.IsTerminal(int(file.Fd()))
}

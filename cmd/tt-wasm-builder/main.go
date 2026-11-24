// Command tt-wasm-builder runs one toolchain operation in a container.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/mandesero/tt-wasm/internal/component/driver"
)

var (
	errUsage   = errors.New("usage: tt-wasm-builder [check LANGUAGE]")
	errRequest = errors.New("expected a single Wasm build request on stdin")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)

	stop()

	if err == nil {
		return
	}

	fmt.Fprintln(os.Stderr, err)

	exit, ok := errors.AsType[*exec.ExitError](err)
	if ok && exit.ExitCode() > 0 {
		os.Exit(exit.ExitCode())
	}

	os.Exit(1)
}

// run handles one JSON request, or checks the image's tools for a language.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 2 && args[0] == "check" {
		return driver.Check(ctx, args[1])
	}

	if len(args) != 0 {
		return errUsage
	}

	var request driver.Request

	decoder := json.NewDecoder(stdin)
	decoder.DisallowUnknownFields()

	err := decoder.Decode(&request)
	if err != nil {
		return fmt.Errorf("%w: %w", errRequest, err)
	}

	var extra json.RawMessage

	err = decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		return errRequest
	}

	return driver.Run(ctx, request, stdout, stderr)
}

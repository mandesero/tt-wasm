// Package runner runs a WebAssembly language driver on the host or in Docker.
package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mandesero/tt-wasm/internal/component/driver"
)

const (
	// Local runs the tools the user installed on the host.
	Local = "local"
	// Docker runs the driver's tools in a prepared container image.
	Docker = "docker"
	// DefaultPlatform is the Docker platform for the first toolchain images.
	DefaultPlatform = "linux/amd64"

	containerComponent = "/component"
	containerWork      = "/work"
	containerCache     = "/cache"
	digestPrefix       = "sha256:"
	dockerWaitDelay    = 5 * time.Second
)

var (
	errRunnerMode    = errors.New("runner must be local or docker")
	errImageRequired = errors.New("no Docker toolchain image selected")
	errImageID       = errors.New("no SHA-256 image ID reported by Docker")
	errMountPath     = errors.New("component, work and cache paths must be absolute")
)

// Settings select the execution environment independently of the driver.
type Settings struct {
	Mode     string `json:"mode,omitempty"`     // Docker when empty.
	Image    string `json:"image,omitempty"`    // A release digest or an image built locally.
	Platform string `json:"platform,omitempty"` // DefaultPlatform when empty.
	Context  string `json:"context,omitempty"`  // Docker's current context when empty.
}

// Prepare checks the local tools or makes the Docker image available and checks
// its tools. Only a release pinned by digest can be pulled. A local development
// tag or ID must already exist.
func Prepare(
	ctx context.Context, settings Settings, language string, stdout, stderr io.Writer,
) error {
	err := settings.validate()
	if err != nil {
		return err
	}

	if settings.Mode == Local {
		return driver.Check(ctx, language)
	}

	_, err = ImageID(ctx, settings)
	if err != nil {
		if !strings.Contains(settings.Image, "@"+digestPrefix) {
			return err
		}

		args := []string{"pull", "--platform", settings.EffectivePlatform(), "--", settings.Image}
		pull := settings.command(ctx, args...)

		err = runDocker(pull, stdout, stderr, "pulling "+settings.Image)
		if err != nil {
			return err
		}
	}

	return settings.check(ctx, language, stdout, stderr)
}

// ImageID resolves a locally available image to the content ID reported by
// Docker. A development tag can be pinned before later offline operations.
func ImageID(ctx context.Context, settings Settings) (string, error) {
	if settings.Image == "" {
		return "", errImageRequired
	}

	cmd := settings.command(ctx, "image", "inspect", "--format", "{{.Id}}", "--", settings.Image)

	output, err := cmd.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("inspecting Docker image %s: %w: %s",
				settings.Image, err, bytes.TrimSpace(exit.Stderr))
		}

		return "", fmt.Errorf("inspecting Docker image %s: %w", settings.Image, err)
	}

	imageID := strings.TrimSpace(string(output))
	digest, found := strings.CutPrefix(imageID, digestPrefix)

	_, decodeErr := hex.DecodeString(digest)
	if !found || len(digest) != 2*sha256.Size || decodeErr != nil ||
		digest != strings.ToLower(digest) {
		return "", fmt.Errorf("%w for %s: %q", errImageID, settings.Image, imageID)
	}

	return imageID, nil
}

// Check reports whether the selected toolchain is ready without pulling an
// image or installing tools.
func Check(ctx context.Context, settings Settings, language string) error {
	err := settings.validate()
	if err != nil {
		return err
	}

	if settings.Mode == Local {
		return driver.Check(ctx, language)
	}

	var stderr bytes.Buffer

	err = settings.check(ctx, language, io.Discard, &stderr)
	if err != nil && stderr.Len() > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}

	return err
}

// Run executes one operation. Docker can write its work directory and may
// write the dependency cache on fetch. The source tree is always read-only.
func Run(
	ctx context.Context, settings Settings, request driver.Request, stdout, stderr io.Writer,
) error {
	err := settings.validate()
	if err != nil {
		return err
	}

	if settings.Mode == Local {
		if !request.Help {
			err = driver.Check(ctx, request.Language)
			if err != nil {
				return err
			}
		}

		return driver.Run(ctx, request, stdout, stderr)
	}

	fetch := request.Operation == driver.Fetch && !request.Help
	args := append(settings.runArgs(!fetch), "--interactive")

	if !request.Help {
		if !filepath.IsAbs(request.Dir) || !filepath.IsAbs(request.Work) ||
			!filepath.IsAbs(request.Cache) {
			return errMountPath
		}

		args = append(args,
			"--mount", bindMount(request.Dir, containerComponent, true),
			"--mount", bindMount(request.Work, containerWork, false),
			"--mount", bindMount(request.Cache, containerCache, !fetch),
		)
	}

	request.Dir, request.Work, request.Cache = containerComponent, containerWork, containerCache

	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encoding the Wasm build request: %w", err)
	}

	cmd := settings.command(ctx, append(args, "--", settings.Image)...)

	cmd.Stdin = bytes.NewReader(body)

	return runDocker(cmd, stdout, stderr, "running "+request.Operation+" in "+settings.Image)
}

// validate checks the mode and whether Docker has an image to run.
func (settings Settings) validate() error {
	switch settings.Mode {
	case Local:
		return nil
	case "", Docker:
		if settings.Image == "" {
			return errImageRequired
		}

		return nil
	default:
		return fmt.Errorf("%w: %q", errRunnerMode, settings.Mode)
	}
}

// check runs the builder's toolchain check with neither mounts nor networking.
func (settings Settings) check(
	ctx context.Context, language string, stdout, stderr io.Writer,
) error {
	args := append(settings.runArgs(true), "--", settings.Image, "check", language)
	cmd := settings.command(ctx, args...)

	return runDocker(cmd, stdout, stderr, "checking "+language+" tools in "+settings.Image)
}

// EffectivePlatform returns the host platform for local tools or the Docker image platform.
func (settings Settings) EffectivePlatform() string {
	if settings.Mode == Local {
		return runtime.GOOS + "/" + runtime.GOARCH
	}

	if settings.Platform == "" {
		return DefaultPlatform
	}

	return settings.Platform
}

// runArgs names an already prepared image's execution options. UID and GID
// keep generated files writable by the user on a Linux host.
func (settings Settings) runArgs(offline bool) []string {
	args := []string{"run", "--rm", "--pull=never", "--platform", settings.EffectivePlatform()}
	if uid := os.Getuid(); uid >= 0 {
		args = append(args, "--user", fmt.Sprintf("%d:%d", uid, os.Getgid()))
	}

	args = append(args, "--env", "HOME=/tmp")
	if offline {
		args = append(args, "--network", "none")
	}

	return args
}

// command runs Docker with the selected context. On cancellation, the Docker
// client forwards SIGTERM to the builder before the client has to be killed.
func (settings Settings) command(ctx context.Context, args ...string) *exec.Cmd {
	if settings.Context != "" {
		args = append([]string{"--context", settings.Context}, args...)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)

	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = dockerWaitDelay

	return cmd
}

// bindMount encodes one Docker --mount value. Docker reads CSV fields, so the
// host path may contain commas and quotes without becoming another option.
func bindMount(source, target string, readOnly bool) string {
	fields := []string{"type=bind", "source=" + source, "target=" + target}
	if readOnly {
		fields = append(fields, "readonly")
	}

	var mount strings.Builder

	writer := csv.NewWriter(&mount)

	_ = writer.Write(fields)
	writer.Flush()

	return strings.TrimSuffix(mount.String(), "\n")
}

// runDocker streams a Docker operation's output and reports the failed action.
func runDocker(cmd *exec.Cmd, stdout, stderr io.Writer, action string) error {
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}

	return nil
}

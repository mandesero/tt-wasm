//go:build !unix

package driver

import "os/exec"

func isolateTool(_ *exec.Cmd) {}

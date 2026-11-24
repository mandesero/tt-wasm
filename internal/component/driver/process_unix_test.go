//go:build unix

package driver

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCancellationStopsToolDescendants(t *testing.T) {
	t.Parallel()

	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	result := make(chan error, 1)
	var output bytes.Buffer
	run := executor{stdout: &output, stderr: &output}

	go func() {
		result <- run.command(ctx, "sh", "-c", `sleep 15 & : > "$1"; wait`, "tool", ready)
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(ready)

		return err == nil
	}, 3*time.Second, 10*time.Millisecond)
	cancel()

	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("a child tool kept the compiler's pipes open after cancellation")
	}
}

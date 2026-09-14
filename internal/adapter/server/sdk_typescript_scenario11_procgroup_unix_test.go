//go:build unix

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSDKScenario11CommandTimeoutKillsDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	output, err := runSDKScenario11Command(
		t,
		1*time.Second,
		t.TempDir(),
		"/bin/sh",
		[]string{"SCENARIO11_DESCENDANT_MARKER=" + marker},
		"-c",
		`(printf 'descendant-ready\n'; sleep 2; printf survived > "$SCENARIO11_DESCENDANT_MARKER") & wait`,
	)
	if !strings.Contains(output, "descendant-ready\n") {
		t.Fatalf("descendant did not report readiness; output=%q, err=%v", output, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("command error = %v, want context deadline exceeded; output=%q", err, output)
	}

	// A descendant orphaned by killing only the shell would create this marker.
	// Waiting past its delay proves cleanup without process-state checks that can
	// misclassify an unreaped zombie as live.
	time.Sleep(2300 * time.Millisecond)
	if contents, statErr := os.ReadFile(marker); statErr == nil {
		t.Fatalf("descendant survived command timeout and wrote %q", contents)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("read descendant marker: %v", statErr)
	}
}

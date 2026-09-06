//go:build unix

package osfs_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestADR_0281_ManagedCommandWithoutTimeoutHasNoAbsoluteDeadline pins that a
// managed lease remains live under the caller context until the command ends.
func TestADR_0281_ManagedCommandWithoutTimeoutHasNoAbsoluteDeadline(t *testing.T) {
	runner := managedJobStreamer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunStreamingWithTemporaryScope(ctx, "sleep 31", tool.TemporaryScopeManaged, newLeasePathWriter())
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("managed command received an unintended default deadline: %v", err)
	case <-time.After(30*time.Second + 500*time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("managed command completed instead of observing cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("managed command did not terminate after cancellation")
	}
}

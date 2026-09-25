package ui

import (
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestServerProviderRecovery_Scenario5_TUINoTerminalAutoRetryAndScheduleNoRearm(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	usersBefore := userBlockCount(m.conv)

	updated, cmd := m.Update(failedStepRetryableResult())
	m = updated.(Model)
	runBatchLeaves(cmd)
	if got := retryFrameCount(conv.send.frames()); got != 0 {
		t.Fatalf("terminal recovery started %d automatic retries, want none", got)
	}
	if got := userBlockCount(m.conv); got != usersBefore {
		t.Fatalf("terminal recovery duplicated user prompt: %d -> %d", usersBefore, got)
	}

	m.prompt.Rewrite("/retry")
	updated, cmd, handled := m.dispatchBareBuiltin(m.prompt.Value())
	if !handled {
		t.Fatal("/retry was not handled")
	}
	m = updated.(Model)
	runBatchLeaves(cmd)
	if got := retryFrameCount(conv.send.frames()); got != 1 {
		t.Fatalf("manual /retry frames = %d, want 1", got)
	}
	if m.prompt.Value() != "" || userBlockCount(m.conv) != usersBefore {
		t.Fatal("manual /retry changed prompt history")
	}

	// An active recovery advisory is not terminal and must not arm another run.
	m = applyAll(m, client.ModelRetryMsg{})
	if got := retryFrameCount(conv.send.frames()); got != 1 {
		t.Fatalf("active recovery rearmed run: retry frames = %d", got)
	}
}

func TestServerProviderRecovery_Scenario6_NoDetachedOrRestartContinuation(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	updated, cmd := m.Update(failedStepRetryableResult())
	m = updated.(Model)
	runBatchLeaves(cmd)
	if m.phase != phaseIdle {
		t.Fatalf("terminal recovery phase = %v, want idle", m.phase)
	}
	if got := retryFrameCount(conv.send.frames()); got != 0 {
		t.Fatalf("terminal recovery detached %d continuation runs", got)
	}
	fresh, freshConv := newQueueModel(t)
	if fresh.phase != phaseIdle || retryFrameCount(freshConv.send.frames()) != 0 {
		t.Fatal("fresh process model claimed a recovery continuation")
	}
}

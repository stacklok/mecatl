package ui

import "testing"

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
}

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

type deadlineExecutionHandle struct {
	deadline time.Time
}

func (*deadlineExecutionHandle) Environment() tool.Environment { return tool.Environment{} }
func (*deadlineExecutionHandle) Renew(context.Context) error   { return nil }
func (*deadlineExecutionHandle) Release(context.Context) error { return nil }
func (h *deadlineExecutionHandle) RenewalDeadline() time.Time  { return h.deadline }

var _ ExecutionRunHandle = (*deadlineExecutionHandle)(nil)

func TestExecutionRenewDelayTracksShortGrantExpiry(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	handle := &deadlineExecutionHandle{deadline: now.Add(5 * time.Second)}
	delay, usable := executionRenewDelay(handle, now)
	if !usable || delay <= 0 || delay >= 5*time.Second || delay < 100*time.Millisecond {
		t.Fatalf("five-second grant schedule delay=%v usable=%t", delay, usable)
	}
	handle.deadline = now.Add(250 * time.Millisecond)
	if _, usable := executionRenewDelay(handle, now); usable {
		t.Fatal("unusable grant remainder would start execution renewal")
	}
}

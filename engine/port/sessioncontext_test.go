package port_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionIDContext(t *testing.T) {
	if id, ok := port.SessionIDFromContext(context.Background()); ok || id != "" {
		t.Fatalf("absent SessionIDFromContext = (%q, %v), want (empty, false)", id, ok)
	}

	const parent session.SessionID = "session-exact-α"
	ctx := port.WithSessionID(context.Background(), parent)
	if id, ok := port.SessionIDFromContext(ctx); !ok || id != parent {
		t.Fatalf("SessionIDFromContext = (%q, %v), want (%q, true)", id, ok, parent)
	}

	const child session.SessionID = "subagent-child-42"
	nested := port.WithSessionID(ctx, child)
	if id, ok := port.SessionIDFromContext(nested); !ok || id != child {
		t.Fatalf("nested SessionIDFromContext = (%q, %v), want (%q, true)", id, ok, child)
	}
	if id, _ := port.SessionIDFromContext(ctx); id != parent {
		t.Fatalf("parent context changed to %q, want %q", id, parent)
	}
}

func TestAttemptCorrelationContext(t *testing.T) {
	ctx := port.WithTurnIndex(port.WithRunSerial(context.Background(), 42), 3)
	if serial, ok := port.RunSerialFromContext(ctx); !ok || serial != 42 {
		t.Fatalf("RunSerialFromContext = (%d, %v), want (42, true)", serial, ok)
	}
	if turn, ok := port.TurnIndexFromContext(ctx); !ok || turn != 3 {
		t.Fatalf("TurnIndexFromContext = (%d, %v), want (3, true)", turn, ok)
	}
	if _, ok := port.RunSerialFromContext(context.Background()); ok {
		t.Fatal("empty context unexpectedly carried a run serial")
	}
	if _, ok := port.TurnIndexFromContext(context.Background()); ok {
		t.Fatal("empty context unexpectedly carried a turn index")
	}
}

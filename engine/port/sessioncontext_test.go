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

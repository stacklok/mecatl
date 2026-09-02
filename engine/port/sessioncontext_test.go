package port_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0290_SessionHeaderLegalValue(t *testing.T) {
	if port.SessionIDHeaderName != "X-Mecatl-Session-ID" {
		t.Fatalf("SessionIDHeaderName = %q, want X-Mecatl-Session-ID", port.SessionIDHeaderName)
	}

	data, err := os.ReadFile("../../testdata/session_header_values.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Legal bool   `json:"legal"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			if got := port.ValidSessionIDHeaderValue(vector.Value); got != vector.Legal {
				t.Errorf("ValidSessionIDHeaderValue(%q) = %v, want %v", vector.Value, got, vector.Legal)
			}
			if vector.Legal {
				ctx := port.WithSessionID(context.Background(), session.SessionID(vector.Value))
				got, ok := port.SessionIDFromContext(ctx)
				if !ok || string(got) != vector.Value {
					t.Errorf("legal value changed through context: (%q, %v), want (%q, true)", got, ok, vector.Value)
				}
			}
		})
	}
}

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
	base := port.WithSessionID(context.Background(), "session")
	run := port.WithRunSerial(base, 42)
	turnCtx := port.WithTurnIndex(run, 3)
	if serial, ok := port.RunSerialFromContext(run); !ok || serial != 42 {
		t.Fatalf("RunSerialFromContext = (%d, %v), want (42, true)", serial, ok)
	}
	if _, ok := port.TurnIndexFromContext(run); ok {
		t.Fatal("immutable WithTurnIndex changed its parent")
	}
	if turn, ok := port.TurnIndexFromContext(turnCtx); !ok || turn != 3 {
		t.Fatalf("TurnIndexFromContext = (%d, %v), want (3, true)", turn, ok)
	}
	if port.SetAttemptTurnIndex(run, 7) {
		t.Fatal("ordinary WithRunSerial context accepted a mutable turn")
	}

	attempt := port.WithRunAttemptContext(context.Background(), "attempt-session", 43)
	if !port.SetAttemptTurnIndex(attempt, 7) {
		t.Fatal("SetAttemptTurnIndex did not find the run-owned carrier")
	}
	if id, ok := port.SessionIDFromContext(attempt); !ok || id != "attempt-session" {
		t.Fatalf("attempt session = (%q, %v), want (attempt-session, true)", id, ok)
	}
	if serial, ok := port.RunSerialFromContext(attempt); !ok || serial != 43 {
		t.Fatalf("attempt serial = (%d, %v), want (43, true)", serial, ok)
	}
	if turn, ok := port.TurnIndexFromContext(attempt); !ok || turn != 7 {
		t.Fatalf("attempt turn = (%d, %v), want (7, true)", turn, ok)
	}
	if port.SetAttemptTurnIndex(context.Background(), 1) {
		t.Fatal("context without a run carrier accepted a mutable turn")
	}
	if _, ok := port.RunSerialFromContext(context.Background()); ok {
		t.Fatal("empty context unexpectedly carried a run serial")
	}
	if _, ok := port.TurnIndexFromContext(context.Background()); ok {
		t.Fatal("empty context unexpectedly carried a turn index")
	}
}

func TestAttemptCorrelationImmutableDerivationAndRunIsolation(t *testing.T) {
	base := port.WithRunSerial(context.Background(), 10)
	run1 := port.WithRunSerial(base, 11)
	run2 := port.WithRunSerial(base, 12)
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		serial int64
	}{
		{"base", base, 10},
		{"run1", run1, 11},
		{"run2", run2, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if serial, ok := port.RunSerialFromContext(tc.ctx); !ok || serial != tc.serial {
				t.Fatalf("serial = (%d, %v), want (%d, true)", serial, ok, tc.serial)
			}
		})
	}
	turn1 := port.WithTurnIndex(context.Background(), 1)
	turn2 := port.WithTurnIndex(context.Background(), 2)
	if turn, _ := port.TurnIndexFromContext(turn1); turn != 1 {
		t.Fatalf("turn1 changed to %d, want 1", turn)
	}
	if turn, _ := port.TurnIndexFromContext(turn2); turn != 2 {
		t.Fatalf("turn2 changed to %d, want 2", turn)
	}

	attempt1 := port.WithRunAttemptContext(context.Background(), "one", 21)
	attempt2 := port.WithRunAttemptContext(context.Background(), "two", 22)
	port.SetAttemptTurnIndex(attempt1, 1)
	port.SetAttemptTurnIndex(attempt2, 2)
	if turn, _ := port.TurnIndexFromContext(attempt1); turn != 1 {
		t.Fatalf("attempt1 turn changed to %d, want 1", turn)
	}
	if turn, _ := port.TurnIndexFromContext(attempt2); turn != 2 {
		t.Fatalf("attempt2 turn changed to %d, want 2", turn)
	}
}

func TestAttemptCorrelationConcurrentDiagnosticReads(t *testing.T) {
	ctx := port.WithRunAttemptContext(context.Background(), "session", 42)
	const updates = 1000
	var readers sync.WaitGroup
	readers.Add(2)
	for range 2 {
		go func() {
			defer readers.Done()
			for range updates {
				if serial, ok := port.RunSerialFromContext(ctx); !ok || serial != 42 {
					t.Errorf("RunSerialFromContext = (%d, %v), want (42, true)", serial, ok)
					return
				}
				if turn, ok := port.TurnIndexFromContext(ctx); ok && (turn < 0 || turn >= updates) {
					t.Errorf("TurnIndexFromContext = (%d, true), want an in-range turn", turn)
					return
				}
			}
		}()
	}
	for turn := range updates {
		if !port.SetAttemptTurnIndex(ctx, turn) {
			t.Fatal("turn update did not find run carrier")
		}
	}
	readers.Wait()
	if turn, ok := port.TurnIndexFromContext(ctx); !ok || turn != updates-1 {
		t.Fatalf("final TurnIndexFromContext = (%d, %v), want (%d, true)", turn, ok, updates-1)
	}
}

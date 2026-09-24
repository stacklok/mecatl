package hookexec_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// Compile-time assertion that Runner satisfies the frozen port interface.
var _ port.HookRunner = (*hookexec.Runner)(nil)

func event() governance.HookEvent {
	return governance.HookEvent{
		Phase:     governance.PhasePreToolUse,
		Tool:      "Shell",
		Input:     json.RawMessage(`{"command":"rm -rf /"}`),
		SessionID: "s1",
	}
}

func TestExitZeroAllows(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "exit 0",
	})
	out, err := r.Run(context.Background(), event())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Outcome.Block {
		t.Fatalf("exit 0 should not block")
	}
}

// gauntlet #5: exit code 2 → Block with the message from stdout.
func TestExitTwoBlocks(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "echo 'nope: dangerous'; exit 2",
	})
	out, err := r.Run(context.Background(), event())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Outcome.Block {
		t.Fatalf("exit 2 should block")
	}
	if out.Outcome.Message != "nope: dangerous" {
		t.Fatalf("expected block message from stdout, got %q", out.Outcome.Message)
	}
}

// Block message falls back to stderr when stdout is empty.
func TestExitTwoBlockMessageFromStderr(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "echo 'err reason' 1>&2; exit 2",
	})
	out, err := r.Run(context.Background(), event())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Outcome.Block || out.Outcome.Message != "err reason" {
		t.Fatalf("expected block with stderr message, got block=%v msg=%q", out.Outcome.Block, out.Outcome.Message)
	}
}

func TestOtherExitIsError(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "echo boom 1>&2; exit 1",
	})
	_, err := r.Run(context.Background(), event())
	if err == nil {
		t.Fatalf("expected error for non-0/2 exit code")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error to carry hook output, got %v", err)
	}
}

// stdin must carry the JSON-serialized event.
func TestStdinCarriesEventJSON(t *testing.T) {
	// The hook greps its own stdin for fields; if absent it exits 2 to signal
	// failure of the assertion via Block.
	script := `payload=$(cat); echo "$payload" | grep -q '"Tool":"Shell"' && echo "$payload" | grep -q '"SessionID":"s1"' || { echo "missing fields: $payload" 1>&2; exit 2; }`
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: script,
	})
	out, err := r.Run(context.Background(), event())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Outcome.Block {
		t.Fatalf("hook reported missing stdin fields: %s", out.Outcome.Message)
	}
}

// A hook may emit a JSON control envelope on stdout (exit 0) carrying a mutation
// that the loop applies. The adapter must parse "mutated" into HookOutcome.Mutated.
func TestExitZeroJSONEnvelopeCarriesMutation(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: `echo '{"mutated":{"command":"ls"},"message":"rewrote"}'`,
	})
	out, err := r.Run(context.Background(), event())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Outcome.Block {
		t.Fatalf("exit 0 must not block")
	}
	if string(out.Outcome.Mutated) != `{"command":"ls"}` {
		t.Fatalf("Mutated = %q, want the rewritten args object", string(out.Outcome.Mutated))
	}
	if out.Outcome.Message != "rewrote" {
		t.Fatalf("Message = %q, want 'rewrote'", out.Outcome.Message)
	}
}

// Plain (non-JSON-object) stdout on exit 0 remains a message, never a mutation —
// the original contract is preserved.
func TestExitZeroPlainStdoutIsMessageNotMutation(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: `echo 'just a note'`,
	})
	out, err := r.Run(context.Background(), event())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Outcome.Mutated != nil {
		t.Fatalf("plain stdout must not produce a mutation; got %q", string(out.Outcome.Mutated))
	}
	if out.Outcome.Message != "just a note" {
		t.Fatalf("Message = %q, want 'just a note'", out.Outcome.Message)
	}
}

func TestNoHookForPhaseAllows(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePostToolUse: "exit 2", // different phase
	})
	out, err := r.Run(context.Background(), event())
	if err != nil || out.Outcome.Block {
		t.Fatalf("unconfigured phase should allow; got block=%v err=%v", out.Outcome.Block, err)
	}
}

func TestNilConfigAllows(t *testing.T) {
	r := hookexec.New(nil)
	out, err := r.Run(context.Background(), event())
	if err != nil || out.Outcome.Block {
		t.Fatalf("nil config should allow everything; got block=%v err=%v", out.Outcome.Block, err)
	}
}

func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "exit 0",
	})
	_, err := r.Run(ctx, event())
	if err == nil {
		t.Fatalf("expected error for cancelled context")
	}
}

func TestTimeout(t *testing.T) {
	r := hookexec.New(map[governance.HookPhase]string{
		governance.PhasePreToolUse: "sleep 5",
	}, hookexec.WithTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := r.Run(context.Background(), event())
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout did not fire promptly")
	}
}

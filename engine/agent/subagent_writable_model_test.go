package agent_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
)

// subagent_writable_model_test.go covers issue #285: a WRITABLE explorer (mode:"read-write"
// with no `agent`) honours a per-call `model` via WithWritableEngineFactory, and the unwired
// / miss paths surface LOUD model-addressable errors (never a silent inherit onto the
// default model).

// writableModelMarkerFactory returns a factory that mints a writable explorer engine whose
// single turn emits "WRITABLE-MODEL:<model>", so a test can tell which model the minted
// engine ran on. found controls the (engine, ok) return.
func writableModelMarkerFactory(t *testing.T, found bool) func(model string) (*agent.Engine, bool) {
	t.Helper()
	return func(model string) (*agent.Engine, bool) {
		if !found {
			return nil, false
		}
		return childEngineWith(mockllm.New(mockllm.TextTurn("WRITABLE-MODEL:"+model)), catalogWith(t)), true
	}
}

// TestSubagentWritableModelUsesFactory: read-write+model with a wired writable engine
// factory mints the child on the requested model (not the default writable explorer, not a
// read-only engine).
func TestSubagentWritableModelUsesFactory(t *testing.T) {
	var rr atomic.Pointer[string]
	task := newWritableSubagent(t, writableChildWriting(t, "DEFAULT WRITABLE (should NOT run)", &rr),
		agent.WithWritableEngineFactory(writableModelMarkerFactory(t, true)))

	res := runOneSubagent(t, task, "p1", `{"prompt":"implement on fast","mode":"read-write","model":"fast"}`)
	if res.IsError {
		t.Fatalf("read-write+model with a wired factory must succeed, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "WRITABLE-MODEL:fast") {
		t.Fatalf("read-write+model must run the factory-minted engine on the requested model, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "DEFAULT WRITABLE") || strings.Contains(res.Content, "READ-ONLY EXPLORER RAN") {
		t.Fatalf("read-write+model must NOT run the default writable explorer or the read-only engine, got:\n%s", res.Content)
	}
	// Direct-write parity note still holds for the minted engine.
	if !strings.Contains(res.Content, "had direct write access to your workspace") {
		t.Fatalf("read-write+model result must carry the direct-write note, got:\n%s", res.Content)
	}
}

// TestSubagentWritableModelUnwiredRejected (ADVERSARIAL): read-write+model with the
// writable engine factory UNWIRED (only the default writable explorer wired) is a LOUD
// validateMode error — never a silent inherit that would run the writable subagent on a
// model the caller did not ask for.
func TestSubagentWritableModelUnwiredRejected(t *testing.T) {
	var rr atomic.Pointer[string]
	// newWritableSubagent wires WithWritableChildEngine but NOT WithWritableEngineFactory.
	task := newWritableSubagent(t, writableChildWriting(t, "should NOT run", &rr))

	res := runOneSubagent(t, task, "p1", `{"prompt":"implement on fast","mode":"read-write","model":"fast"}`)
	if !res.IsError {
		t.Fatalf("read-write+model with no writable engine factory must be rejected, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "per-call `model`") || !strings.Contains(res.Content, "not supported in this deployment") {
		t.Fatalf("the error must name the per-call model as unsupported in this deployment, got: %q", res.Content)
	}
	if rr.Load() != nil {
		t.Fatal("the writable child must NOT have run (a rejected call never mints/runs an engine)")
	}
}

// TestSubagentWritableModelFactoryMiss: read-write+model where the factory returns
// (nil,false) for the requested model surfaces the model-addressable "unknown or unroutable
// model" error (never a silent fallback to the default writable model).
func TestSubagentWritableModelFactoryMiss(t *testing.T) {
	var rr atomic.Pointer[string]
	task := newWritableSubagent(t, writableChildWriting(t, "should NOT run", &rr),
		agent.WithWritableEngineFactory(writableModelMarkerFactory(t, false))) // always miss

	res := runOneSubagent(t, task, "p1", `{"prompt":"go","mode":"read-write","model":"nope-1.0"}`)
	if !res.IsError {
		t.Fatalf("read-write+model with a factory miss must be an error, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "unknown or unroutable model") || !strings.Contains(res.Content, "nope-1.0") {
		t.Fatalf("the error must name the unroutable model, got: %q", res.Content)
	}
}

// compile-time: the option constructor has the expected shape.
var _ agent.SubagentOption = agent.WithWritableEngineFactory(func(string) (*agent.Engine, bool) { return nil, false })

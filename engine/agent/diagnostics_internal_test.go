package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// internalCapturingDiag is a mutex-safe port.Diagnostics for the in-package tests
// (a sibling of the external-package capturingDiag), recording every line with its
// With-bound attributes so a test can assert level + key/values.
type internalCapturingDiag struct {
	mu      *sync.Mutex
	records *[]internalDiagRecord
	bound   map[string]any
}

type internalDiagRecord struct {
	level port.Level
	msg   string
	attrs map[string]any
}

func newInternalCapturingDiag() *internalCapturingDiag {
	return &internalCapturingDiag{mu: &sync.Mutex{}, records: &[]internalDiagRecord{}, bound: map[string]any{}}
}

func (c *internalCapturingDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	attrs := make(map[string]any, len(c.bound)+len(args)/2)
	for k, v := range c.bound {
		attrs[k] = v
	}
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			attrs[k] = args[i+1]
		}
	}
	c.mu.Lock()
	*c.records = append(*c.records, internalDiagRecord{level: level, msg: msg, attrs: attrs})
	c.mu.Unlock()
}

func (c *internalCapturingDiag) With(args ...any) port.Diagnostics {
	bound := make(map[string]any, len(c.bound)+len(args)/2)
	for k, v := range c.bound {
		bound[k] = v
	}
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			bound[k] = args[i+1]
		}
	}
	return &internalCapturingDiag{mu: c.mu, records: c.records, bound: bound}
}

func (c *internalCapturingDiag) snapshot() []internalDiagRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]internalDiagRecord, len(*c.records))
	copy(out, *c.records)
	return out
}

// succeedingCompactor returns a valid one-message history with no error, so the
// loop reaches ReplaceHistory (whose state guard is the branch under test).
type succeedingCompactor struct{}

func (succeedingCompactor) Compact(context.Context, *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	return []session.Message{session.NewUserMessage("compacted goal")}, "compacted summary", session.AuxiliaryUsage{}, nil
}

// hugeTokenCounter reports an over-threshold count so maybeCompact always trips.
type hugeTokenCounter struct{}

func (hugeTokenCounter) Count(string) int { return 1 << 20 }
func (hugeTokenCounter) CountMessages(messages []session.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Text) + 1
	}
	return total
}

// TestCompactionReplaceRejectedEmitsWarn covers the SECOND compaction-failure WARN
// branch (loop.go maybeCompact): the compactor SUCCEEDS but the session rejects the
// replacement (ReplaceHistory is legal only while StateRunning). It drives
// maybeCompact directly with a NON-running session so ReplaceHistory returns the
// illegal-transition error, and asserts: a WARN line with the error kv fires, AND
// maybeCompact reports it did NOT compact (the history is left intact / the run
// would continue uncompacted). This is the defensive branch the full-loop
// TestCompactionFailureEmitsWarn (Compact()-error branch) cannot reach, since the
// session is always running by the time the loop calls maybeCompact.
func TestCompactionReplaceRejectedEmitsWarn(t *testing.T) {
	diag := newInternalCapturingDiag()
	e := NewEngine(Deps{
		Compactor:       succeedingCompactor{},
		TokenCounter:    hugeTokenCounter{},
		ContextWindow:   func() int { return 100 },
		CompactionRatio: 0.8,
		Diagnostics:     diag,
		Model:           "m",
	})

	// A freshly-created session is StateIdle (NOT StateRunning), so ReplaceHistory
	// rejects with an illegal-transition error — exactly the branch under test.
	sess := session.New("sess-replace", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := sess.SeedHistory([]session.Message{session.NewUserMessage(strings.Repeat("history", 20))}); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	original := append([]session.Message(nil), sess.Conversation.Messages...)

	// Bind the run-scoped diag the way Engine.Run does, then call maybeCompact.
	r := &Run{diag: e.bindRunDiag(sess.ID)}
	req := port.LLMRequest{}
	compacted := e.maybeCompact(context.Background(), r, sess, 0, &req)

	if compacted {
		t.Fatal("maybeCompact reported it compacted, want false (ReplaceHistory rejected the replacement)")
	}
	// History must be unchanged: the rejected replacement is not applied.
	if len(sess.Conversation.Messages) != len(original) {
		t.Fatalf("history length = %d, want %d (rejected replacement must not be applied)",
			len(sess.Conversation.Messages), len(original))
	}

	records := diag.snapshot()
	var rec internalDiagRecord
	var found bool
	for _, r := range records {
		if strings.Contains(r.msg, "session rejected") {
			rec, found = r, true
			break
		}
	}
	if !found {
		t.Fatalf("no ReplaceHistory-reject WARN line; records=%v", records)
	}
	if rec.level != port.LevelWarn {
		t.Fatalf("ReplaceHistory-reject level = %v, want LevelWarn", rec.level)
	}
	if _, ok := rec.attrs["error"]; !ok {
		t.Fatalf("ReplaceHistory-reject line missing error kv; attrs=%v", rec.attrs)
	}
	if got, _ := rec.attrs["session"].(string); got != "sess-replace" {
		t.Fatalf("ReplaceHistory-reject line session = %q, want %q", got, "sess-replace")
	}
}

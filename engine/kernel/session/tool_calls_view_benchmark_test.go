package session

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/views"
)

var toolCallsBenchmarkSink ToolCallsView
var toolCallTraversalSink int
var toolCallCopySink []byte
var toolCallPrerequisitesSink []views.SliceView[uint64, RequestedCallID]

// Scope-bearing prerequisite adapters can allocate on capture. Keep their
// construction distinct from repeatable borrowed traversal.
func captureToolCallPrerequisites(calls ToolCallsView) []views.SliceView[uint64, RequestedCallID] {
	result := make([]views.SliceView[uint64, RequestedCallID], calls.Len())
	for i, call := range calls.All() {
		invocation, _ := call.Invocation()
		result[i] = invocation.Prerequisites()
	}
	return result
}

func settledToolCallViews(t testing.TB, responses, count, field, sibling int) map[string]*Session {
	t.Helper()
	s := toolCallViewSession(t)
	for range responses {
		ids := toolCallViewResponse(t, s, count, field, sibling)
		interactionSubmit(t, s, func(b *SessionCommit) {
			for i, id := range ids {
				invocation := toolCallViewIntent(sibling)
				if i > 0 {
					invocation.Prerequisites = []uint64{1}
				}
				interactionCheck(t, b.AddToolInvocation(id, invocation))
			}
		})
		interactionSubmit(t, s, func(b *SessionCommit) {
			interactionCheck(t, b.AddToolDisposition(ids[0], toolCallViewReturned(field, sibling)))
		})
		if len(ids) > 1 {
			interactionSubmit(t, s, func(b *SessionCommit) {
				for _, id := range ids[1:] {
					interactionCheck(t, b.AddToolDisposition(id, toolCallViewReturned(field, sibling)))
				}
			})
		}
	}
	interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedCompleted)) })
	loaded, err := Load(t.Context(), s.history, s.Snapshot().HistoryID(), s.policy)
	interactionCheck(t, err)
	return map[string]*Session{"Submit": s, "Load": loaded}
}

// Traversal visits the join and its nested borrowed children, without exporting
// DTOs or bytes. The cost depends on call/child counts, not sibling byte lengths.
func traverseToolCallViews(calls ToolCallsView, prerequisites []views.SliceView[uint64, RequestedCallID]) int {
	n := 0
	for i, call := range calls.All() {
		n += len(call.RequestedCall().Name()) + call.RequestedCall().Arguments().Len()
		invocation, _ := call.Invocation()
		n += len(invocation.Name()) + invocation.Arguments().Len()
		objectValue, _ := invocation.Arguments().Get("nested")
		object, _ := objectValue.Object()
		arrayValue, _ := object.Get("items")
		array, _ := arrayValue.Array()
		n += array.Len()
		for _, prerequisite := range prerequisites[i].All() {
			if prerequisite.RequestID() != call.ID().RequestID() {
				panic("lost prerequisite scope")
			}
			n++
		}
		settlement, _ := call.Settlement()
		n += int(settlement.Category())
		disposition, _ := settlement.ExplicitDisposition()
		diagnostic, _ := disposition.Diagnostic()
		n += len(diagnostic)
		returned, _ := disposition.Returned()
		n += len(returned.Status()) + len(returned.Output().Capture())
		for _, part := range returned.Output().Parts().All() {
			if data, ok := part.JSON(); ok {
				n += data.Len()
			}
			if content, ok := part.Content(); ok {
				text, _ := content.Text()
				n += len(text)
			}
		}
	}
	return n
}

func TestToolCallsBorrowAndSelectedCopy(t *testing.T) {
	for _, axis := range []struct {
		name           string
		field, sibling int
	}{{"small", 32, 32}, {"requested-large", 64 << 10, 32}, {"sibling-large", 32, 64 << 10}} {
		t.Run(axis.name, func(t *testing.T) {
			for seam, s := range settledToolCallViews(t, 1, 2, axis.field, axis.sibling) {
				t.Run(seam, func(t *testing.T) {
					calls := s.Snapshot().RunAt(0).ToolCalls()
					prerequisites := captureToolCallPrerequisites(calls)
					if calls.Len() > 1 && prerequisites[1].Len() != 1 {
						t.Fatal("benchmark fixture has no scoped prerequisite traversal")
					}
					if n := testing.AllocsPerRun(100, func() { toolCallTraversalSink = traverseToolCallViews(calls, prerequisites) }); n != 0 {
						t.Fatalf("borrowed traversal allocated %g times", n)
					}
					call := calls.At(0)
					disposition, _ := call.Disposition()
					returned, _ := disposition.Returned()
					json, _ := returned.Output().Parts().At(0).JSON()
					for _, leaf := range []struct {
						copy func() []byte
						size int
					}{{call.RequestedCall().Arguments().Copy, axis.field}, {json.Copy, axis.field + len(` {"x":""} `)}} {
						if n := testing.AllocsPerRun(100, func() { toolCallCopySink = leaf.copy() }); n != 1 {
							t.Fatalf("selected Copy allocated %g times", n)
						}
						if len(toolCallCopySink) != leaf.size {
							t.Fatal("selected Copy length")
						}
						toolCallCopySink[0] ^= 0xff
						if leaf.copy()[0] == toolCallCopySink[0] {
							t.Fatal("Copy mutated pinned backing")
						}
					}
				})
			}
		})
	}
}

func BenchmarkRunToolCalls(b *testing.B) {
	for _, axis := range []struct {
		name                             string
		responses, count, field, sibling int
	}{
		{"small", 1, 1, 32, 32},
		{"calls-32", 1, 32, 32, 32},
		{"calls-256", 1, 256, 32, 32},
		{"responses-4", 4, 32, 32, 32},
		{"requested-large", 1, 1, 64 << 10, 32},
		{"sibling-large", 1, 1, 32, 64 << 10},
	} {
		b.Run(axis.name, func(b *testing.B) {
			for seam, s := range settledToolCallViews(b, axis.responses, axis.count, axis.field, axis.sibling) {
				b.Run(seam, func(b *testing.B) {
					run := s.Snapshot().RunAt(0)
					calls := run.ToolCalls()
					prerequisites := captureToolCallPrerequisites(calls)
					if calls.Len() > 1 && prerequisites[1].Len() != 1 {
						b.Fatal("benchmark fixture has no scoped prerequisite traversal")
					}
					b.Run("capture-prerequisites", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							toolCallPrerequisitesSink = captureToolCallPrerequisites(calls)
						}
					})
					b.Run("construct", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							toolCallsBenchmarkSink = run.ToolCalls()
						}
					})
					b.Run("traverse-borrowed", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							toolCallTraversalSink = traverseToolCallViews(calls, prerequisites)
						}
					})
					b.Run("leaf-copy", func(b *testing.B) {
						leaf := calls.At(0).RequestedCall().Arguments()
						b.ReportAllocs()
						for b.Loop() {
							toolCallCopySink = leaf.Copy()
						}
					})
				})
			}
		})
	}
}

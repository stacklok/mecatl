package session_test

import (
	"strconv"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

// scopedInputOperations includes scope capture in immediate traversal and forces
// collection/iterator escape separately. None materializes an element slice.
func scopedInputOperations(v session.EffectiveInputView) []iteratorTraversal {
	return []iteratorTraversal{
		{"indexed", func() {
			inputs := v.Inputs()
			for i := range inputs.Len() {
				iteratorItemSink = inputs.At(i)
			}
		}},
		{"immediate", func() {
			for _, item := range v.Inputs().All() {
				iteratorItemSink = item
			}
		}},
		{"escaped-collection", func() { iteratorInputsSink = v.Inputs() }},
		{"escaped-iterator", func() { iteratorInputSeqSink = v.Inputs().All() }},
	}
}

func BenchmarkScopedInputCollection(b *testing.B) {
	for _, axis := range iteratorAxes {
		b.Run(axis.name, func(b *testing.B) {
			for seam, s := range iteratorFixture(b, axis.count, axis.payload) {
				b.Run(seam, func(b *testing.B) {
					v := iteratorViewSet(b, s).effective
					for _, operation := range scopedInputOperations(v) {
						b.Run(operation.name, func(b *testing.B) {
							b.ReportAllocs()
							for b.Loop() {
								operation.run()
							}
						})
					}
				})
			}
		})
	}
}

func TestScopedInputCollectionAllocations(t *testing.T) {
	for _, axis := range iteratorAxes {
		for seam, s := range iteratorFixture(t, axis.count, axis.payload) {
			t.Run(axis.name+"/"+seam, func(t *testing.T) {
				v := iteratorViewSet(t, s).effective
				for _, operation := range scopedInputOperations(v) {
					t.Run(operation.name, func(t *testing.T) {
						// Permit bounded closure state, not allocation or copying
						// proportional to collection length or payload size.
						if n := testing.AllocsPerRun(100, operation.run); n > 2 {
							t.Fatalf("scope capture allocated %g times", n)
						}
						// MemStats is process-wide; use the same bounded resampling
						// as the context-free traversal check to exclude runtime noise.
						bytes := allocatedViewBytes(operation.run)
						for range 2 {
							if bytes <= 128 {
								break
							}
							bytes = min(bytes, allocatedViewBytes(operation.run))
						}
						if bytes > 128 {
							t.Fatalf("scope capture allocated %d bytes", bytes)
						}
					})
				}
				inputs := v.Inputs()
				sequenceContract(t, iteratorValues(inputs.All()), axis.count, inputs.At)()
			})
		}
	}
}

func TestEffectiveInputsRetainEnclosingRun(t *testing.T) {
	h, s, policy := newIteratorSession(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	var runs []session.RunID
	var ids []session.UserInputID
	for i := range 2 {
		run := batchRun(t, b)
		id, err := b.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{{Text: new(strconv.Itoa(i))}}})
		mustBatch(t, err)
		mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{
			{UserInputID: new(id.RunRelative())},
			{Inline: &session.AttributedInput{Actor: "context", Purpose: session.PromptContext, Source: session.ContentSource{Description: strconv.Itoa(i)}}},
			{UserInputID: new(id.RunRelative())},
		}}))
		mustBatch(t, b.EndRun(session.RunEndedInterrupted))
		runs = append(runs, run)
		ids = append(ids, id)
	}
	submitBatch(t, s, b)
	if ids[0].RunRelative() != ids[1].RunRelative() || ids[0] == ids[1] {
		t.Fatal("fixture must reuse local identity across distinct runs")
	}
	for seam, candidate := range iteratorSessions(t, h, s, policy) {
		t.Run(seam, func(t *testing.T) {
			snap := candidate.Snapshot()
			var retained [][]session.InputItemView
			var checks []func()
			for i := range runs {
				effective, ok := snap.RunAt(i).RecordAt(2).EffectiveInput()
				if !ok || effective.RunID() != runs[i] {
					t.Fatal("missing scoped effective input")
				}
				inputs := effective.Inputs()
				checks = append(checks, sequenceContract(t, iteratorValues(inputs.All()), 3, inputs.At))
				var items []session.InputItemView
				for index, item := range inputs.All() {
					if index != len(items) {
						t.Fatal("wrong index")
					}
					items = append(items, item)
				}
				retained = append(retained, items)
			}
			// Both materialized sets outlive collection construction in the other run.
			for repeat := range 2 {
				for i, items := range retained {
					checks[i]()
					for _, index := range []int{0, 2} {
						id, ok := items[index].UserInputID()
						resolved, found := snap.UserInputByID(id)
						if !ok || !found || id != ids[i] || resolved.ID() != ids[i] {
							t.Fatalf("iteration %d: enclosing run crossed into retained elements", repeat)
						}
					}
					inline, ok := items[1].Inline()
					if !ok || inline.Parts().Len() != 0 || inline.Source().Description() != strconv.Itoa(i) {
						t.Fatal("present-empty inline lost backing")
					}
				}
			}
		})
	}
}

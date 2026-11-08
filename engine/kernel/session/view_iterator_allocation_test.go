package session_test

import (
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

var (
	iteratorPartSink   session.ContentPartView
	iteratorItemSink   session.InputItemView
	iteratorBlockSink  session.InstructionBlockView
	iteratorToolSink   session.ToolDefinitionView
	iteratorRecordSink session.RecordView
	iteratorRunSink    session.RunView
	iteratorCountSink  int
	iteratorHeapSink   *session.ContentPartView
	iteratorBytesSink  []byte
	iteratorPartsSink  []session.ContentPartView
	iteratorSeqSink    iter.Seq[session.ContentPartView]
)

type iteratorTraversal struct {
	name string
	run  func()
}

// Keep concrete public calls in these consumers: factoring iteration into a
// generic/opaque helper would measure a different allocation/escape contract.
func iteratorTraversals(v iteratorViews) []iteratorTraversal {
	return []iteratorTraversal{
		{"user/direct", func() {
			n := 0
			for p := range v.user.Parts() {
				iteratorPartSink = p
				n++
			}
			iteratorCountSink = n
		}},
		{"user/reused", func() {
			seq := v.user.Parts()
			n := 0
			for range 2 {
				for p := range seq {
					iteratorPartSink = p
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"inline/direct", func() {
			n := 0
			for p := range v.inline.Parts() {
				iteratorPartSink = p
				n++
			}
			iteratorCountSink = n
		}},
		{"inline/reused", func() {
			seq := v.inline.Parts()
			n := 0
			for range 2 {
				for p := range seq {
					iteratorPartSink = p
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"block/direct", func() {
			n := 0
			for p := range v.block.Parts() {
				iteratorPartSink = p
				n++
			}
			iteratorCountSink = n
		}},
		{"block/reused", func() {
			seq := v.block.Parts()
			n := 0
			for range 2 {
				for p := range seq {
					iteratorPartSink = p
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"instructions/direct", func() {
			n := 0
			for b := range v.instructions.Blocks() {
				iteratorBlockSink = b
				n++
			}
			iteratorCountSink = n
		}},
		{"instructions/reused", func() {
			seq := v.instructions.Blocks()
			n := 0
			for range 2 {
				for b := range seq {
					iteratorBlockSink = b
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"effective/direct", func() {
			n := 0
			for item := range v.effective.Inputs() {
				iteratorItemSink = item
				n++
			}
			iteratorCountSink = n
		}},
		{"effective/reused", func() {
			seq := v.effective.Inputs()
			n := 0
			for range 2 {
				for item := range seq {
					iteratorItemSink = item
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"catalog/direct", func() {
			n := 0
			for tool := range v.catalog.Tools() {
				iteratorToolSink = tool
				n++
			}
			iteratorCountSink = n
		}},
		{"catalog/reused", func() {
			seq := v.catalog.Tools()
			n := 0
			for range 2 {
				for tool := range seq {
					iteratorToolSink = tool
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"run-records/direct", func() {
			n := 0
			for record := range v.run.Records() {
				iteratorRecordSink = record
				n++
			}
			iteratorCountSink = n
		}},
		{"run-records/reused", func() {
			seq := v.run.Records()
			n := 0
			for range 2 {
				for record := range seq {
					iteratorRecordSink = record
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"snapshot-records/direct", func() {
			n := 0
			for record := range v.snapshot.Records() {
				iteratorRecordSink = record
				n++
			}
			iteratorCountSink = n
		}},
		{"snapshot-records/reused", func() {
			seq := v.snapshot.Records()
			n := 0
			for range 2 {
				for record := range seq {
					iteratorRecordSink = record
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"snapshot-runs/direct", func() {
			n := 0
			for run := range v.snapshot.Runs() {
				iteratorRunSink = run
				n++
			}
			iteratorCountSink = n
		}},
		{"snapshot-runs/reused", func() {
			seq := v.snapshot.Runs()
			n := 0
			for range 2 {
				for run := range seq {
					iteratorRunSink = run
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"nested/instructions", func() {
			n := 0
			for block := range v.instructions.Blocks() {
				for part := range block.Parts() {
					iteratorPartSink = part
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"nested/effective", func() {
			n := 0
			for item := range v.effective.Inputs() {
				inline, _ := item.Inline()
				for part := range inline.Parts() {
					iteratorPartSink = part
					n++
				}
			}
			iteratorCountSink = n
		}},
		{"nested/records", func() {
			n := 0
			for record := range v.snapshot.Records() {
				if user, ok := record.UserInput(); ok {
					for part := range user.Parts() {
						iteratorPartSink = part
						n++
					}
				}
				if catalog, ok := record.ToolCatalog(); ok {
					for tool := range catalog.Tools() {
						iteratorToolSink = tool
						n++
					}
				}
			}
			iteratorCountSink = n
		}},
		{"nested/history", func() {
			n := 0
			for run := range v.snapshot.Runs() {
				for record := range run.Records() {
					iteratorRecordSink = record
					n++
				}
			}
			iteratorCountSink = n
		}},
	}
}

var iteratorAxes = []struct {
	name           string
	count, payload int
}{
	{"N1", 1, 32}, {"N32", 32, 32}, {"N1024", 1024, 32},
	{"payload32B", 4, 32}, {"payload32KiB", 4, 32 << 10}, {"payload64KiB", 4, 64 << 10},
}

// assertIteratorNoAllocation times only traversal; fixture and consumer closure
// construction happen before this check. Both allocations and copied bytes matter.
func assertIteratorNoAllocation(t *testing.T, traverse func()) {
	t.Helper()
	if n := testing.AllocsPerRun(100, traverse); n != 0 {
		t.Fatalf("complete traversal allocated %g times", n)
	}
	// MemStats is process-wide: unrelated runtime work can contaminate a sample,
	// especially under -race. Keep the minimum of three bounded samples; a
	// traversal allocation repeats in every sample and remains a failure.
	bytes := allocatedViewBytes(traverse)
	for range 2 {
		if bytes == 0 {
			break
		}
		bytes = min(bytes, allocatedViewBytes(traverse))
	}
	if bytes != 0 {
		t.Fatalf("complete traversal allocated %d bytes/op", bytes)
	}
}

func TestViewIteratorRecordTraversalAllocations(t *testing.T) {
	for _, count := range []int{1, 32, 1024} {
		for seam, v := range iteratorRecordFixture(t, count) {
			for _, check := range iteratorTraversals(v) {
				if !strings.Contains(check.name, "-records/") {
					continue
				}
				t.Run(strconv.Itoa(count)+"/"+seam+"/"+check.name, func(t *testing.T) {
					assertIteratorNoAllocation(t, check.run)
					want := count
					if strings.HasSuffix(check.name, "/reused") {
						want *= 2
					}
					if iteratorCountSink != want {
						t.Fatalf("traversed %d records, want %d", iteratorCountSink, want)
					}
				})
			}
		}
	}
}

func TestViewIteratorTraversalAllocations(t *testing.T) {
	for _, axis := range iteratorAxes {
		t.Run(axis.name, func(t *testing.T) {
			for seam, s := range iteratorFixture(t, axis.count, axis.payload) {
				t.Run(seam, func(t *testing.T) {
					v := iteratorViewSet(t, s)
					for _, check := range iteratorTraversals(v) {
						t.Run(check.name, func(t *testing.T) {
							assertIteratorNoAllocation(t, check.run)
							family, mode, _ := strings.Cut(check.name, "/")
							want := axis.count
							switch family {
							case "run-records":
								want = v.run.RecordCount()
							case "snapshot-records":
								want = v.snapshot.RecordCount()
							case "nested":
								want = 2*axis.count - 1
								switch mode {
								case "history":
									want = v.snapshot.RecordCount() - 1 // Creation precedes every run.
								case "records":
									want = 2 * axis.count
								}
							}
							if mode == "reused" {
								want *= 2
							}
							if iteratorCountSink != want {
								t.Fatalf("traversed %d elements, want %d", iteratorCountSink, want)
							}
						})
					}
					viewBytes := int(reflect.TypeFor[session.ContentPartView]().Size())
					controls := []struct {
						name   string
						run    func()
						allocs int
						bytes  int
					}{
						{"per-element", func() {
							for part := range v.user.Parts() {
								iteratorHeapSink = new(session.ContentPartView)
								*iteratorHeapSink = part
							}
						}, axis.count, axis.count * viewBytes},
						{"owned-bytes", func() {
							for part := range v.user.Parts() {
								media, _ := part.Media()
								data, _ := media.InlineData()
								iteratorBytesSink = data.Copy()
							}
						}, axis.count, axis.count * axis.payload},
						{"collect", func() { iteratorPartsSink = slices.Collect(v.user.Parts()) }, 1, axis.count * viewBytes},
					}
					for _, control := range controls {
						t.Run("control/"+control.name, func(t *testing.T) {
							if n := testing.AllocsPerRun(10, control.run); n < float64(control.allocs) {
								t.Fatalf("allocation control missed slope: %g", n)
							}
							if n := allocatedViewBytes(control.run); n < uint64(control.bytes) {
								t.Fatalf("byte control missed copies/collection: %d", n)
							}
						})
					}
				})
			}
		})
	}
}

// Opaque iteration can allocate range/yield state per consumption. In nested
// consumers that cost accumulates per block; it is not copying content storage.
//
//go:noinline
func consumeIteratorParts(seq iter.Seq[session.ContentPartView]) {
	n := 0
	for part := range seq {
		iteratorPartSink = part
		n++
	}
	iteratorCountSink = n
}

func BenchmarkViewIteratorTraversals(b *testing.B) {
	for _, axis := range iteratorAxes {
		b.Run(axis.name, func(b *testing.B) {
			for seam, s := range iteratorFixture(b, axis.count, axis.payload) {
				b.Run(seam, func(b *testing.B) {
					v := iteratorViewSet(b, s)
					checks := iteratorTraversals(v)
					stored := v.user.Parts() // Escaping setup is outside consumption timing.
					checks = append(checks, iteratorTraversal{"opaque", func() { consumeIteratorParts(v.user.Parts()) }},
						iteratorTraversal{"escaping-create", func() { iteratorSeqSink = v.user.Parts() }},
						iteratorTraversal{"stored-consume", func() { consumeIteratorParts(stored) }},
						iteratorTraversal{"stored-create-consume", func() { iteratorSeqSink = v.user.Parts(); consumeIteratorParts(iteratorSeqSink) }},
						iteratorTraversal{"nested-opaque", func() {
							for block := range v.instructions.Blocks() {
								consumeIteratorParts(block.Parts())
							}
						}})
					for _, check := range checks {
						b.Run(check.name, func(b *testing.B) {
							b.ReportAllocs()
							b.ResetTimer()
							for b.Loop() {
								check.run()
							}
						})
					}
				})
			}
		})
	}
}

package session

import "testing"

var usageSnapshotSink Snapshot
var usageCommitSink CommitView
var usageEntriesSink UsageEntriesView

// Visit nested borrowed children without exporting DTOs or bytes. Scalar reads
// on one bucket must not clone the populated sibling or report-wide evidence.
func traverseUsageEntries(entries UsageEntriesView) int64 {
	var total int64
	for i, entry := range entries.All() {
		looked, ok := entries.ByID(entry.ID())
		if !ok || looked != entry || entries.At(i) != entry {
			panic("lookup changed joined entry")
		}
		definition := entry.Definition()
		total += int64(len(definition.Purpose()))
		_, _ = definition.RunID()
		_, _ = definition.RequestID()
		report := entry.Report()
		total += int64(len(report.State()))
		for _, bucket := range report.Buckets().All() {
			route, _ := bucket.SelectedRoute()
			model, _ := bucket.SelectedModel()
			total += int64(len(route) + len(model))
			c := bucket.Counters()
			for _, read := range [...]func() (int64, bool){c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning} {
				if n, ok := read(); ok {
					total += n
				}
			}
		}
		if raw, ok := report.Raw(); ok {
			total += int64(len(raw.Producer())+len(raw.Format())) + raw.Version()
			for _, b := range raw.Data().All() {
				total += int64(b)
			}
		}
	}
	return total
}

func TestUsageEntriesAllocations(t *testing.T) {
	for _, sibling := range []int{32, 64 << 10} {
		s := usageEntriesFixture(t, 2, 3, sibling)
		loaded, err := Load(t.Context(), s.history, s.Snapshot().HistoryID(), s.policy, testConfig())
		mustBatch(t, err)
		for seam, owner := range map[string]*Session{"Submit": s, "Load": loaded} {
			t.Run(seam, func(t *testing.T) {
				snapshot := owner.Snapshot()
				joined := snapshot.Usages()
				batch := beginBatch(t, owner)
				defer func() { mustBatch(t, batch.Discard()) }()
				// Include populated proposed accounting, not only an empty builder.
				mustBatch(t, batch.ReplaceUsage(joined.At(0).ID(), usageReportFixture()))
				capture := testing.AllocsPerRun(100, func() { usageSnapshotSink = owner.Snapshot() })
				commitCapture := testing.AllocsPerRun(100, func() {
					var err error
					usageCommitSink, err = batch.View()
					if err != nil {
						panic(err)
					}
				})
				construct := testing.AllocsPerRun(100, func() { usageEntriesSink = snapshot.Usages() })
				traverse := testing.AllocsPerRun(100, func() { usageReadSink = traverseUsageEntries(joined) })
				raw, ok := joined.At(0).Report().Raw()
				if !ok || raw.Data().Len() != sibling || joined.At(0).Report().Buckets().Len() != 2 {
					t.Fatal("empty allocation fixture")
				}
				copyLeaf := testing.AllocsPerRun(100, func() { usageByteSink = raw.Data().Copy() })
				t.Logf("sibling=%d snapshot=%g commit=%g join=%g traversal=%g leaf-copy=%g allocs", sibling, capture, commitCapture, construct, traverse, copyLeaf)
				if capture != 0 || commitCapture != 0 || construct != 1 || traverse != 0 || copyLeaf != 1 {
					t.Fatal("capture/traversal copied backing or join/leaf allocations changed")
				}
				// Exact borrowed traversal oracle: both identities have the fixture's
				// purpose, state, labels, counters and every opaque byte.
				want := int64(2 * (len(UsagePurposeMain) + len(UsageReportActive) + len(" Route ")*2 + len("Model") + sibling + 18 + len("p") + len("usage") + 1 + sibling*255))
				if usageReadSink != want {
					t.Fatalf("borrowed traversal = %d, want %d", usageReadSink, want)
				}
			})
		}
	}
}

func BenchmarkSnapshotUsages(b *testing.B) {
	for _, axis := range []struct {
		name                              string
		identities, replacements, sibling int
	}{
		{"small", 2, 1, 32},
		{"identities-32", 32, 1, 32},
		{"reports-128", 2, 63, 32},
		{"sibling-large", 2, 1, 64 << 10},
	} {
		b.Run(axis.name, func(b *testing.B) {
			s := usageEntriesFixture(b, axis.identities, axis.replacements, axis.sibling)
			loaded, err := Load(b.Context(), s.history, s.Snapshot().HistoryID(), s.policy, testConfig())
			interactionCheck(b, err)
			for seam, owner := range map[string]*Session{"Submit": s, "Load": loaded} {
				b.Run(seam, func(b *testing.B) {
					snapshot := owner.Snapshot()
					entries := snapshot.Usages()
					batch := beginBatch(b, owner)
					defer func() { interactionCheck(b, batch.Discard()) }()
					interactionCheck(b, batch.ReplaceUsage(entries.At(0).ID(), usageReportFixture()))
					b.Run("capture-snapshot", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							usageSnapshotSink = owner.Snapshot()
						}
					})
					b.Run("capture-commit", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							var err error
							usageCommitSink, err = batch.View()
							if err != nil {
								b.Fatal(err)
							}
						}
					})
					b.Run("construct-join", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							usageEntriesSink = snapshot.Usages()
						}
					})
					b.Run("traverse-borrowed", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							usageReadSink = traverseUsageEntries(entries)
						}
					})
					b.Run("leaf-copy", func(b *testing.B) {
						raw, _ := entries.At(0).Report().Raw()
						b.ReportAllocs()
						for b.Loop() {
							usageByteSink = raw.Data().Copy()
						}
					})
				})
			}
		})
	}
}

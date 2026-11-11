package sqlite

import (
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

// Fixtures are test-owned temporary storage. These report local filesystem
// behavior only, not physical-media throughput or power-loss qualification.
func BenchmarkAppend(b *testing.B) {
	for _, size := range []int{1024, 64 << 10} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			backend, err := New(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				if err := backend.Close(); err != nil {
					b.Error(err)
				}
			}()
			id := history.NewHistoryID()
			c := largeProposal(1, size)
			// Exclude initial directory publication and include retained-handle WAL/FULL
			// transactions, including whatever standard auto-checkpoints they trigger.
			o, err := backend.Append(b.Context(), id, history.CommitRef{}, c)
			if err != nil {
				b.Fatal(err)
			}
			head := o.Head
			b.ReportAllocs()
			b.SetBytes(int64(len(c.Bytes)))
			b.ResetTimer()
			for range b.N {
				c.Ref.Sequence = head.Sequence + 1
				o, err = backend.Append(b.Context(), id, head, c)
				if err != nil || o.Head != c.Ref {
					b.Fatalf("append: %+v %v", o, err)
				}
				head = o.Head
			}
			b.StopTimer()
		})
	}
}

func BenchmarkReadPage(b *testing.B) {
	for _, count := range []int{1, 16} {
		b.Run(fmt.Sprintf("commits=%d", count), func(b *testing.B) {
			backend, err := New(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				if err := backend.Close(); err != nil {
					b.Error(err)
				}
			}()
			id := history.NewHistoryID()
			c := largeProposal(1, 4096)
			var head history.CommitRef
			for i := 1; i <= count; i++ {
				c.Ref.Sequence = int64(i)
				o, err := backend.Append(b.Context(), id, head, c)
				if err != nil || o.Head != c.Ref {
					b.Fatalf("fixture: %+v %v", o, err)
				}
				head = o.Head
			}
			request := history.ByteReadRequest{ReadRequest: history.ReadRequest{Through: head, MaxCommits: count}, MaxCommitBytes: 8192, MaxBytes: count * 8192}
			b.ReportAllocs()
			b.SetBytes(int64(count * len(c.Bytes)))
			b.ResetTimer()
			for range b.N {
				page, err := backend.Read(b.Context(), id, request)
				if err != nil || len(page) != count {
					b.Fatalf("read: count=%d %v", len(page), err)
				}
			}
			b.StopTimer()
		})
	}
}

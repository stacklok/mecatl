package mem

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// precheckedContext snapshots the initial backend context check before signaling.
// With the backend mutex held, cancellation then precedes any locked state access.
type precheckedContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (c *precheckedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestCancellationAfterInitialBackendCheck(t *testing.T) {
	id := history.NewHistoryID()
	c := format.Commit{FormatVersion: 1, HistoryID: id, Sequence: 1,
		Records: []format.Record{{Kind: "opaque", KindVersion: 1}}}
	encoded, cid, err := format.EncodeCommit(c, format.Limits{MaxCommitBytes: 1024, MaxRecords: 1, MaxKindBytes: 8, MaxPayloadBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	root := history.ByteCommit{Ref: history.CommitRef{Sequence: 1, CID: cid}, Bytes: encoded}
	// Backend bytes are opaque; only the successor's index metadata matters here.
	second := history.ByteCommit{Ref: history.CommitRef{Sequence: 2, CID: cid}, Bytes: []byte{1}}
	for _, operation := range []string{"Observe", "Append", "Read"} {
		t.Run(operation, func(t *testing.T) {
			backend := New()
			if got, err := backend.Append(context.Background(), id, history.CommitRef{}, root); err != nil || got != (history.Observation{Head: root.Ref, Commit: root.Ref}) {
				t.Fatalf("seed append = %+v, %v", got, err)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &precheckedContext{Context: base, checked: make(chan struct{})}
			done := make(chan struct{})
			backend.mu.Lock()
			go func() {
				defer close(done)
				switch operation {
				case "Observe":
					got, err := backend.Observe(ctx, id, 1)
					if !errors.Is(err, context.Canceled) || got != (history.Observation{}) {
						t.Errorf("observe = %+v, %v", got, err)
					}
				case "Append":
					got, err := backend.Append(ctx, id, root.Ref, second)
					if !errors.Is(err, context.Canceled) || got != (history.Observation{}) {
						t.Errorf("append = %+v, %v", got, err)
					}
				case "Read":
					got, err := backend.Read(ctx, id, history.ByteReadRequest{ReadRequest: history.ReadRequest{Through: root.Ref, MaxCommits: 1}, MaxBytes: 1024, MaxCommitBytes: 1024})
					if !errors.Is(err, context.Canceled) || got != nil {
						t.Errorf("read = %+v, %v", got, err)
					}
				}
			}()
			<-ctx.checked
			cancel()
			backend.mu.Unlock()
			<-done
			if got, err := backend.Observe(context.Background(), id, 2); err != nil || got != (history.Observation{Head: root.Ref}) {
				t.Fatalf("canceled operation changed history: %+v, %v", got, err)
			}
		})
	}
}

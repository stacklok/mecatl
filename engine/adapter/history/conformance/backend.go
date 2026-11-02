package conformance

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// backendContract checks the byte seam independently of History's protocol and
// codecs. References come from valid DTO fixtures, but stored bytes are opaque.
func backendContract(t *testing.T, factory Factory) {
	b := factory(t)
	ctx := context.Background()
	id := history.NewHistoryID()
	_, root := encoded(t, id, history.CommitRef{}, "root")
	_, second := encoded(t, id, root, "second")
	_, competing := encoded(t, id, history.CommitRef{}, "competing")
	input := []byte{0xff, 1, 2, 3} // Deliberately not a canonical commit.
	original := bytes.Clone(input)
	got, err := b.Observe(ctx, id, 1)
	if err != nil || got != (history.Observation{}) {
		t.Fatalf("absent observation = %+v, %v", got, err)
	}
	accepted, err := b.Append(ctx, id, history.CommitRef{}, history.ByteCommit{Ref: root, Bytes: input})
	if err != nil || accepted != (history.Observation{Head: root, Commit: root}) {
		t.Fatalf("opaque append = %+v, %v", accepted, err)
	}
	input[0] ^= 0xff
	accepted, err = b.Append(ctx, id, root, history.ByteCommit{Ref: second, Bytes: []byte{4, 5, 6}})
	if err != nil || accepted != (history.Observation{Head: second, Commit: second}) {
		t.Fatalf("successor CAS = %+v, %v", accepted, err)
	}
	for _, test := range []struct {
		ordinal int64
		ref     history.CommitRef
	}{{0, history.CommitRef{}}, {1, root}, {2, second}, {3, history.CommitRef{}}} {
		got, err := b.Observe(ctx, id, test.ordinal)
		if err != nil || got != (history.Observation{Head: second, Commit: test.ref}) {
			t.Fatalf("ordinal %d = %+v, %v", test.ordinal, got, err)
		}
	}
	for _, ref := range []history.CommitRef{root, competing} {
		got, err := b.Append(ctx, id, history.CommitRef{}, history.ByteCommit{Ref: ref, Bytes: []byte{8}})
		if err != nil || got != (history.Observation{Head: second, Commit: root}) {
			t.Fatalf("failed CAS evidence = %+v, %v", got, err)
		}
	}
	r := history.ByteReadRequest{ReadRequest: history.ReadRequest{Through: second, MaxCommits: 2}, MaxBytes: 7, MaxCommitBytes: 4}
	for _, mutate := range []func(*history.ByteReadRequest){
		func(r *history.ByteReadRequest) { r.MaxBytes = 0 },
		func(r *history.ByteReadRequest) { r.MaxBytes = -1 },
		func(r *history.ByteReadRequest) { r.MaxCommitBytes = 0 },
		func(r *history.ByteReadRequest) { r.MaxCommitBytes = -1 },
		func(r *history.ByteReadRequest) { r.MaxCommits = 0 },
		func(r *history.ByteReadRequest) { r.MaxCommits = -1 },
	} {
		invalid := r
		mutate(&invalid)
		page, err := b.Read(ctx, id, invalid)
		if !errors.Is(err, history.ErrInvalid) || page != nil {
			t.Fatalf("invalid byte budget = %+v, %v", page, err)
		}
	}
	page, err := b.Read(ctx, id, r)
	if err != nil || len(page) != 2 || page[0].Ref != root || page[1].Ref != second || !bytes.Equal(page[0].Bytes, original) {
		t.Fatalf("byte read = %+v, %v", page, err)
	}
	page[0].Bytes[0] ^= 0xff
	page[0] = history.ByteCommit{}
	page, err = b.Read(ctx, id, r)
	if err != nil || !bytes.Equal(page[0].Bytes, original) {
		t.Fatalf("read alias = %+v, %v", page, err)
	}
	r.MaxCommits = 1
	page, err = b.Read(ctx, id, r)
	if err != nil || len(page) != 1 {
		t.Fatalf("count cap = %+v, %v", page, err)
	}
	r.MaxCommits, r.MaxBytes = 2, 6
	page, err = b.Read(ctx, id, r)
	if err != nil || len(page) != 1 {
		t.Fatalf("total byte cap = %+v, %v", page, err)
	}
	r.MaxBytes = 3
	page, err = b.Read(ctx, id, r)
	var hint history.PageTooSmallError
	if !errors.As(err, &hint) || hint.RequiredBytes != 4 || page != nil {
		t.Fatalf("first commit budget = %+v, %v", page, err)
	}
	r.MaxBytes, r.MaxCommitBytes = 7, 3
	page, err = b.Read(ctx, id, r)
	if err == nil || page != nil {
		t.Fatalf("per-commit cap = %+v, %v", page, err)
	}
	r.MaxCommitBytes, r.After = 4, root
	page, err = b.Read(ctx, id, r)
	if err != nil || len(page) != 1 || page[0].Ref != second {
		t.Fatalf("before evidence = %+v, %v", page, err)
	}
	r.After = second
	page, err = b.Read(ctx, id, r)
	if err != nil || len(page) != 0 {
		t.Fatalf("byte EOF = %+v, %v", page, err)
	}
	r.Through, r.After = competing, history.CommitRef{}
	page, err = b.Read(ctx, id, r)
	if !errors.Is(err, history.ErrInvalid) || page != nil {
		t.Fatalf("foreign endpoint = %+v, %v", page, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := b.Observe(canceled, id, 1); !errors.Is(err, context.Canceled) || got != (history.Observation{}) {
		t.Fatalf("canceled Observe = %+v, %v", got, err)
	}
	if got, err := b.Append(canceled, history.NewHistoryID(), history.CommitRef{}, history.ByteCommit{Ref: root, Bytes: original}); !errors.Is(err, context.Canceled) || got != (history.Observation{}) {
		t.Fatalf("canceled Append = %+v, %v", got, err)
	}
	r.Through = second
	if got, err := b.Read(canceled, id, r); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled Read = %+v, %v", got, err)
	}
}

// backendAppendSequences checks index validation without History or commit codecs.
// Even a speculative expected head at MaxInt64 must fail before a CAS verdict.
func backendAppendSequences(t *testing.T, factory Factory) {
	cid, err := format.ParseCID("bafyreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                     string
		seed, expected, proposal int64
	}{
		{"non-1-root", 0, 0, 2},
		{"skipped-successor", 1, 1, 3},
		{"repeated-successor", 1, 1, 1},
		{"overflow-expected", 1, math.MaxInt64, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, ctx, id := factory(t), context.Background(), history.NewHistoryID()
			ref := func(sequence int64) history.CommitRef {
				if sequence == 0 {
					return history.CommitRef{}
				}
				return history.CommitRef{Sequence: sequence, CID: cid}
			}
			// These bytes are intentionally not a canonical commit; CIDs need only
			// be syntactically valid at this opaque storage boundary.
			opaque := []byte{0xff, 1}
			if test.seed != 0 {
				got, err := b.Append(ctx, id, history.CommitRef{}, history.ByteCommit{Ref: ref(1), Bytes: opaque})
				if err != nil || got != (history.Observation{Head: ref(1), Commit: ref(1)}) {
					t.Fatalf("seed = %+v, %v", got, err)
				}
			}
			got, err := b.Append(ctx, id, ref(test.expected), history.ByteCommit{Ref: ref(test.proposal), Bytes: opaque})
			if !errors.Is(err, history.ErrInvalid) || got != (history.Observation{}) {
				t.Fatalf("invalid sequence = %+v, %v; want zero and ErrInvalid", got, err)
			}
			checkOrdinals := func(head int64) {
				t.Helper()
				for _, sequence := range []int64{0, 1, 2, 3, math.MaxInt64} {
					var occupied history.CommitRef
					if sequence > 0 && sequence <= head {
						occupied = ref(sequence)
					}
					got, err := b.Observe(ctx, id, sequence)
					if err != nil || got != (history.Observation{Head: ref(head), Commit: occupied}) {
						t.Fatalf("head %d ordinal %d = %+v, %v", head, sequence, got, err)
					}
				}
			}
			checkOrdinals(test.seed)
			next := ref(test.seed + 1)
			got, err = b.Append(ctx, id, ref(test.seed), history.ByteCommit{Ref: next, Bytes: opaque})
			if err != nil || got != (history.Observation{Head: next, Commit: next}) {
				t.Fatalf("valid append after rejection = %+v, %v", got, err)
			}
			checkOrdinals(next.Sequence)
		})
	}
}

// boundedByteReadAllocation proves byte/count caps apply before copying retained
// data, including an excluded tail after a successful owned prefix.
// This intentionally is not part of the portable backend contract test.
func boundedByteReadAllocation(t *testing.T, factory Factory) {
	b := factory(t)
	id := history.NewHistoryID()
	_, root := encoded(t, id, history.CommitRef{}, "large opaque bytes")
	stored := bytes.Repeat([]byte{1}, largeInputBytes)
	if got, err := b.Append(context.Background(), id, history.CommitRef{}, history.ByteCommit{Ref: root, Bytes: stored}); err != nil || got != (history.Observation{Head: root, Commit: root}) {
		t.Fatalf("store opaque bytes = %+v, %v", got, err)
	}
	for _, test := range []struct {
		name               string
		pageCap, commitCap int
	}{
		{"page-cap", 1, largeInputBytes},
		{"commit-cap", largeInputBytes, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := history.ByteReadRequest{ReadRequest: history.ReadRequest{Through: root, MaxCommits: 1}, MaxBytes: test.pageCap, MaxCommitBytes: test.commitCap}
			allocated := totalAlloc(t, func() {
				for range rejectedAppendCalls {
					got, err := b.Read(context.Background(), id, r)
					if err == nil || got != nil {
						t.Fatalf("bounded read = %+v, %v", got, err)
					}
				}
			})
			if allocated > rejectedAppendCalls*rejectedAppendAllocationLimit {
				t.Fatalf("bounded read allocated %d bytes", allocated)
			}
		})
	}

	prefixID := history.NewHistoryID()
	_, small := encoded(t, prefixID, history.CommitRef{}, "small opaque bytes")
	_, large := encoded(t, prefixID, small, "large opaque tail")
	prefix := []byte{0xff, 2, 3, 4}
	var expected history.CommitRef
	for _, commit := range []history.ByteCommit{{Ref: small, Bytes: prefix}, {Ref: large, Bytes: stored}} {
		if got, err := b.Append(context.Background(), prefixID, expected, commit); err != nil || got != (history.Observation{Head: commit.Ref, Commit: commit.Ref}) {
			t.Fatalf("store prefix fixture = %+v, %v", got, err)
		}
		expected = commit.Ref
	}
	for _, test := range []struct {
		name                 string
		maxBytes, maxCommits int
	}{
		{"prefix-count-cap", len(prefix) + len(stored), 1},
		// The tail fits both per-commit and total caps, but not remaining bytes.
		{"prefix-remaining-byte-cap", len(stored), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := history.ByteReadRequest{ReadRequest: history.ReadRequest{Through: large, MaxCommits: test.maxCommits}, MaxBytes: test.maxBytes, MaxCommitBytes: len(stored)}
			allocated := totalAlloc(t, func() {
				for range rejectedAppendCalls {
					got, err := b.Read(context.Background(), prefixID, r)
					if err != nil || len(got) != 1 || got[0].Ref != small || !bytes.Equal(got[0].Bytes, prefix) {
						t.Fatalf("bounded prefix = %+v, %v", got, err)
					}
					// The next read must not see mutations to either returned slice.
					got[0].Bytes[0] ^= 0xff
					got[0] = history.ByteCommit{}
				}
			})
			if allocated > rejectedAppendCalls*rejectedAppendAllocationLimit {
				t.Fatalf("bounded prefix read allocated %d bytes; want at most %d", allocated, rejectedAppendCalls*rejectedAppendAllocationLimit)
			}
		})
	}
}

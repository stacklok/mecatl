package history_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// scriptedBackend isolates core trust-boundary tests from adapter behavior.
// Missing hooks panic so unexpected reads or hidden write retries fail the test.
type scriptedBackend struct {
	history.Backend
	observe func(context.Context, history.HistoryID, int64) (history.Observation, error)
	append  func(context.Context, history.HistoryID, history.CommitRef, history.ByteCommit) (history.Observation, error)
	read    func(context.Context, history.HistoryID, history.ByteReadRequest) ([]history.ByteCommit, error)
}

func (s scriptedBackend) Observe(ctx context.Context, id history.HistoryID, n int64) (history.Observation, error) {
	return s.observe(ctx, id, n)
}
func (s scriptedBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
	return s.append(ctx, id, expected, c)
}
func (s scriptedBackend) Read(ctx context.Context, id history.HistoryID, r history.ByteReadRequest) ([]history.ByteCommit, error) {
	return s.read(ctx, id, r)
}

func corePolicy() history.Policy {
	return history.Policy{CommitLimits: format.Limits{MaxCommitBytes: 4096, MaxRecords: 4, MaxKindBytes: 32, MaxPayloadBytes: 2048},
		RetryWindow: 2, MaxPageBytes: 16384, MaxPageCommits: 16}
}

func newHistory(t *testing.T, b history.Backend, p history.Policy) *history.History {
	t.Helper()
	h, err := history.New(b, p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func dto(id history.HistoryID, parent history.CommitRef, payload string) format.Commit {
	c := format.Commit{FormatVersion: 1, Sequence: parent.Sequence + 1,
		Records: []format.Record{{Kind: "opaque", KindVersion: 1, Payload: []byte(payload)}}}
	if parent.Sequence == 0 {
		c.HistoryID = id
	} else {
		c.Parent = parent.CID
	}
	return c
}

func byteCommit(t *testing.T, c format.Commit) history.ByteCommit {
	t.Helper()
	b, cid, err := format.EncodeCommit(c, corePolicy().CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	return history.ByteCommit{Ref: history.CommitRef{Sequence: c.Sequence, CID: cid}, Bytes: b}
}

func TestIdentifyIsLocalCanonicalAndTracksMutation(t *testing.T) {
	id := history.NewHistoryID()
	root := dto(id, history.CommitRef{}, "\x00\xffopaque")
	h := newHistory(t, scriptedBackend{}, corePolicy()) // Any backend call panics.
	r1, err := h.Identify(id, root)
	if err != nil || r1 != byteCommit(t, root).Ref {
		t.Fatalf("Identify = %+v, %v", r1, err)
	}
	second := dto(id, r1, "successor")
	for _, c := range []format.Commit{root, second} {
		before := c.Clone()
		ref, err := h.Identify(id, c)
		if err != nil || ref != byteCommit(t, c).Ref || !c.Equal(before) {
			t.Fatalf("Identify = %+v, %v; mutated input %v", ref, err, !c.Equal(before))
		}
		again, err := h.Identify(id, c.Clone())
		if err != nil || again != ref {
			t.Fatalf("repeated Identify = %+v, %v", again, err)
		}
		writes := 0
		submitter := newHistory(t, scriptedBackend{append: func(_ context.Context, gotID history.HistoryID, expected history.CommitRef, got history.ByteCommit) (history.Observation, error) {
			writes++
			wantParent := history.CommitRef{}
			if c.Sequence > 1 {
				wantParent = history.CommitRef{Sequence: c.Sequence - 1, CID: c.Parent}
			}
			if writes != 1 || gotID != id || expected != wantParent || got.Ref != ref || !reflect.DeepEqual(got.Bytes, byteCommit(t, c).Bytes) {
				t.Fatal("Identify and Append disagree")
			}
			return history.Observation{Head: ref, Commit: ref}, nil
		}}, corePolicy())
		got, err := submitter.Append(context.Background(), id, c)
		if err != nil || writes != 1 || got.Proposal != ref || got.Status != history.StatusAccepted {
			t.Fatalf("Append = %+v, %v; writes %d", got, err, writes)
		}
		c.Records[0].Payload[0] ^= 0xff
		mutated, err := h.Identify(id, c)
		if err != nil || mutated == ref {
			t.Fatalf("mutated Identify = %+v, %v", mutated, err)
		}
	}
	// A successor's scope is not in its envelope; local identity cannot prove
	// membership in a stored history, and must not query it.
	otherScope, err := h.Identify(history.NewHistoryID(), second)
	if err != nil || otherScope != byteCommit(t, second).Ref {
		t.Fatalf("successor scope = %+v, %v", otherScope, err)
	}
}

func TestIdentifyAndAppendRejectInputsWithoutBackendCalls(t *testing.T) {
	id := history.NewHistoryID()
	for _, test := range []struct {
		name   string
		mutate func(*history.HistoryID, *format.Commit, *history.Policy)
	}{
		{"missing-scope", func(id *history.HistoryID, _ *format.Commit, _ *history.Policy) { *id = history.HistoryID{} }},
		{"foreign-root", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { c.HistoryID = history.NewHistoryID() }},
		{"missing-dto", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { *c = format.Commit{} }},
		{"missing-sequence", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { c.Sequence = 0 }},
		{"negative-sequence", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { c.Sequence = -1 }},
		{"missing-records", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { c.Records = nil }},
		{"invalid-record", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { c.Records[0].KindVersion = 0 }},
		{"version", func(_ *history.HistoryID, c *format.Commit, _ *history.Policy) { c.FormatVersion = 2 }},
		{"commit-byte-limit", func(_ *history.HistoryID, _ *format.Commit, p *history.Policy) { p.CommitLimits.MaxCommitBytes = 1 }},
		{"record-count-limit", func(_ *history.HistoryID, c *format.Commit, p *history.Policy) {
			c.Records = append(c.Records, c.Records[0])
			p.CommitLimits.MaxRecords = 1
		}},
		{"kind-byte-limit", func(_ *history.HistoryID, _ *format.Commit, p *history.Policy) { p.CommitLimits.MaxKindBytes = 1 }},
		{"payload-byte-limit", func(_ *history.HistoryID, _ *format.Commit, p *history.Policy) { p.CommitLimits.MaxPayloadBytes = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope, c, p := id, dto(id, history.CommitRef{}, "root"), corePolicy()
			test.mutate(&scope, &c, &p)
			h := newHistory(t, scriptedBackend{}, p)
			if got, err := h.Identify(scope, c); !errors.Is(err, history.ErrInvalid) || got != (history.CommitRef{}) {
				t.Fatalf("Identify = %+v, %v", got, err)
			}
			if got, err := h.Append(context.Background(), scope, c); !errors.Is(err, history.ErrInvalid) || got != (history.Resolution{}) {
				t.Fatalf("Append = %+v, %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := newHistory(t, scriptedBackend{}, corePolicy())
	if got, err := h.Append(ctx, id, dto(id, history.CommitRef{}, "root")); !errors.Is(err, context.Canceled) || got != (history.Resolution{}) {
		t.Fatalf("canceled Append = %+v, %v", got, err)
	}
}

func TestReadRejectsCorruptBackendResults(t *testing.T) {
	id := history.NewHistoryID()
	root := byteCommit(t, dto(id, history.CommitRef{}, "root"))
	second := byteCommit(t, dto(id, root.Ref, "second"))
	foreign := byteCommit(t, dto(history.NewHistoryID(), history.CommitRef{}, "foreign"))
	wrongParent := byteCommit(t, dto(id, foreign.Ref, "wrong parent"))
	third := byteCommit(t, dto(id, second.Ref, "third"))
	for _, test := range []struct {
		name   string
		mutate func(*[]history.ByteCommit, *history.ReadRequest, *history.Policy)
	}{
		{"malformed", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			(*b)[1].Bytes = []byte{0xa0}
		}},
		{"truncated", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			(*b)[1].Bytes = (*b)[1].Bytes[:len((*b)[1].Bytes)-1]
		}},
		{"trailing", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			(*b)[1].Bytes = append((*b)[1].Bytes, 0)
		}},
		{"indexed-cid", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			(*b)[0].Ref.CID = second.Ref.CID
		}},
		{"indexed-cid-prefix", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			*b = (*b)[:1]
			(*b)[0].Ref.CID = second.Ref.CID
		}},
		{"indexed-sequence", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) { (*b)[0].Ref.Sequence++ }},
		{"indexed-sequence-prefix", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			*b = (*b)[:1]
			(*b)[0].Ref.Sequence++
		}},
		{"out-of-order", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			(*b)[0], (*b)[1] = (*b)[1], (*b)[0]
		}},
		{"foreign-root", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) { (*b)[0] = foreign }},
		{"parent-link", func(b *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			(*b)[1] = wrongParent
			r.Through = wrongParent.Ref
		}},
		{"endpoint-bytes", func(_ *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			r.Through = wrongParent.Ref
		}},
		{"genesis-starts-at-successor", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			*b = (*b)[1:]
		}},
		{"checkpoint-repeats-anchor", func(_ *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			r.After = root.Ref
		}},
		{"checkpoint-wrong-parent", func(b *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			r.After = root.Ref
			r.Through = third.Ref // Prefix does not reach the endpoint.
			*b = []history.ByteCommit{wrongParent}
		}},
		{"checkpoint-skips-sequence", func(b *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			r.After = root.Ref
			r.Through = third.Ref
			*b = []history.ByteCommit{third}
		}},
		{"checkpoint-self-consistent-foreign-chain", func(b *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			r.After = root.Ref
			r.Through = wrongParent.Ref
			*b = []history.ByteCommit{wrongParent}
		}},
		{"eof-returns-content", func(b *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) {
			r.After = second.Ref
			*b = []history.ByteCommit{third}
		}},
		{"empty-before-eof", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) { *b = nil }},
		{"past-endpoint", func(b *[]history.ByteCommit, _ *history.ReadRequest, _ *history.Policy) {
			*b = append(*b, third)
		}},
		{"count-cap", func(_ *[]history.ByteCommit, r *history.ReadRequest, _ *history.Policy) { r.MaxCommits = 1 }},
		{"byte-budget", func(_ *[]history.ByteCommit, _ *history.ReadRequest, p *history.Policy) {
			p.MaxPageBytes = len(root.Bytes)
		}},
		{"commit-byte-cap", func(_ *[]history.ByteCommit, _ *history.ReadRequest, p *history.Policy) {
			p.CommitLimits.MaxCommitBytes = len(root.Bytes) - 1
		}},
		{"field-cap", func(_ *[]history.ByteCommit, _ *history.ReadRequest, p *history.Policy) {
			p.CommitLimits.MaxPayloadBytes = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := corePolicy()
			r := history.ReadRequest{Through: second.Ref, MaxCommits: 3}
			b := []history.ByteCommit{
				{Ref: root.Ref, Bytes: append([]byte(nil), root.Bytes...)},
				{Ref: second.Ref, Bytes: append([]byte(nil), second.Bytes...)},
			}
			test.mutate(&b, &r, &p)
			h := newHistory(t, scriptedBackend{read: func(_ context.Context, gotID history.HistoryID, got history.ByteReadRequest) ([]history.ByteCommit, error) {
				if gotID != id || got.ReadRequest != r || got.MaxBytes != p.MaxPageBytes || got.MaxCommitBytes != p.CommitLimits.MaxCommitBytes {
					t.Fatal("core did not pass read bounds")
				}
				return b, nil
			}}, p)
			got, err := h.Read(context.Background(), id, r)
			if !errors.Is(err, history.ErrCorrupt) || !reflect.DeepEqual(got, history.Page{}) {
				t.Fatalf("Read = %+v, %v", got, err)
			}
		})
	}
}

func TestReadValidatesAvailableBoundariesWithoutGenesisScan(t *testing.T) {
	id := history.NewHistoryID()
	root := byteCommit(t, dto(id, history.CommitRef{}, "root"))
	second := byteCommit(t, dto(id, root.Ref, "second"))
	third := byteCommit(t, dto(id, second.Ref, "third"))
	calls := 0
	h := newHistory(t, scriptedBackend{read: func(_ context.Context, _ history.HistoryID, r history.ByteReadRequest) ([]history.ByteCommit, error) {
		calls++
		if r.After != root.Ref || r.Through != third.Ref {
			t.Fatal("unexpected read")
		}
		return []history.ByteCommit{second}, nil
	}}, corePolicy())
	page, err := h.Read(context.Background(), id, history.ReadRequest{After: root.Ref, Through: third.Ref, MaxCommits: 1})
	if err != nil || calls != 1 || page.Next != second.Ref || len(page.Commits) != 1 || page.Commits[0].Ref != second.Ref {
		t.Fatalf("Read = %+v, %v; calls %d", page, err, calls)
	}
}

func TestAppendCASAdjudicationNeverRetriesWrites(t *testing.T) {
	id := history.NewHistoryID()
	rootDTO := dto(id, history.CommitRef{}, "root")
	root := byteCommit(t, rootDTO)
	other := byteCommit(t, dto(id, history.CommitRef{}, "competing"))
	second := byteCommit(t, dto(id, root.Ref, "second"))
	third := byteCommit(t, dto(id, second.Ref, "third"))
	backendErr := errors.New("lost acknowledgement")
	for _, test := range []struct {
		name    string
		result  history.Observation
		err     error
		status  history.Status
		wantErr error
	}{
		{"accepted-or-duplicate", history.Observation{Head: root.Ref, Commit: root.Ref}, nil, history.StatusAccepted, nil},
		{"different-winner", history.Observation{Head: other.Ref, Commit: other.Ref}, nil, history.StatusConflict, nil},
		{"duplicate-with-new-head", history.Observation{Head: second.Ref, Commit: root.Ref}, nil, history.StatusAccepted, nil},
		{"advanced-past-window", history.Observation{Head: third.Ref, Commit: root.Ref}, nil, history.StatusRetryExpired, nil},
		{"error-with-acceptance-shaped-output", history.Observation{Head: root.Ref, Commit: root.Ref}, backendErr, 0, backendErr},
		{"unchanged-head", history.Observation{}, nil, 0, history.ErrCorrupt},
		{"missing-ordinal", history.Observation{Head: root.Ref}, nil, 0, history.ErrCorrupt},
		{"incoherent-ordinal", history.Observation{Head: root.Ref, Commit: other.Ref}, nil, 0, history.ErrCorrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads, writes := 0, 0
			h := newHistory(t, scriptedBackend{
				observe: func(context.Context, history.HistoryID, int64) (history.Observation, error) {
					reads++
					t.Fatal("Append must not observe before CAS")
					return history.Observation{}, nil
				},
				append: func(_ context.Context, gotID history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
					writes++
					if writes != 1 || gotID != id || expected != (history.CommitRef{}) || c.Ref != root.Ref || !reflect.DeepEqual(c.Bytes, root.Bytes) {
						t.Fatal("unexpected CAS write")
					}
					cancel() // Receipt evidence must survive late caller cancellation.
					return test.result, test.err
				},
			}, corePolicy())
			got, err := h.Append(ctx, id, rootDTO)
			if writes != 1 || reads != 0 {
				t.Fatalf("calls = %d reads, %d writes", reads, writes)
			}
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || got != (history.Resolution{}) {
					t.Fatalf("Append = %+v, %v", got, err)
				}
			} else if err != nil || got != (history.Resolution{Proposal: root.Ref, Head: test.result.Head, Status: test.status}) {
				t.Fatalf("Append = %+v, %v", got, err)
			}
		})
	}
}

func TestAppendSuccessorCASAllowsSpeculativeExpectedHeads(t *testing.T) {
	id := history.NewHistoryID()
	root := byteCommit(t, dto(id, history.CommitRef{}, "root")).Ref
	other := byteCommit(t, dto(id, history.CommitRef{}, "other root")).Ref
	second := byteCommit(t, dto(id, root, "second")).Ref
	for _, test := range []struct {
		name    string
		parent  history.CommitRef
		head    history.CommitRef
		wantErr error
	}{
		{"absent", root, history.CommitRef{}, nil},
		{"lower", second, root, nil},
		{"same-ordinal-wrong-cid", root, other, nil},
		{"exact-successor-contradiction", root, root, history.ErrCorrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := dto(id, test.parent, "proposal")
			proposal := byteCommit(t, c).Ref
			writes := 0
			h := newHistory(t, scriptedBackend{
				append: func(_ context.Context, gotID history.HistoryID, expected history.CommitRef, got history.ByteCommit) (history.Observation, error) {
					writes++
					if writes != 1 || gotID != id || expected != test.parent || got.Ref != proposal {
						t.Fatal("wrong CAS proposal or expected parent")
					}
					return history.Observation{Head: test.head}, nil
				},
			}, corePolicy())
			got, err := h.Append(context.Background(), id, c)
			if writes != 1 {
				t.Fatalf("Append calls = %d", writes)
			}
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || got != (history.Resolution{}) {
					t.Fatalf("Append = %+v, %v", got, err)
				}
			} else if err != nil || got != (history.Resolution{Proposal: proposal, Head: test.head, Status: history.StatusConflict}) {
				t.Fatalf("Append = %+v, %v", got, err)
			}
		})
	}
}

func TestDuplicateAcceptanceSurvivesLateCancellation(t *testing.T) {
	id := history.NewHistoryID()
	c := dto(id, history.CommitRef{}, "root")
	ref := byteCommit(t, c).Ref
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHistory(t, scriptedBackend{append: func(context.Context, history.HistoryID, history.CommitRef, history.ByteCommit) (history.Observation, error) {
		cancel()
		return history.Observation{Head: ref, Commit: ref}, nil
	}}, corePolicy())
	got, err := h.Append(ctx, id, c)
	if err != nil || got != (history.Resolution{Proposal: ref, Head: ref, Status: history.StatusAccepted}) {
		t.Fatalf("duplicate = %+v, %v", got, err)
	}
}

func TestObservationCorruptionAndErrors(t *testing.T) {
	id := history.NewHistoryID()
	c := dto(id, history.CommitRef{}, "root")
	ref := byteCommit(t, c).Ref
	other := byteCommit(t, dto(id, history.CommitRef{}, "other")).Ref
	for _, o := range []history.Observation{
		{Head: history.CommitRef{Sequence: 1}},
		{Head: history.CommitRef{CID: ref.CID}},
		{Commit: ref},
		{Head: ref},
		{Head: ref, Commit: other},
		{Head: ref, Commit: history.CommitRef{Sequence: 2, CID: ref.CID}},
	} {
		h := newHistory(t, scriptedBackend{
			observe: func(context.Context, history.HistoryID, int64) (history.Observation, error) { return o, nil },
			append: func(context.Context, history.HistoryID, history.CommitRef, history.ByteCommit) (history.Observation, error) {
				return o, nil
			},
		}, corePolicy())
		got, err := h.Resolve(context.Background(), id, ref)
		if !errors.Is(err, history.ErrCorrupt) || got != (history.Resolution{}) {
			t.Fatalf("Resolve(%+v) = %+v, %v", o, got, err)
		}
		got, err = h.Append(context.Background(), id, c)
		if !errors.Is(err, history.ErrCorrupt) || got != (history.Resolution{}) {
			t.Fatalf("Append(%+v) = %+v, %v", o, got, err)
		}
	}
	errBackend := errors.New("backend failure")
	h := newHistory(t, scriptedBackend{
		observe: func(context.Context, history.HistoryID, int64) (history.Observation, error) {
			return history.Observation{Head: ref, Commit: ref}, errBackend
		},
		append: func(context.Context, history.HistoryID, history.CommitRef, history.ByteCommit) (history.Observation, error) {
			return history.Observation{Head: ref, Commit: ref}, errBackend
		},
		read: func(context.Context, history.HistoryID, history.ByteReadRequest) ([]history.ByteCommit, error) {
			return []history.ByteCommit{{Ref: ref, Bytes: []byte{1}}}, errBackend
		},
	}, corePolicy())
	if got, err := h.Head(context.Background(), id); got != (history.CommitRef{}) || !errors.Is(err, errBackend) {
		t.Fatalf("Head = %+v, %v", got, err)
	}
	if got, err := h.Resolve(context.Background(), id, ref); got != (history.Resolution{}) || !errors.Is(err, errBackend) {
		t.Fatalf("Resolve = %+v, %v", got, err)
	}
	if got, err := h.Append(context.Background(), id, c); got != (history.Resolution{}) || !errors.Is(err, errBackend) {
		t.Fatalf("Append = %+v, %v", got, err)
	}
	if got, err := h.Read(context.Background(), id, history.ReadRequest{Through: ref, MaxCommits: 1}); !reflect.DeepEqual(got, history.Page{}) || !errors.Is(err, errBackend) {
		t.Fatalf("Read = %+v, %v", got, err)
	}
}

func TestMaxSequenceDoesNotWrap(t *testing.T) {
	id := history.NewHistoryID()
	root := byteCommit(t, dto(id, history.CommitRef{}, "root")).Ref
	for _, sequence := range []int64{2, math.MaxInt64} {
		c := dto(id, root, "proposal")
		c.Sequence = sequence
		proposal := byteCommit(t, c).Ref
		p := corePolicy()
		p.RetryWindow = math.MaxInt64
		writes := 0
		h := newHistory(t, scriptedBackend{append: func(_ context.Context, _ history.HistoryID, expected history.CommitRef, got history.ByteCommit) (history.Observation, error) {
			writes++
			if expected != (history.CommitRef{Sequence: sequence - 1, CID: c.Parent}) || got.Ref != proposal {
				t.Fatal("wrong expected parent or proposal")
			}
			return history.Observation{Head: history.CommitRef{Sequence: math.MaxInt64, CID: proposal.CID}, Commit: proposal}, nil
		}}, p)
		got, err := h.Append(context.Background(), id, c)
		if err != nil || writes != 1 || got.Status != history.StatusAccepted || got.Head.Sequence != math.MaxInt64 {
			t.Fatalf("Append = %+v, %v; writes %d", got, err, writes)
		}
	}
}

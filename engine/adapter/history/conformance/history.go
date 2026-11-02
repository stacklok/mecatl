package conformance

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

const opaqueKind = "opaque"

func policy() history.Policy {
	return history.Policy{
		CommitLimits: format.Limits{MaxCommitBytes: 4096, MaxRecords: 4, MaxKindBytes: 32, MaxPayloadBytes: 2048},
		RetryWindow:  2, MaxPageBytes: 16384, MaxPageCommits: 16,
	}
}

// Factory creates a fresh isolated backend for each child test. Run constructs
// the real History with the supplied test policy. Register cleanup with t.Cleanup.
type Factory func(*testing.T) history.Backend

// historyFactory is the internal DTO-facing fixture constructor.
type historyFactory func(*testing.T, history.Policy) *history.History

func withPolicy(factory Factory) historyFactory {
	return func(t *testing.T, p history.Policy) *history.History {
		t.Helper()
		h, err := history.New(factory(t), p)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
}

// Run exercises the real History/backend contract and transport uncertainty.
func Run(t *testing.T, backend Factory) {
	factory := withPolicy(backend)
	t.Helper()
	t.Run("BackendContract", func(t *testing.T) { backendContract(t, backend) })
	t.Run("BackendAppendSequences", func(t *testing.T) { backendAppendSequences(t, backend) })
	t.Run("ReadBoundaries", func(t *testing.T) { readBoundaries(t, backend) })
	t.Run("LostAcknowledgement", func(t *testing.T) { lostAcknowledgement(t, backend) })
	t.Run("IndependentInstancePolicies", func(t *testing.T) { independentInstancePolicies(t, backend) })
	t.Run("MisalignedReadPolicy", func(t *testing.T) { misalignedReadPolicy(t, backend) })
	for _, test := range []struct {
		name string
		run  func(*testing.T, historyFactory)
	}{
		{"IdentifyAndMutation", identifyAndMutation},
		{"AppendResolveAndRetryWindow", appendResolveAndRetryWindow},
		{"ReadBoundsAndOwnership", readBoundsAndOwnership},
		{"InvalidInputsAndMissingHistory", invalidInputsAndMissingHistory},
		{"CanceledOperations", canceledOperations},
		{"ConcurrentReadAndResolutionDuringAppend", concurrentReadAndResolutionDuringAppend},
		{"ConcurrentRootAndDuplicateAppends", concurrentRootAndDuplicateAppends},
		{"ConcurrentSuccessorAndDuplicateAppends", concurrentSuccessorAndDuplicateAppends},
		{"ScopeIsolation", scopeIsolation},
		{"AppendLimits", appendLimits},
		{"AdvancingRetryWindows", advancingRetryWindows},
		{"StablePagination", stablePagination},
		{"PageHintDoesNotRaisePolicy", pageHintDoesNotRaisePolicy},
		{"DelayedRequest", delayedRequest},
	} {
		t.Run(test.name, func(t *testing.T) { test.run(t, factory) })
	}
}

// RunAllocationChecks measures operation allocations in a quiescent process.
// It supplements Run for in-process adapters; unrelated background allocations
// would contaminate runtime.MemStats.TotalAlloc and must be excluded from this check.
func RunAllocationChecks(t *testing.T, backend Factory) {
	factory := withPolicy(backend)
	t.Helper()
	t.Run("BoundedByteRead", func(t *testing.T) { boundedByteReadAllocation(t, backend) })
	t.Run("RejectedCommits", func(t *testing.T) { appendLargeLimitRejections(t, factory) })
	t.Run("TinyRead", func(t *testing.T) { hugePolicyTinyResult(t, factory) })
}

func encoded(t *testing.T, id history.HistoryID, parent history.CommitRef, payload string) (format.Commit, history.CommitRef) {
	t.Helper()
	c := format.Commit{FormatVersion: 1, Sequence: parent.Sequence + 1,
		Records: []format.Record{{Kind: opaqueKind, KindVersion: 7, Payload: []byte(payload)}},
	}
	if parent.Sequence == 0 {
		c.HistoryID = id
	} else {
		c.Parent = parent.CID
	}
	_, cid, err := format.EncodeCommit(c, policy().CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	return c, history.CommitRef{Sequence: c.Sequence, CID: cid}
}

func wantResolution(t *testing.T, got history.Resolution, err error, status history.Status, proposal, head history.CommitRef) {
	t.Helper()
	want := history.Resolution{Status: status, Proposal: proposal, Head: head}
	if err != nil || got != want {
		t.Fatalf("resolution = %+v, %v; want %+v", got, err, want)
	}
}

func appendAccepted(t *testing.T, s *history.History, id history.HistoryID, b format.Commit, ref history.CommitRef) {
	t.Helper()
	got, err := s.Append(context.Background(), id, b)
	wantResolution(t, got, err, history.StatusAccepted, ref, ref)
}

func identifyAndMutation(t *testing.T, factory historyFactory) {
	s := factory(t, policy())
	id := history.NewHistoryID()
	root, want := encoded(t, id, history.CommitRef{}, "\x00\xffopaque")
	original, err := s.Identify(id, root)
	if err != nil || original != want {
		t.Fatalf("Identify = %+v, %v", original, err)
	}
	if head, err := s.Head(context.Background(), id); head != (history.CommitRef{}) || !errors.Is(err, history.ErrNotFound) {
		t.Fatalf("Identify created history: %+v, %v", head, err)
	}
	root.Records[0].Payload[0] ^= 0xff
	mutated, err := s.Identify(id, root)
	if err != nil || mutated == original {
		t.Fatalf("mutated Identify = %+v, %v", mutated, err)
	}
	appendAccepted(t, s, id, root, mutated)
	got, err := s.Resolve(context.Background(), id, original)
	wantResolution(t, got, err, history.StatusConflict, original, mutated)
	child, want := encoded(t, id, mutated, "successor")
	ref, err := s.Identify(id, child)
	if err != nil || ref != want {
		t.Fatalf("successor Identify = %+v, %v", ref, err)
	}
	appendAccepted(t, s, id, child, ref)
}

func appendResolveAndRetryWindow(t *testing.T, factory historyFactory) {
	ctx := context.Background()
	s := factory(t, policy())
	id := history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "root")
	second, r2 := encoded(t, id, r1, "second")
	third, r3 := encoded(t, id, r2, "third")
	otherRoot, otherR1 := encoded(t, id, history.CommitRef{}, "competing root")
	otherSecond, otherR2 := encoded(t, id, r1, "competing second")

	got, err := s.Resolve(ctx, id, r1)
	wantResolution(t, got, err, history.StatusNotObserved, r1, history.CommitRef{})
	got, err = s.Append(ctx, id, second)
	wantResolution(t, got, err, history.StatusConflict, r2, history.CommitRef{})
	if head, err := s.Head(ctx, id); !errors.Is(err, history.ErrNotFound) || head != (history.CommitRef{}) {
		t.Fatalf("unknown successor reserved history: %+v, %v", head, err)
	}
	appendAccepted(t, s, id, root, r1)
	got, err = s.Append(ctx, id, root)
	wantResolution(t, got, err, history.StatusAccepted, r1, r1)
	got, err = s.Append(ctx, id, otherRoot)
	wantResolution(t, got, err, history.StatusConflict, otherR1, r1)
	got, err = s.Append(ctx, id, third)
	wantResolution(t, got, err, history.StatusConflict, r3, r1)
	got, err = s.Resolve(ctx, id, r3)
	wantResolution(t, got, err, history.StatusNotObserved, r3, r1)
	appendAccepted(t, s, id, second, r2)
	got, err = s.Append(ctx, id, root)
	wantResolution(t, got, err, history.StatusAccepted, r1, r2)
	got, err = s.Resolve(ctx, id, r1)
	wantResolution(t, got, err, history.StatusAccepted, r1, r2)
	appendAccepted(t, s, id, third, r3)
	for _, b := range []format.Commit{root, otherRoot} {
		_, proposal, decodeErr := format.EncodeCommit(b, policy().CommitLimits)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		ref := history.CommitRef{Sequence: 1, CID: proposal}
		got, err = s.Append(ctx, id, b)
		wantResolution(t, got, err, history.StatusRetryExpired, ref, r3)
		got, err = s.Resolve(ctx, id, ref)
		wantResolution(t, got, err, history.StatusRetryExpired, ref, r3)
	}
	got, err = s.Append(ctx, id, second)
	wantResolution(t, got, err, history.StatusAccepted, r2, r3)
	got, err = s.Append(ctx, id, otherSecond)
	wantResolution(t, got, err, history.StatusConflict, otherR2, r3)
	got, err = s.Resolve(ctx, id, otherR2)
	wantResolution(t, got, err, history.StatusConflict, otherR2, r3)
	_, wrongRef := encoded(t, id, otherR2, "wrong parent")
	fourth, r4 := encoded(t, id, wrongRef, "still wrong parent")
	got, err = s.Append(ctx, id, fourth)
	wantResolution(t, got, err, history.StatusConflict, r4, r3)
	// Independently vary only ancestry: this candidate is exactly head+1.
	wrongParent, wrongParentRef := encoded(t, id, history.CommitRef{Sequence: r3.Sequence, CID: r1.CID}, "wrong ancestry")
	got, err = s.Append(ctx, id, wrongParent)
	wantResolution(t, got, err, history.StatusConflict, wrongParentRef, r3)
	page, err := s.Read(ctx, id, history.ReadRequest{Through: r3, MaxCommits: 3})
	if err != nil || page.Next != r3 || !sameCommits(page.Commits, []format.Commit{root, second, third}) {
		t.Fatalf("retry expiry deleted history: %+v, %v", page, err)
	}
	if head, err := s.Head(ctx, id); err != nil || head != r3 {
		t.Fatalf("head = %+v, %v", head, err)
	}
}

func advancingRetryWindows(t *testing.T, factory historyFactory) {
	for _, window := range []struct {
		name  string
		width int64
	}{{"one", 1}, {"four", 4}} {
		t.Run("window-"+window.name, func(t *testing.T) {
			p := policy()
			p.RetryWindow = window.width
			s := factory(t, p)
			ctx := context.Background()
			id := history.NewHistoryID()
			commits := make([]format.Commit, 0, int(window.width+1))
			refs := make([]history.CommitRef, 0, int(window.width+1))
			parent := history.CommitRef{}
			for range int(window.width + 1) {
				b, ref := encoded(t, id, parent, "advance")
				commits = append(commits, b)
				refs = append(refs, ref)
				appendAccepted(t, s, id, b, ref)
				parent = ref
			}
			head := refs[len(refs)-1]
			lastRetained := refs[len(refs)-int(window.width)]
			firstExpired := refs[len(refs)-int(window.width)-1]
			retainedConflict, retainedConflictRef := conflictingCommit(t, id, refs, lastRetained.Sequence)
			expiredConflict, expiredConflictRef := conflictingCommit(t, id, refs, firstExpired.Sequence)
			for _, test := range []struct {
				name   string
				input  format.Commit
				ref    history.CommitRef
				status history.Status
			}{
				{"retained-matching", commits[lastRetained.Sequence-1], lastRetained, history.StatusAccepted},
				{"retained-conflicting", retainedConflict, retainedConflictRef, history.StatusConflict},
				{"expired-matching", commits[firstExpired.Sequence-1], firstExpired, history.StatusRetryExpired},
				{"expired-conflicting", expiredConflict, expiredConflictRef, history.StatusRetryExpired},
			} {
				t.Run(test.name, func(t *testing.T) {
					got, err := s.Append(ctx, id, test.input)
					wantResolution(t, got, err, test.status, test.ref, head)
					got, err = s.Resolve(ctx, id, test.ref)
					wantResolution(t, got, err, test.status, test.ref, head)
				})
			}
		})
	}
}

func conflictingCommit(t *testing.T, id history.HistoryID, refs []history.CommitRef, sequence int64) (format.Commit, history.CommitRef) {
	t.Helper()
	parent := history.CommitRef{}
	if sequence > 1 {
		parent = refs[sequence-2]
	}
	return encoded(t, id, parent, "conflicting")
}

func readBoundsAndOwnership(t *testing.T, factory historyFactory) {
	ctx := context.Background()
	s := factory(t, policy())
	id := history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "\x00opaque\xff")
	original := root.Clone()
	appendAccepted(t, s, id, root, r1)
	root.Records[0].Payload[0] ^= 0xff
	second, r2 := encoded(t, id, r1, "second")
	third, r3 := encoded(t, id, r2, "third")
	appendAccepted(t, s, id, second, r2)
	appendAccepted(t, s, id, third, r3)
	req := history.ReadRequest{Through: r2, MaxCommits: policy().MaxPageCommits}
	page, err := s.Read(ctx, id, req)
	wantPage(t, page, err, r2, original, second)
	page.Commits[0].Commit.Records[0].Payload[0] ^= 0xff
	page.Commits[1].Commit.Records[0].Payload[0] ^= 0xff
	page.Commits[0] = history.Commit{}
	page, err = s.Read(ctx, id, req)
	wantPage(t, page, err, r2, original, second)
	for _, budget := range []int{encodedLen(t, original) - 1, encodedLen(t, original), encodedLen(t, original) + encodedLen(t, second) - 1, encodedLen(t, original) + encodedLen(t, second)} {
		p := policy()
		p.MaxPageBytes = budget
		limited := factory(t, p)
		appendAccepted(t, limited, id, original, r1)
		appendAccepted(t, limited, id, second, r2)
		appendAccepted(t, limited, id, third, r3)
		page, err = limited.Read(ctx, id, req)
		if budget < encodedLen(t, original) {
			var tooSmall history.PageTooSmallError
			if !errors.Is(err, history.ErrPageTooSmall) || !errors.As(err, &tooSmall) || tooSmall.RequiredBytes != encodedLen(t, original) || !reflect.DeepEqual(page, history.Page{}) {
				t.Fatalf("too-small read = %+v, %v", page, err)
			}
		} else if budget < encodedLen(t, original)+encodedLen(t, second) {
			wantPage(t, page, err, r1, original)
		} else {
			wantPage(t, page, err, r2, original, second)
		}
	}
	req.MaxCommits = 1
	page, err = s.Read(ctx, id, req)
	if err != nil || len(page.Commits) != 1 || page.Next != r1 {
		t.Fatalf("count-limited read = %+v, %v", page, err)
	}
	req.After = r1
	page, err = s.Read(ctx, id, req)
	if err != nil || !sameCommits(page.Commits, []format.Commit{second}) || page.Next != r2 {
		t.Fatalf("after read = %+v, %v", page, err)
	}
	req.After = r2
	page, err = s.Read(ctx, id, req)
	if err != nil || len(page.Commits) != 0 || page.Next != r2 {
		t.Fatalf("empty endpoint read = %+v, %v", page, err)
	}
}

func hugePolicyTinyResult(t *testing.T, factory historyFactory) {
	p := policy()
	p.MaxPageCommits = math.MaxInt
	p.MaxPageBytes = math.MaxInt
	p.RetryWindow = math.MaxInt64
	s := factory(t, p)
	id := history.NewHistoryID()
	b, ref := encoded(t, id, history.CommitRef{}, "tiny")
	appendAccepted(t, s, id, b, ref)
	request := history.ReadRequest{Through: ref, MaxCommits: math.MaxInt}
	var (
		page history.Page
		err  error
	)
	allocated := totalAlloc(t, func() {
		page, err = s.Read(context.Background(), id, request)
	})
	// A tiny result may reserve modest constant slack, but must not size storage
	// from either huge request limit.
	if err != nil || len(page.Commits) != 1 || !page.Commits[0].Commit.Equal(b) || allocated > 64<<10 {
		t.Fatalf("huge policy/tiny result = %+v, %v; allocated %d bytes", page, err, allocated)
	}
}

func invalidInputsAndMissingHistory(t *testing.T, factory historyFactory) {
	ctx := context.Background()
	s := factory(t, policy())
	id := history.NewHistoryID()
	b, ref := encoded(t, id, history.CommitRef{}, "private-payload")
	req := history.ReadRequest{Through: ref, MaxCommits: policy().MaxPageCommits}
	if head, err := s.Head(ctx, id); !errors.Is(err, history.ErrNotFound) || head != (history.CommitRef{}) {
		t.Fatalf("unknown head = %+v, %v", head, err)
	}
	if page, err := s.Read(ctx, id, req); !errors.Is(err, history.ErrNotFound) || !reflect.DeepEqual(page, history.Page{}) {
		t.Fatalf("unknown read = %+v, %v", page, err)
	}
	if head, err := s.Head(ctx, history.HistoryID{}); !errors.Is(err, history.ErrInvalid) || head != (history.CommitRef{}) {
		t.Fatalf("zero ID head = %+v, %v", head, err)
	}
	for _, input := range []struct {
		id history.HistoryID
		b  format.Commit
	}{
		{history.HistoryID{}, b}, {history.NewHistoryID(), b}, {id, format.Commit{}},
		{id, format.Commit{FormatVersion: 2, HistoryID: id, Sequence: 1, Records: b.Records}},
	} {
		got, err := s.Append(ctx, input.id, input.b)
		if !errors.Is(err, history.ErrInvalid) || got != (history.Resolution{}) || bytes.Contains([]byte(err.Error()), []byte("private-payload")) {
			t.Fatalf("invalid append = %+v, %v", got, err)
		}
	}
	for _, proposal := range []history.CommitRef{{}, {CID: ref.CID}, {Sequence: -1, CID: ref.CID}, {Sequence: 1}} {
		got, err := s.Resolve(ctx, id, proposal)
		if !errors.Is(err, history.ErrInvalid) || got != (history.Resolution{}) {
			t.Fatalf("invalid resolve = %+v, %v", got, err)
		}
	}
	got, err := s.Resolve(ctx, history.HistoryID{}, ref)
	if !errors.Is(err, history.ErrInvalid) || got != (history.Resolution{}) {
		t.Fatalf("zero ID resolve = %+v, %v", got, err)
	}
	appendAccepted(t, s, id, b, ref)
	_, foreign := encoded(t, history.NewHistoryID(), history.CommitRef{}, "foreign")
	for _, mutate := range []func(*history.ReadRequest){
		func(r *history.ReadRequest) { r.After = history.CommitRef{Sequence: -1, CID: ref.CID} },
		func(r *history.ReadRequest) { r.After = history.CommitRef{Sequence: 2, CID: ref.CID} },
		func(r *history.ReadRequest) { r.After = history.CommitRef{Sequence: 1} },
		func(r *history.ReadRequest) { r.After = history.CommitRef{CID: ref.CID} },
		func(r *history.ReadRequest) { r.After = foreign },
		func(r *history.ReadRequest) { r.Through = history.CommitRef{} },
		func(r *history.ReadRequest) { r.Through.Sequence = 0 },
		func(r *history.ReadRequest) { r.Through.Sequence = -1 },
		func(r *history.ReadRequest) { r.Through.CID = format.CID{} },
		func(r *history.ReadRequest) { r.Through = foreign },
		func(r *history.ReadRequest) { r.Through.Sequence = 2 },
		func(r *history.ReadRequest) { r.MaxCommits = 0 },
		func(r *history.ReadRequest) { r.MaxCommits = -1 },
		func(r *history.ReadRequest) { r.MaxCommits++ },
	} {
		r := req
		mutate(&r)
		page, err := s.Read(ctx, id, r)
		wantPageError(t, page, err, history.ErrInvalid)
	}
	page, err := s.Read(ctx, history.HistoryID{}, req)
	if !errors.Is(err, history.ErrInvalid) || !reflect.DeepEqual(page, history.Page{}) {
		t.Fatalf("zero ID read = %+v, %v", page, err)
	}
}

func canceledOperations(t *testing.T, factory historyFactory) {
	s := factory(t, policy())
	id := history.NewHistoryID()
	b, ref := encoded(t, id, history.CommitRef{}, "root")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := s.Append(ctx, id, b)
	if !errors.Is(err, context.Canceled) || got != (history.Resolution{}) {
		t.Fatalf("canceled append = %+v, %v", got, err)
	}
	if head, err := s.Head(context.Background(), id); !errors.Is(err, history.ErrNotFound) || head != (history.CommitRef{}) {
		t.Fatalf("canceled append mutated store: %+v, %v", head, err)
	}
	appendAccepted(t, s, id, b, ref)
	child, _ := encoded(t, id, ref, "canceled successor")
	got, err = s.Append(ctx, id, child)
	if !errors.Is(err, context.Canceled) || got != (history.Resolution{}) {
		t.Fatalf("canceled successor = %+v, %v", got, err)
	}
	if head, err := s.Head(context.Background(), id); err != nil || head != ref {
		t.Fatalf("canceled successor mutated store: %+v, %v", head, err)
	}
	if head, err := s.Head(ctx, id); !errors.Is(err, context.Canceled) || head != (history.CommitRef{}) {
		t.Fatalf("canceled head = %+v, %v", head, err)
	}
	got, err = s.Resolve(ctx, id, ref)
	if !errors.Is(err, context.Canceled) || got != (history.Resolution{}) {
		t.Fatalf("canceled resolve = %+v, %v", got, err)
	}
	page, err := s.Read(ctx, id, history.ReadRequest{Through: ref, MaxCommits: 1})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(page, history.Page{}) {
		t.Fatalf("canceled read = %+v, %v", page, err)
	}
}

func concurrentReadAndResolutionDuringAppend(t *testing.T, factory historyFactory) {
	ctx := context.Background()
	s := factory(t, policy())
	id := history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "root")
	appendAccepted(t, s, id, root, r1)
	var commits []format.Commit
	refs := []history.CommitRef{r1}
	parent := r1
	for range 32 {
		b, ref := encoded(t, id, parent, "successor")
		commits = append(commits, b)
		refs = append(refs, ref)
		parent = ref
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i, b := range commits {
			got, err := s.Append(ctx, id, b)
			want := history.Resolution{Status: history.StatusAccepted, Proposal: refs[i+1], Head: refs[i+1]}
			if err != nil || got != want {
				t.Errorf("concurrent successor append = %+v, %v; want %+v", got, err, want)
				return
			}
		}
	})
	for range 64 {
		page, err := s.Read(ctx, id, history.ReadRequest{Through: r1, MaxCommits: 16})
		if err != nil || page.Next != r1 || !sameCommits(page.Commits, []format.Commit{root}) {
			t.Errorf("concurrent fixed-endpoint read = %+v, %v", page, err)
		}
		for _, operation := range []string{"Resolve", "Append"} {
			var got history.Resolution
			var err error
			if operation == "Append" {
				got, err = s.Append(ctx, id, root)
			} else {
				got, err = s.Resolve(ctx, id, r1)
			}
			wantStatus := history.StatusAccepted
			if got.Head.Sequence > policy().RetryWindow {
				wantStatus = history.StatusRetryExpired
			}
			if err != nil || got.Status != wantStatus || got.Proposal != r1 || got.Head.Sequence < 1 || got.Head.Sequence > parent.Sequence {
				t.Errorf("inconsistent concurrent %s observation = %+v, %v", operation, got, err)
			} else if got.Head != refs[got.Head.Sequence-1] {
				t.Errorf("wrong observed head = %+v", got.Head)
			}
		}
	}
	wg.Wait()
}

func concurrentRootAndDuplicateAppends(t *testing.T, factory historyFactory) {
	s := factory(t, policy())
	id := history.NewHistoryID()
	b1, r1 := encoded(t, id, history.CommitRef{}, "root one")
	b2, r2 := encoded(t, id, history.CommitRef{}, "root two")
	start := make(chan struct{})
	heads := make(chan history.CommitRef, 32)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			<-start
			b, ref := b1, r1
			if i%2 == 1 {
				b, ref = b2, r2
			}
			got, err := s.Append(context.Background(), id, b)
			heads <- got.Head
			if err != nil || got.Proposal != ref || got.Head.Sequence != 1 || (got.Head != r1 && got.Head != r2) {
				t.Errorf("concurrent append = %+v, %v", got, err)
				return
			}
			want := history.StatusConflict
			if got.Head == ref {
				want = history.StatusAccepted
			}
			if got.Status != want {
				t.Errorf("concurrent status = %v; want %v", got.Status, want)
			}
		})
	}
	close(start)
	wg.Wait()
	close(heads)
	head, err := s.Head(context.Background(), id)
	if err != nil || head.Sequence != 1 {
		t.Fatalf("concurrent head = %+v, %v", head, err)
	}
	for observed := range heads {
		if observed != head {
			t.Errorf("multiple root winners: %+v and %+v", observed, head)
		}
	}
}

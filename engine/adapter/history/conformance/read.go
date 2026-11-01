package conformance

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// sameCommits compares complete DTOs, including opaque payloads and envelopes.
func sameCommits(got []history.Commit, want []format.Commit) bool {
	return slices.EqualFunc(got, want, func(g history.Commit, w format.Commit) bool {
		return g.Commit.Equal(w)
	})
}

func encodedLen(t *testing.T, c format.Commit) int {
	t.Helper()
	b, _, err := format.EncodeCommit(c, policy().CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// wantPage checks complete DTOs, verified references, and the exclusive cursor.
func wantPage(t *testing.T, page history.Page, err error, next history.CommitRef, commits ...format.Commit) {
	t.Helper()
	if err != nil || page.Next != next || !sameCommits(page.Commits, commits) {
		t.Fatalf("page = %+v, %v; want next %+v, commits %+v", page, err, next, commits)
	}
	for i, c := range commits {
		_, cid, err := format.EncodeCommit(c, policy().CommitLimits)
		if err != nil || page.Commits[i].Ref != (history.CommitRef{Sequence: c.Sequence, CID: cid}) {
			t.Fatalf("page reference %d = %+v, %v", i, page.Commits[i].Ref, err)
		}
	}
}

func wantPageError(t *testing.T, page history.Page, err, target error) {
	t.Helper()
	if !errors.Is(err, target) || !reflect.DeepEqual(page, history.Page{}) {
		t.Fatalf("page = %+v, %v; want zero page and %v", page, err, target)
	}
}

// readBoundaries checks both seams: core chain checks must not hide a backend
// that accepts an unindexed lower anchor, including at EOF.
func readBoundaries(t *testing.T, factory Factory) {
	ctx := context.Background()
	backend := factory(t)
	s, err := history.New(backend, policy())
	if err != nil {
		t.Fatal(err)
	}
	id, foreignID := history.NewHistoryID(), history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "root")
	second, r2 := encoded(t, id, r1, "second")
	third, r3 := encoded(t, id, r2, "third")
	_, future := encoded(t, id, r3, "future")
	foreignRoot, foreign := encoded(t, foreignID, history.CommitRef{}, "foreign")
	appendAccepted(t, s, id, root, r1)
	appendAccepted(t, s, id, second, r2)
	appendAccepted(t, s, id, third, r3)
	appendAccepted(t, s, foreignID, foreignRoot, foreign)
	for _, test := range []struct {
		name           string
		after, through history.CommitRef
	}{
		{"lower-wrong-cid", history.CommitRef{Sequence: 1, CID: r2.CID}, r3},
		{"lower-foreign", foreign, r3},
		{"lower-future", future, r3},
		{"lower-sequence-only", history.CommitRef{Sequence: 1}, r3},
		{"lower-cid-only", history.CommitRef{CID: r1.CID}, r3},
		{"lower-negative", history.CommitRef{Sequence: -1, CID: r1.CID}, r3},
		{"same-ordinal-eof-mismatch", history.CommitRef{Sequence: 3, CID: r2.CID}, r3},
		{"unaccepted-eof", history.CommitRef{Sequence: 3, CID: r2.CID}, history.CommitRef{Sequence: 3, CID: r2.CID}},
		{"foreign-eof", foreign, foreign},
		{"upper-wrong-cid", r1, history.CommitRef{Sequence: 3, CID: r2.CID}},
		{"upper-foreign", history.CommitRef{}, foreign},
		{"upper-future", r1, future},
		{"upper-sequence-only", r1, history.CommitRef{Sequence: 3}},
		{"upper-cid-only", history.CommitRef{}, history.CommitRef{CID: r3.CID}},
		{"upper-zero", history.CommitRef{}, history.CommitRef{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A prefix-only read must still verify the indexed upper boundary.
			r := history.ReadRequest{After: test.after, Through: test.through, MaxCommits: 1}
			t.Run("Backend", func(t *testing.T) {
				page, err := backend.Read(ctx, id, history.ByteReadRequest{ReadRequest: r, MaxBytes: policy().MaxPageBytes, MaxCommitBytes: policy().CommitLimits.MaxCommitBytes})
				if !errors.Is(err, history.ErrInvalid) || page != nil {
					t.Fatalf("invalid boundary byte page = %+v, %v", page, err)
				}
			})
			t.Run("History", func(t *testing.T) {
				page, err := s.Read(ctx, id, r)
				wantPageError(t, page, err, history.ErrInvalid)
			})
		})
	}
}

func stablePagination(t *testing.T, factory historyFactory) {
	s := factory(t, policy())
	ctx := context.Background()
	id := history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "\x00opaque\xff")
	second, r2 := encoded(t, id, r1, "second")
	third, r3 := encoded(t, id, r2, "third")
	appendAccepted(t, s, id, root, r1)
	appendAccepted(t, s, id, second, r2)
	selected, err := s.Head(ctx, id)
	if err != nil || selected != r2 {
		t.Fatalf("selected head = %+v, %v", selected, err)
	}
	req := history.ReadRequest{Through: selected, MaxCommits: 1}
	page, err := s.Read(ctx, id, req)
	wantPage(t, page, err, r1, root)
	appendAccepted(t, s, id, third, r3)
	req.After = page.Next
	page, err = s.Read(ctx, id, req)
	wantPage(t, page, err, r2, second)
	req.After = page.Next
	page, err = s.Read(ctx, id, req)
	if err != nil || page.Next != selected || len(page.Commits) != 0 {
		t.Fatalf("selected endpoint EOF = %+v, %v", page, err)
	}
	// Resume from an accepted checkpoint, retaining the captured endpoint while
	// a fourth commit is appended between pages.
	req = history.ReadRequest{After: r1, Through: r3, MaxCommits: 1}
	page, err = s.Read(ctx, id, req)
	wantPage(t, page, err, r2, second)
	fourth, r4 := encoded(t, id, r3, "fourth")
	appendAccepted(t, s, id, fourth, r4)
	req.After = page.Next
	page, err = s.Read(ctx, id, req)
	wantPage(t, page, err, r3, third)
	req.After = page.Next
	page, err = s.Read(ctx, id, req)
	wantPage(t, page, err, r3)
	req = history.ReadRequest{Through: r3, MaxCommits: 3}
	page, err = s.Read(ctx, id, req)
	wantPage(t, page, err, r3, root, second, third)
	parent := history.CommitRef{}
	for i, c := range page.Commits {
		if c.Commit.Sequence != parent.Sequence+1 || c.Commit.Parent != parent.CID || (i == 0 && c.Commit.HistoryID != id) {
			t.Fatalf("noncontiguous commit %d: %+v", i, c)
		}
		parent = c.Ref
	}
}

func pageHintDoesNotRaisePolicy(t *testing.T, factory historyFactory) {
	id := history.NewHistoryID()
	b, ref := encoded(t, id, history.CommitRef{}, "opaque")
	p := policy()
	p.MaxPageBytes = encodedLen(t, b) - 1
	s := factory(t, p)
	appendAccepted(t, s, id, b, ref)
	req := history.ReadRequest{Through: ref, MaxCommits: 1}
	for range 2 {
		page, err := s.Read(context.Background(), id, req)
		var hint history.PageTooSmallError
		if !errors.Is(err, history.ErrPageTooSmall) || !errors.As(err, &hint) || hint.RequiredBytes != encodedLen(t, b) || !reflect.DeepEqual(page, history.Page{}) {
			t.Fatalf("policy-limited page = %+v, %v", page, err)
		}
	}
	// Only a new explicitly configured History can use the diagnostic hint.
	p.MaxPageBytes = encodedLen(t, b)
	s = factory(t, p)
	appendAccepted(t, s, id, b, ref)
	page, err := s.Read(context.Background(), id, req)
	wantPage(t, page, err, ref, b)
}

// misalignedReadPolicy verifies that sharing storage never silently raises a
// reader's independent byte or DTO materialization caps.
func misalignedReadPolicy(t *testing.T, factory Factory) {
	for _, bound := range []string{"bytes", "records", "kind", "payload"} {
		t.Run(bound, func(t *testing.T) {
			backend := factory(t)
			writer, err := history.New(backend, policy())
			if err != nil {
				t.Fatal(err)
			}
			id := history.NewHistoryID()
			c, _ := encoded(t, id, history.CommitRef{}, "read bounds")
			c.Records = append(c.Records, c.Records[0].Clone())
			_, cid, err := format.EncodeCommit(c, policy().CommitLimits)
			if err != nil {
				t.Fatal(err)
			}
			ref := history.CommitRef{Sequence: 1, CID: cid}
			appendAccepted(t, writer, id, c, ref)
			p := policy()
			switch bound {
			case "bytes":
				p.CommitLimits.MaxCommitBytes = encodedLen(t, c) - 1
			case "records":
				p.CommitLimits.MaxRecords = 1
			case "kind":
				p.CommitLimits.MaxKindBytes = len(opaqueKind) - 1
			case "payload":
				p.CommitLimits.MaxPayloadBytes = 1
			}
			reader, err := history.New(backend, p)
			if err != nil {
				t.Fatal(err)
			}
			r := history.ReadRequest{Through: ref, MaxCommits: 1}
			got, err := reader.Read(context.Background(), id, r)
			if err == nil || !reflect.DeepEqual(got, history.Page{}) {
				t.Fatalf("read beyond %s policy = %+v, %v", bound, got, err)
			}
			got, err = writer.Read(context.Background(), id, r)
			wantPage(t, got, err, ref, c)
		})
	}
}

// independentInstancePolicies proves retry windows live in History, not Backend.
func independentInstancePolicies(t *testing.T, factory Factory) {
	backend := factory(t)
	p := policy()
	p.RetryWindow = 1
	narrow, err := history.New(backend, p)
	if err != nil {
		t.Fatal(err)
	}
	p.RetryWindow = 3
	wide, err := history.New(backend, p)
	if err != nil {
		t.Fatal(err)
	}
	id := history.NewHistoryID()
	root, r1 := encoded(t, id, history.CommitRef{}, "root")
	second, r2 := encoded(t, id, r1, "second")
	third, r3 := encoded(t, id, r2, "third")
	appendAccepted(t, narrow, id, root, r1)
	appendAccepted(t, wide, id, second, r2)
	appendAccepted(t, narrow, id, third, r3)
	got, err := narrow.Resolve(context.Background(), id, r1)
	wantResolution(t, got, err, history.StatusRetryExpired, r1, r3)
	got, err = wide.Resolve(context.Background(), id, r1)
	wantResolution(t, got, err, history.StatusAccepted, r1, r3)
}

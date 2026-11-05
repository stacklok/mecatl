package mem_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/conformance"
	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func policy() history.Policy {
	return history.Policy{
		CommitLimits: codec.Limits{MaxBytes: 4096, MaxArrayElements: 4, MaxStringBytes: 32, MaxByteStringBytes: 2048, MaxMapPairs: 8, MaxDepth: 8, MaxNodes: 128, MaxCollectionElements: 64},
		RetryWindow:  2, MaxPageBytes: 16384, MaxPageCommits: 16,
	}
}

func newBackend(*testing.T) history.Backend { return mem.New() }

func TestConformance(t *testing.T) { conformance.Run(t, newBackend) }

func TestAllocationChecks(t *testing.T) { conformance.RunAllocationChecks(t, newBackend) }

func TestPolicyValidationAndCopy(t *testing.T) {
	for _, mutate := range []func(*history.Policy){
		func(p *history.Policy) { p.CommitLimits.MaxBytes = 0 },
		func(p *history.Policy) { p.CommitLimits.MaxArrayElements = -1 },
		func(p *history.Policy) { p.CommitLimits.MaxStringBytes = 0 },
		func(p *history.Policy) { p.CommitLimits.MaxByteStringBytes = 0 },
		func(p *history.Policy) { p.RetryWindow = 0 },
		func(p *history.Policy) { p.MaxPageBytes = 0 },
		func(p *history.Policy) { p.MaxPageCommits = -1 },
	} {
		p := policy()
		mutate(&p)
		if s, err := history.New(mem.New(), p); s != nil || !errors.Is(err, history.ErrInvalid) {
			t.Fatalf("invalid policy returned %v, %v", s, err)
		}
	}
	if s, err := history.New(nil, policy()); s != nil || !errors.Is(err, history.ErrInvalid) {
		t.Fatalf("nil backend returned %v, %v", s, err)
	}
	p := policy()
	p.MaxPageCommits, p.MaxPageBytes, p.RetryWindow = math.MaxInt, math.MaxInt, math.MaxInt64
	s, err := history.New(mem.New(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.CommitLimits.MaxBytes = 1
	p.MaxPageBytes, p.MaxPageCommits, p.RetryWindow = 1, 1, 1
	id := history.NewHistoryID()
	c := format.Commit{FormatVersion: 1, HistoryID: &id, Sequence: 1,
		Records: []format.Record{{Kind: "opaque", KindVersion: 7, Payload: []byte("tiny")}},
	}
	data, err := codec.Encode(c, policy().CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	ref := history.CommitRef{Sequence: 1, CID: contentid.Sum(data)}
	got, err := s.Append(context.Background(), id, c)
	if err != nil || got != (history.Resolution{Status: history.StatusAccepted, Proposal: ref, Head: ref}) {
		t.Fatalf("append with copied policy = %+v, %v", got, err)
	}
	page, err := s.Read(context.Background(), id, history.ReadRequest{Through: ref, MaxCommits: math.MaxInt})
	if err != nil || len(page.Commits) != 1 || !page.Commits[0].Commit.Equal(c) || page.Commits[0].Ref != ref {
		t.Fatalf("copied policy/tiny result = %+v, %v", page, err)
	}
}

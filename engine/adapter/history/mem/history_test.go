package mem_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/conformance"
	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func policy() history.Policy {
	return history.Policy{
		CommitLimits: format.Limits{MaxCommitBytes: 4096, MaxRecords: 4, MaxKindBytes: 32, MaxPayloadBytes: 2048},
		RetryWindow:  2, MaxPageBytes: 16384, MaxPageCommits: 16,
	}
}

func newBackend(*testing.T) history.Backend { return mem.New() }

func TestConformance(t *testing.T) { conformance.Run(t, newBackend) }

func TestAllocationChecks(t *testing.T) { conformance.RunAllocationChecks(t, newBackend) }

func TestPolicyValidationAndCopy(t *testing.T) {
	for _, mutate := range []func(*history.Policy){
		func(p *history.Policy) { p.CommitLimits.MaxCommitBytes = 0 },
		func(p *history.Policy) { p.CommitLimits.MaxRecords = -1 },
		func(p *history.Policy) { p.CommitLimits.MaxKindBytes = 0 },
		func(p *history.Policy) { p.CommitLimits.MaxPayloadBytes = 0 },
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
	p.CommitLimits.MaxCommitBytes = 1
	p.MaxPageBytes, p.MaxPageCommits, p.RetryWindow = 1, 1, 1
	id := history.NewHistoryID()
	c := format.Commit{FormatVersion: 1, HistoryID: id, Sequence: 1,
		Records: []format.Record{{Kind: "opaque", KindVersion: 7, Payload: []byte("tiny")}},
	}
	_, cid, err := format.EncodeCommit(c, policy().CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	ref := history.CommitRef{Sequence: 1, CID: cid}
	got, err := s.Append(context.Background(), id, c)
	if err != nil || got != (history.Resolution{Status: history.StatusAccepted, Proposal: ref, Head: ref}) {
		t.Fatalf("append with copied policy = %+v, %v", got, err)
	}
	page, err := s.Read(context.Background(), id, history.ReadRequest{Through: ref, MaxCommits: math.MaxInt})
	if err != nil || len(page.Commits) != 1 || !page.Commits[0].Commit.Equal(c) || page.Commits[0].Ref != ref {
		t.Fatalf("copied policy/tiny result = %+v, %v", page, err)
	}
}

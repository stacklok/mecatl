// Package reflectionstore tests crash and concurrency behavior of the durable store.
package reflectionstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func testCandidate(x string) learning.Candidate {
	return learning.Candidate{Kind: learning.CandidateProjectFact, Key: "project/" + x, Value: "Use task test.", Description: "Test command", Evidence: []learning.EvidenceRef{{SessionID: session.SessionID("s-" + x), Locator: learning.EvidenceMessage, Ordinal: 0, Digest: strings.Repeat("d", 64)}}}
}
func TestSecretCandidateIsNeverPersisted(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	p := learning.ProposalPartition{Principal: "i", Project: "p"}
	c := testCandidate("secret")
	c.Value = "sk-abcdefghijklmnopqrstuvwxyz123456"
	if _, err := s.StageBatch(context.Background(), p, strings.Repeat("e", 64), []learning.Candidate{c}, nil); err == nil {
		t.Fatal("secret candidate persisted")
	}
	if raw, err := os.ReadFile(s.path); err == nil && strings.Contains(string(raw), c.Value) {
		t.Fatal("secret appears on disk")
	}
}

func TestRenameFailurePreservesPriorDocument(t *testing.T) {
	s, e := New(filepath.Join(t.TempDir(), "s"))
	if e != nil {
		t.Fatal(e)
	}
	p := learning.ProposalPartition{Principal: "i", Project: "p"}
	if _, e = s.StageBatch(context.Background(), p, strings.Repeat("a", 64), []learning.Candidate{testCandidate("one")}, nil); e != nil {
		t.Fatal(e)
	}
	s.rename = func(string, string) error { return errors.New("crash") }
	if _, e = s.StageBatch(context.Background(), p, strings.Repeat("b", 64), []learning.Candidate{testCandidate("two")}, nil); e == nil {
		t.Fatal("rename succeeded")
	}
	s.rename = os.Rename
	page, e := s.List(context.Background(), p, learning.ProposalList{})
	if e != nil || len(page.Records) != 1 {
		t.Fatalf("page=%#v err=%v", page, e)
	}
}
func TestConcurrentStoreHandlesDoNotLoseWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	a, _ := New(dir)
	b, _ := New(dir)
	p := learning.ProposalPartition{Principal: "i", Project: "p"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, x := range []struct {
		s *Store
		c learning.Candidate
	}{{a, testCandidate("one")}, {b, testCandidate("two")}} {
		wg.Add(1)
		go func(i int, x struct {
			s *Store
			c learning.Candidate
		}) {
			defer wg.Done()
			_, e := x.s.StageBatch(context.Background(), p, strings.Repeat(string(rune('a'+i)), 64), []learning.Candidate{x.c}, nil)
			errs <- e
		}(i, x)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	page, e := a.List(context.Background(), p, learning.ProposalList{})
	if e != nil || len(page.Records) != 2 {
		t.Fatalf("records=%d err=%v", len(page.Records), e)
	}
}

func TestLazyStartupAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.path); !os.IsNotExist(err) {
		t.Fatalf("empty startup created proposal document: %v", err)
	}
	part := learning.ProposalPartition{Principal: "principal", Project: "project"}
	staged, err := store.StageBatch(context.Background(), part, strings.Repeat("a", 64), []learning.Candidate{testCandidate("reopen")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.Get(context.Background(), part, staged[0].ID)
	if err != nil || !found || got.Version != staged[0].Version {
		t.Fatalf("reopen: found=%v record=%#v err=%v", found, got, err)
	}
}

func TestCorruptCrossPartitionRecordFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	part := learning.ProposalPartition{Principal: "principal", Project: "project"}
	if _, err = store.StageBatch(context.Background(), part, strings.Repeat("a", 64), []learning.Candidate{testCandidate("corrupt")}, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, records := range doc.Partitions {
		for id, record := range records {
			record.Partition.Principal = "other"
			records[id] = record
		}
	}
	raw, _ = json.Marshal(doc)
	if err = os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.List(context.Background(), part, learning.ProposalList{}); !errors.Is(err, learning.ErrInvalidProposal) {
		t.Fatalf("corrupt partition error=%v", err)
	}
}

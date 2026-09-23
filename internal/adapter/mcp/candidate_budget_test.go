package mcp

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCandidateListBudgetBoundsInputBeforePublication(t *testing.T) {
	budget := NewCandidateListBudget(2, 2, 4)
	if err := budget.consumePage(2); err != nil {
		t.Fatalf("entry boundary: %v", err)
	}
	if err := budget.consumePage(0); err != nil {
		t.Fatalf("page boundary: %v", err)
	}
	if err := budget.consumePage(0); err == nil {
		t.Fatal("page over-bound accepted")
	}

	buf := make([]byte, 8)
	n, err := budget.readResponse(strings.NewReader("12345"), buf)
	if err != nil || n != 4 {
		t.Fatalf("byte boundary read = (%d, %v), want (4, nil)", n, err)
	}
	n, err = budget.readResponse(strings.NewReader("5"), buf)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "bytes exceed") {
		t.Fatalf("byte over-bound read = (%d, %v)", n, err)
	}
	_, pages, bytes := budget.Stats()
	if pages != 2 || bytes != 4 {
		t.Fatalf("input counters: pages=%d bytes=%d", pages, bytes)
	}

	budget.Seal()
	n, err = budget.readResponse(strings.NewReader("12345"), buf)
	if err != nil {
		t.Fatalf("sealed runtime read: %v", err)
	}
	if n != 5 {
		t.Fatalf("sealed runtime read bytes = %d, want 5", n)
	}
}

type slowCountingReader struct {
	reads *atomic.Int64
}

func (r slowCountingReader) Read(p []byte) (int, error) {
	time.Sleep(10 * time.Millisecond)
	r.reads.Add(1)
	p[0] = 'x'
	return 1, nil
}

func TestCandidateListBudgetChargesConcurrentBodiesBeforeAllocation(t *testing.T) {
	const limit = 4
	budget := NewCandidateListBudget(1, 1, limit)
	var reads atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = budget.readResponse(slowCountingReader{reads: &reads}, make([]byte, 1))
		}()
	}
	wg.Wait()
	if got := reads.Load(); got != limit {
		t.Fatalf("underlying concurrent body reads = %d, want aggregate limit %d", got, limit)
	}
	_, _, gotBytes := budget.Stats()
	if gotBytes != limit {
		t.Fatalf("charged bytes = %d, want %d", gotBytes, limit)
	}
}

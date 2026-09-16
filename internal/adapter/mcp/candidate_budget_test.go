package mcp

import (
	"strings"
	"testing"
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
	if err == nil || n != 0 || !strings.Contains(err.Error(), "bytes exceed") {
		t.Fatalf("byte over-bound read = (%d, %v)", n, err)
	}
	_, pages, bytes := budget.Stats()
	if pages != 2 || bytes != 0 {
		t.Fatalf("failed input changed counters: pages=%d bytes=%d", pages, bytes)
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

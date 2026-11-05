package codec

import (
	"strings"
	"testing"
)

func newTestBudget(t *testing.T, l Limits) Budget {
	t.Helper()
	b, err := NewBudget(l)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBudgetScalarEstimate(t *testing.T) {
	for _, cap := range []int{8, 9, 10} {
		l := testLimits()
		l.MaxBytes = cap
		b := newTestBudget(t, l)
		err := b.CheckScalar(0)
		if cap < 9 {
			requireError(t, err, ErrLimitExceeded)
		} else if err != nil || b.EstimatedBytes() != 9 {
			t.Fatalf("scalar estimate = %d, %v", b.EstimatedBytes(), err)
		}
	}
	for _, length := range []int{0, 1, 12} {
		l := testLimits()
		l.MaxBytes = 9 + length
		b := newTestBudget(t, l)
		if err := b.CheckBytes(length, 0, l.MaxByteStringBytes); err != nil {
			t.Fatal(err)
		}
		if b.EstimatedBytes() != 9+length {
			t.Fatalf("estimate = %d", b.EstimatedBytes())
		}
		b = newTestBudget(t, l)
		requireError(t, b.CheckBytes(length+1, 0, l.MaxByteStringBytes), ErrLimitExceeded)
	}
}

func TestBudgetScalars(t *testing.T) {
	l := testLimits()
	l.MaxStringBytes = 4
	l.MaxByteStringBytes = 4
	for _, tc := range []struct {
		text string
		cap  int
		want error
	}{
		{"éé", 4, nil}, {"éé", 3, ErrLimitExceeded},
		{"12345", 8, ErrLimitExceeded}, {"\xff", 1, ErrInvalidData},
		// Length/cap rejection must precede the UTF-8 scan.
		{"\xff\xff", 1, ErrLimitExceeded}, {"x", 0, ErrInvalidLimits},
		{"x", -1, ErrInvalidLimits}, {"", 1, nil},
	} {
		b := newTestBudget(t, l)
		requireError(t, b.CheckString(tc.text, 0, tc.cap), tc.want)
	}
	for _, tc := range []struct {
		length, cap int
		want        error
	}{
		{4, 4, nil}, {4, 3, ErrLimitExceeded}, {5, 10, ErrLimitExceeded},
		{-1, 4, ErrInvalidData}, {0, 0, ErrInvalidLimits},
	} {
		b := newTestBudget(t, l)
		requireError(t, b.CheckBytes(tc.length, 0, tc.cap), tc.want)
	}
	b := newTestBudget(t, l)
	requireError(t, b.CheckScalar(-1), ErrInvalidData)
	b = newTestBudget(t, l)
	requireError(t, b.CheckScalar(l.MaxDepth+1), ErrLimitExceeded)
}

func TestBudgetDeclarationsBeforeVisits(t *testing.T) {
	l := testLimits()
	l.MaxNodes = 5
	l.MaxBytes = 45
	l.MaxCollectionElements = 2
	b := newTestBudget(t, l)
	if err := b.CheckMap(2, 0, l.MaxMapPairs); err != nil {
		t.Fatal(err)
	}
	if b.nodes != 5 || b.elements != 2 || b.EstimatedBytes() != 45 || b.visited != 1 {
		t.Fatalf("declarations not fully reserved: %+v", b)
	}
	for i := 0; i < 4; i++ {
		if err := b.CheckScalar(1); err != nil {
			t.Fatal(err)
		}
	}
	if b.EstimatedBytes() != 45 {
		t.Fatal("visited children charged twice")
	}
	requireError(t, b.CheckScalar(0), ErrInvalidData) // Not a second root.
	for _, mutate := range []func(*Limits){
		func(l *Limits) { l.MaxNodes-- },
		func(l *Limits) { l.MaxBytes-- },
		func(l *Limits) { l.MaxCollectionElements-- },
		func(l *Limits) { l.MaxMapPairs = 1 },
	} {
		tight := l
		mutate(&tight)
		b := newTestBudget(t, tight)
		requireError(t, b.CheckMap(2, 0, tight.MaxMapPairs), ErrLimitExceeded)
	}
	b = newTestBudget(t, l)
	requireError(t, b.CheckMap(2, 0, 1), ErrLimitExceeded)
	b = newTestBudget(t, l)
	requireError(t, b.CheckArray(-1, 0, 1), ErrInvalidData)
	b = newTestBudget(t, l)
	requireError(t, b.CheckArray(0, 0, 0), ErrInvalidLimits)
}

func TestBudgetNestedDeclarationsAndDepth(t *testing.T) {
	l := testLimits()
	l.MaxCollectionElements = 3
	b := newTestBudget(t, l)
	if err := b.CheckArray(2, 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckArray(1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckScalar(2); err != nil {
		t.Fatal(err)
	}
	// Root sibling: cumulative declaration budget is already exhausted.
	requireError(t, b.CheckMap(1, 1, 1), ErrLimitExceeded)
	l = testLimits()
	l.MaxNodes = 4
	b = newTestBudget(t, l)
	if err := b.CheckArray(2, 0, 2); err != nil {
		t.Fatal(err)
	}
	// It is not enough to count visited nodes: sibling exposure is reserved.
	requireError(t, b.CheckMap(1, 1, 1), ErrLimitExceeded)
	l = testLimits()
	l.MaxDepth = 1
	for _, nonempty := range []bool{false, true} {
		b := newTestBudget(t, l)
		if err := b.CheckArray(1, 0, 1); err != nil {
			t.Fatal(err)
		}
		n := 0
		var want error
		if nonempty {
			n, want = 1, ErrLimitExceeded
		}
		requireError(t, b.CheckArray(n, 1, 1), want)
	}
}

func TestBudgetOverflowSafe(t *testing.T) {
	maximum := int(^uint(0) >> 1)
	l := Limits{MaxBytes: maximum, MaxStringBytes: maximum, MaxByteStringBytes: maximum,
		MaxArrayElements: maximum, MaxMapPairs: maximum, MaxDepth: maximum,
		MaxNodes: maximum, MaxCollectionElements: maximum}
	b := newTestBudget(t, l)
	requireError(t, b.CheckMap(maximum, 0, maximum), ErrLimitExceeded)
	b = newTestBudget(t, l)
	requireError(t, b.CheckArray(maximum, 0, maximum), ErrLimitExceeded)
	b = newTestBudget(t, l)
	requireError(t, b.CheckBytes(maximum, 0, maximum), ErrLimitExceeded)
	// Near-limit reservation succeeds without overflowing the multiplication.
	b = newTestBudget(t, l)
	n := (maximum - 9) / 18
	if err := b.CheckMap(n, 0, maximum); err != nil {
		t.Fatal(err)
	}
	if b.EstimatedBytes() != 9+18*n {
		t.Fatal("bad near-limit estimate")
	}
	// Aggregate subtraction remains safe near MaxInt, without huge allocations.
	b.elements = maximum - 1
	requireError(t, b.CheckArray(2, 1, maximum), ErrLimitExceeded)
}

func TestBudgetRejectLargeScalarWithoutCopy(t *testing.T) {
	text := strings.Repeat("x", 1<<20)
	l := testLimits()
	// A rejected prebuilt scalar must not be copied or scanned for UTF-8.
	allocs := testing.AllocsPerRun(20, func() {
		b, err := NewBudget(l)
		if err != nil {
			panic(err)
		}
		if err := b.CheckString(text, 0, 1); err != ErrLimitExceeded {
			panic(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("rejection allocated %g times", allocs)
	}
}

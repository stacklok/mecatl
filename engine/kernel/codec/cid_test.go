package codec

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

var cidFixture = mustCID([36]byte{
	1, 0x71, 0x12, 32,
	0x03, 0x1c, 0xdb, 0x85, 0x8c, 0xa3, 0xbd, 0x65,
	0x7c, 0xdd, 0x72, 0xd0, 0x61, 0x94, 0xee, 0xf3,
	0xc7, 0x98, 0x8f, 0xa7, 0x0c, 0x40, 0x33, 0x7e,
	0xd4, 0xdf, 0x91, 0xbe, 0xd9, 0xa0, 0xfc, 0x74,
})

func mustCID(binary [36]byte) contentid.CID {
	v, err := contentid.ParseBinary(binary[:])
	if err != nil {
		panic(err)
	}
	return v
}

func cidLimits() Limits {
	return Limits{
		MaxBytes:              55,
		MaxStringBytes:        64,
		MaxByteStringBytes:    37,
		MaxArrayElements:      8,
		MaxMapPairs:           8,
		MaxDepth:              2,
		MaxNodes:              2,
		MaxCollectionElements: 1,
	}
}

func TestCIDExactLinkCheckReadAndEmit(t *testing.T) {
	want := []byte{
		0xd8, 0x2a, 0x58, 0x25, 0,
		1, 0x71, 0x12, 32,
		0x03, 0x1c, 0xdb, 0x85, 0x8c, 0xa3, 0xbd, 0x65,
		0x7c, 0xdd, 0x72, 0xd0, 0x61, 0x94, 0xee, 0xf3,
		0xc7, 0x98, 0x8f, 0xa7, 0x0c, 0x40, 0x33, 0x7e,
		0xd4, 0xdf, 0x91, 0xbe, 0xd9, 0xa0, 0xfc, 0x74,
	}
	if got, err := AppendCID([]byte{0xaa}, cidFixture); err != nil || !bytes.Equal(got, append([]byte{0xaa}, want...)) {
		t.Fatalf("AppendCID = %x, %v", got, err)
	}

	limits := cidLimits()
	budget, err := NewBudget(limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.CheckCID(cidFixture, 0); err != nil {
		t.Fatal(err)
	}
	if budget.EstimatedBytes() != 55 {
		t.Fatalf("estimated bytes = %d, want 55", budget.EstimatedBytes())
	}
	if budget.elements != 0 {
		t.Fatalf("CID charged collection elements: %d", budget.elements)
	}

	reader, err := NewReader(want, limits)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.CID(0)
	if err != nil || got != cidFixture {
		t.Fatalf("CID = %v, %v", got, err)
	}
	if err := reader.Done(); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i] = 0
	}
	if got != cidFixture {
		t.Fatal("CID borrows input")
	}
}

func TestCIDRejectsMalformedLinksAndZeroValues(t *testing.T) {
	valid, err := AppendCID(nil, cidFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"wrong tag", append([]byte{0xd8, 0x29}, valid[2:]...), ErrInvalidData},
		{"nonminimal tag", append([]byte{0xd9, 0, 42}, valid[2:]...), ErrInvalidData},
		{"stacked tag", append([]byte{0xd8, 42}, valid...), ErrInvalidData},
		{"nonminimal bytes", append([]byte{0xd8, 42, 0x59, 0, 37}, valid[4:]...), ErrInvalidData},
		{"wrong prefix", append([]byte{0xd8, 42, 0x58, 37, 1}, valid[5:]...), ErrInvalidData},
		{"wrong length", append([]byte{0xd8, 42, 0x58, 36}, valid[5:]...), ErrInvalidData},
		{"wrong profile", append([]byte{0xd8, 42, 0x58, 37, 0, 2}, valid[6:]...), ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewReader(tc.data, cidLimits())
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.CID(0)
			if !errors.Is(err, tc.want) || got != (contentid.CID{}) {
				t.Fatalf("CID = %v, %v", got, err)
			}
		})
	}
	for end := 0; end < len(valid); end++ {
		r, err := NewReader(valid[:end], cidLimits())
		if err != nil {
			t.Fatal(err)
		}
		if got, err := r.CID(0); !errors.Is(err, ErrInvalidData) || got != (contentid.CID{}) {
			t.Fatalf("prefix %d: CID = %v, %v", end, got, err)
		}
	}
	budget, err := NewBudget(cidLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.CheckCID(contentid.CID{}, 0); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("CheckCID zero = %v", err)
	}
	if got, err := AppendCID([]byte{1}, contentid.CID{}); !errors.Is(err, ErrInvalidData) || !bytes.Equal(got, []byte{1}) {
		t.Fatalf("AppendCID zero = %x, %v", got, err)
	}
}

func TestCIDReadAllocations(t *testing.T) {
	data, err := AppendCID(nil, cidFixture)
	if err != nil {
		t.Fatal(err)
	}
	limits := cidLimits()
	if got := testing.AllocsPerRun(100, func() {
		r, err := NewReader(data, limits)
		if err != nil {
			t.Fatal(err)
		}
		v, err := r.CID(0)
		if err != nil || v != cidFixture || r.Done() != nil {
			t.Fatal("CID read failed", err)
		}
	}); got != 0 {
		t.Fatalf("CID read allocated %g times; fixed-size result needs no heap copy", got)
	}
}

func TestCIDDepthNearOverflow(t *testing.T) {
	data, err := AppendCID(nil, cidFixture)
	if err != nil {
		t.Fatal(err)
	}
	limits := cidLimits()
	limits.MaxDepth = math.MaxInt
	for _, depth := range []int{math.MaxInt - 1, math.MaxInt} {
		var want error
		if depth == math.MaxInt {
			want = ErrLimitExceeded
		}
		b, err := NewBudget(limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.CheckCID(cidFixture, depth); !errors.Is(err, want) {
			t.Fatalf("CheckCID depth %d: %v, want %v", depth, err, want)
		}
		r, err := NewReader(data, limits)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.CID(depth)
		if !errors.Is(err, want) {
			t.Fatalf("CID depth %d: %v, want %v", depth, err, want)
		}
		if want == nil && (got != cidFixture || r.Done() != nil) {
			t.Fatal("maximum valid depth changed CID or traversal")
		}
		if want != nil && got != (contentid.CID{}) {
			t.Fatal("depth failure returned partial CID")
		}
	}
}

func TestCIDLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Limits)
		depth  int
		want   error
	}{
		{"bytes", func(l *Limits) { l.MaxBytes = 54 }, 0, ErrLimitExceeded},
		{"byte string", func(l *Limits) { l.MaxByteStringBytes = 36 }, 0, ErrLimitExceeded},
		{"nodes", func(l *Limits) { l.MaxNodes = 1 }, 0, ErrLimitExceeded},
		{"depth", func(l *Limits) { l.MaxDepth = 0 }, 0, ErrInvalidLimits},
		{"nested depth", func(l *Limits) { l.MaxDepth = 1 }, 1, ErrLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := cidLimits()
			tc.mutate(&limits)
			budget, err := NewBudget(limits)
			if tc.want == ErrInvalidLimits {
				if !errors.Is(err, tc.want) {
					t.Fatalf("NewBudget = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := budget.CheckCID(cidFixture, tc.depth); !errors.Is(err, tc.want) {
				t.Fatalf("CheckCID = %v", err)
			}
			if tc.name == "bytes" {
				reader, err := NewReader(nil, limits)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := reader.CID(0); got != (contentid.CID{}) || !errors.Is(err, ErrLimitExceeded) {
					t.Fatalf("CID on uninspected input = %v, %v", got, err)
				}
			}
		})
	}
}

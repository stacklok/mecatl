package codec

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

var cidFixture = [36]byte{
	1, 0x71, 0x12, 32,
	0x03, 0x1c, 0xdb, 0x85, 0x8c, 0xa3, 0xbd, 0x65,
	0x7c, 0xdd, 0x72, 0xd0, 0x61, 0x94, 0xee, 0xf3,
	0xc7, 0x98, 0x8f, 0xa7, 0x0c, 0x40, 0x33, 0x7e,
	0xd4, 0xdf, 0x91, 0xbe, 0xd9, 0xa0, 0xfc, 0x74,
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

func TestCIDLinkExactCheckReadAndEmit(t *testing.T) {
	want := []byte{
		0xd8, 0x2a, 0x58, 0x25, 0,
		1, 0x71, 0x12, 32,
		0x03, 0x1c, 0xdb, 0x85, 0x8c, 0xa3, 0xbd, 0x65,
		0x7c, 0xdd, 0x72, 0xd0, 0x61, 0x94, 0xee, 0xf3,
		0xc7, 0x98, 0x8f, 0xa7, 0x0c, 0x40, 0x33, 0x7e,
		0xd4, 0xdf, 0x91, 0xbe, 0xd9, 0xa0, 0xfc, 0x74,
	}
	if got := AppendCIDLink([]byte{0xaa}, cidFixture); len(got) != 42 || !bytes.Equal(got, append([]byte{0xaa}, want...)) {
		t.Fatalf("AppendCIDLink = %x", got)
	}
	b := newTestBudget(t, cidLimits())
	if err := b.CheckCIDLink(0); err != nil {
		t.Fatal(err)
	}
	if b.EstimatedBytes() != 55 || b.elements != 0 || b.nodes != 2 || b.visited != 2 {
		t.Fatalf("link accounting = %+v", b)
	}
	r := testReader(t, want, cidLimits())
	got, err := r.CIDLink(0)
	if err != nil || got != cidFixture {
		t.Fatalf("CIDLink = %x, %v", got, err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	if r.budget != b {
		t.Fatalf("read/check accounting differs: %+v, %+v", r.budget, b)
	}
	clear(want)
	if got != cidFixture {
		t.Fatal("CIDLink borrows input")
	}
}

func TestCIDLinkRejectsMalformedFraming(t *testing.T) {
	valid := AppendCIDLink(nil, cidFixture)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"wrong tag", append([]byte{0xd8, 0x29}, valid[2:]...)},
		{"nonminimal tag", append([]byte{0xd9, 0, 42}, valid[2:]...)},
		{"stacked tag", append([]byte{0xd8, 42}, valid...)},
		{"nonminimal bytes", append([]byte{0xd8, 42, 0x59, 0, 37}, valid[4:]...)},
		{"wrong prefix", append([]byte{0xd8, 42, 0x58, 37, 1}, valid[5:]...)},
		{"short length", append([]byte{0xd8, 42, 0x58, 36}, valid[5:]...)},
		{"long length", append([]byte{0xd8, 42, 0x58, 38}, valid[4:]...)},
		{"huge length", []byte{0xd8, 42, 0x5b, 255, 255, 255, 255, 255, 255, 255, 255}},
		{"wrong tag major", append([]byte{0x18, 42}, valid[2:]...)},
		{"text child", append([]byte{0xd8, 42, 0x78, 37}, valid[4:]...)},
		{"indefinite child", []byte{0xd8, 42, 0x5f, 0xff}},
		{"null", []byte{0xf6}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testReader(t, tc.data, cidLimits())
			got, err := r.CIDLink(0)
			if !errors.Is(err, ErrInvalidData) || got != ([36]byte{}) {
				t.Fatalf("CIDLink = %x, %v", got, err)
			}
		})
	}
	for end := 0; end < len(valid); end++ {
		r := testReader(t, valid[:end], cidLimits())
		if got, err := r.CIDLink(0); !errors.Is(err, ErrInvalidData) || got != ([36]byte{}) {
			t.Fatalf("prefix %d: CIDLink = %x, %v", end, got, err)
		}
	}
	r := testReader(t, append(valid, 0), cidLimits())
	if _, err := r.CIDLink(0); err != nil {
		t.Fatal(err)
	}
	requireError(t, r.Done(), ErrInvalidData)
}

func TestCIDLinkLeavesProfileValidationToOwner(t *testing.T) {
	for _, raw := range [][36]byte{{}, {2, 0xff, 0xff, 0xff}, cidFixture} {
		data := AppendCIDLink(nil, raw)
		r := testReader(t, data, cidLimits())
		got, err := r.CIDLink(0)
		if err != nil || got != raw || r.Done() != nil {
			t.Fatalf("raw roundtrip = %x, %v", got, err)
		}
	}
}

func TestCIDLinkReadAllocations(t *testing.T) {
	data := AppendCIDLink(nil, cidFixture)
	limits := cidLimits()
	if got := testing.AllocsPerRun(100, func() {
		r, err := NewReader(data, limits)
		if err != nil {
			t.Fatal(err)
		}
		v, err := r.CIDLink(0)
		if err != nil || v != cidFixture || r.Done() != nil {
			t.Fatal("CIDLink read failed", err)
		}
	}); got != 0 {
		t.Fatalf("CIDLink read allocated %g times", got)
	}
}

func TestCIDLinkRejectLengthWithoutAllocation(t *testing.T) {
	// Fully supplied oversized contents must fail before copying, just like an
	// oversized declaration without contents. Input allocation is outside the run.
	large := append([]byte{0xd8, 42, 0x5a, 0, 0x10, 0, 0}, make([]byte, 1<<20)...)
	limits := testLimits()
	limits.MaxBytes = len(large)
	for _, data := range [][]byte{large, large[:7]} {
		if got := testing.AllocsPerRun(100, func() {
			r, err := NewReader(data, limits)
			if err != nil {
				t.Fatal(err)
			}
			v, err := r.CIDLink(0)
			if err != ErrInvalidData || v != ([36]byte{}) || r.pos != 7 {
				t.Fatalf("length rejection = %x, %v, cursor %d", v, err, r.pos)
			}
		}); got != 0 {
			t.Fatalf("length rejection allocated %g times", got)
		}
	}
}

func TestCIDLinkDepthNearOverflow(t *testing.T) {
	data := AppendCIDLink(nil, cidFixture)
	limits := cidLimits()
	limits.MaxDepth = math.MaxInt
	for _, depth := range []int{-1, math.MaxInt - 1, math.MaxInt} {
		var want error
		if depth < 0 {
			want = ErrInvalidData
		} else if depth == math.MaxInt {
			want = ErrLimitExceeded
		}
		b := newTestBudget(t, limits)
		requireError(t, b.CheckCIDLink(depth), want)
		r := testReader(t, data, limits)
		got, err := r.CIDLink(depth)
		requireError(t, err, want)
		if want == nil && (got != cidFixture || r.Done() != nil) {
			t.Fatal("maximum valid depth changed link or traversal")
		}
		if want != nil && got != ([36]byte{}) {
			t.Fatal("depth failure returned partial link")
		}
	}
}

func TestCIDLinkLimits(t *testing.T) {
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
			b, err := NewBudget(limits)
			if tc.want == ErrInvalidLimits {
				requireError(t, err, tc.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requireError(t, b.CheckCIDLink(tc.depth), tc.want)
			// Policy rejects before any wire inspection.
			r := testReader(t, nil, limits)
			got, err := r.CIDLink(tc.depth)
			if got != ([36]byte{}) || !errors.Is(err, tc.want) {
				t.Fatalf("CIDLink on uninspected input = %x, %v", got, err)
			}
		})
	}
}

func TestCIDLinkNestedSharedBudget(t *testing.T) {
	limits := testLimits()
	limits.MaxBytes, limits.MaxNodes = 9+2*55, 5
	data := []byte{0x82}
	data = AppendCIDLink(data, cidFixture)
	data = AppendCIDLink(data, cidFixture)
	b := newTestBudget(t, limits)
	if err := b.CheckArray(2, 0, 2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := b.CheckCIDLink(1); err != nil {
			t.Fatal(err)
		}
	}
	r := testReader(t, data, limits)
	if _, err := r.Array(0, 2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.CIDLink(1); err != nil {
			t.Fatal(err)
		}
	}
	if r.Done() != nil || r.budget != b || b.size != 119 || b.elements != 2 {
		t.Fatal("nested link accounting differs", r.budget, b)
	}
	limits.MaxBytes--
	b = newTestBudget(t, limits)
	if err := b.CheckArray(2, 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckCIDLink(1); err != nil {
		t.Fatal(err)
	}
	requireError(t, b.CheckCIDLink(1), ErrLimitExceeded)
}

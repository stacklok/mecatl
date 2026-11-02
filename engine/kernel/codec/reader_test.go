package codec

import (
	"bytes"
	"errors"
	"math"
	"sync"
	"testing"
	"unsafe"

	"github.com/jcalabro/atmos/cbor"
)

func testReader(t *testing.T, data []byte, l Limits) Reader {
	t.Helper()
	r, err := NewReader(data, l)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReaderIntegerBounds(t *testing.T) {
	l := testLimits()
	// Independent wire extrema, not only Atmos append/read roundtrips.
	for _, tc := range []struct {
		data []byte
		want int64
	}{
		{[]byte{0x3b, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, math.MinInt64},
		{[]byte{0x1b, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, math.MaxInt64},
	} {
		r := testReader(t, tc.data, l)
		v, err := r.Int(0)
		if err != nil || v != tc.want {
			t.Fatalf("integer golden = %d, %v", v, err)
		}
	}
	r := testReader(t, []byte{0x1b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, l)
	maxUint, err := r.Uint(0)
	if err != nil || maxUint != math.MaxUint64 {
		t.Fatalf("uint golden = %d, %v", maxUint, err)
	}
	for _, v := range []int64{math.MinInt64, math.MinInt64 + 1, -65537, -257, -25, -24, -1, 0, 23, 24, 255, 256, 65535, 65536, math.MaxInt32, math.MaxInt64} {
		data := cbor.AppendInt(nil, v)
		r := testReader(t, data, l)
		got, err := r.Int(0)
		if err != nil || got != v {
			t.Fatalf("Int(%d) = %d, %v", v, got, err)
		}
		if err := r.Done(); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []uint64{0, 23, 24, 255, 256, 65535, 65536, math.MaxUint32, math.MaxUint32 + 1, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		data := cbor.AppendUint(nil, v)
		r := testReader(t, data, l)
		got, err := r.Uint(0)
		if err != nil || got != v {
			t.Fatalf("Uint(%d) = %d, %v", v, got, err)
		}
		if err := r.Done(); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range [][]byte{
		{0x1b, 0x80, 0, 0, 0, 0, 0, 0, 0}, // MaxInt64+1
		{0x3b, 0x80, 0, 0, 0, 0, 0, 0, 0}, // MinInt64-1
		{0x3b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		r := testReader(t, data, l)
		v, err := r.Int(0)
		requireError(t, err, ErrInvalidData)
		if v != 0 {
			t.Fatal("nonzero failure output")
		}
	}
	r = testReader(t, []byte{0x20}, l)
	v, err := r.Uint(0)
	requireError(t, err, ErrInvalidData)
	if v != 0 {
		t.Fatal("nonzero failure output")
	}
}

func TestReaderStrictHeaders(t *testing.T) {
	l := testLimits()
	// All nonminimal widths, reserved/indefinite headers, null, tags, simple
	// values, floats, wrong types, and truncated headers must be rejected.
	bad := [][]byte{
		nil, {0x18}, {0x19, 1}, {0x1a, 1}, {0x1b, 1},
		{0x18, 23}, {0x19, 0, 24}, {0x1a, 0, 0, 1, 0},
		{0x1b, 0, 0, 0, 0, 0, 1, 0, 0},
		{0x1c}, {0x1d}, {0x1e}, {0x1f}, {0xff}, {0xf6},
		{0xf4}, {0xf5}, {0xf7}, {0xf8, 0}, {0xf9, 0, 0},
		{0xfa, 0, 0, 0, 0}, {0xfb, 0, 0, 0, 0, 0, 0, 0, 0},
		{0xc0, 0}, {0xd8, 42, 0x40}, {0x40}, {0x60}, {0x80}, {0xa0},
	}
	for _, data := range bad {
		r := testReader(t, data, l)
		_, err := r.Int(0)
		requireError(t, err, ErrInvalidData)
	}
	for _, data := range [][]byte{{0x78, 1, 'x'}, {0x79, 0, 1, 'x'}, {0x7f}, {0x61}, {0x61, 0xff}, {0x41, 'x'}, {0xf6}} {
		r := testReader(t, data, l)
		v, err := r.String(0, l.MaxStringBytes)
		requireError(t, err, ErrInvalidData)
		if v != "" {
			t.Fatal("partial string")
		}
	}
	for _, data := range [][]byte{nil, {0}, {0xf6}, {0xf8, 20}, {0xf8, 21}, {0xff}} {
		r := testReader(t, data, l)
		v, err := r.Bool(0)
		requireError(t, err, ErrInvalidData)
		if v {
			t.Fatal("nonzero bool failure output")
		}
	}
	for _, value := range []bool{false, true} {
		r := testReader(t, cbor.AppendBool(nil, value), l)
		v, err := r.Bool(0)
		if err != nil || v != value {
			t.Fatalf("Bool = %v, %v", v, err)
		}
		if err := r.Done(); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range [][]byte{{0x98, 0}, {0x9f}, {0xf6}, {0xa0}} {
		r := testReader(t, data, l)
		_, err := r.Array(0, l.MaxArrayElements)
		requireError(t, err, ErrInvalidData)
	}
	for _, data := range [][]byte{{0xb8, 0}, {0xbf}, {0xf6}, {0x80}} {
		r := testReader(t, data, l)
		_, err := r.Map(0, l.MaxMapPairs)
		requireError(t, err, ErrInvalidData)
	}
}

func TestReaderLimitsAndDeclarations(t *testing.T) {
	l := testLimits()
	l.MaxBytes = 1
	_, err := NewReader([]byte{0, 0}, l)
	requireError(t, err, ErrLimitExceeded)
	r := testReader(t, []byte{0}, l)
	_, err = r.Uint(0)
	requireError(t, err, ErrLimitExceeded) // Conservative headroom, not actual length.
	l = testLimits()
	for _, data := range [][]byte{
		{0x7b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0x7b, 0x80, 0, 0, 0, 0, 0, 0, 0},
		{0x62, 0xff, 0xff},
	} {
		r := testReader(t, data, l)
		_, err := r.String(0, 1)
		requireError(t, err, ErrLimitExceeded) // Before scan, even for invalid UTF-8.
	}
	r = testReader(t, []byte{0x61, 0xff}, l)
	_, err = r.String(0, 0)
	requireError(t, err, ErrInvalidLimits)
	for _, shape := range []struct {
		data []byte
		read func(*Reader) (int, error)
	}{
		{[]byte{0x82, 0, 0}, func(r *Reader) (int, error) { return r.Array(0, 2) }},
		{[]byte{0xa1, 0x60, 0}, func(r *Reader) (int, error) { return r.Map(0, 1) }},
	} {
		for _, mutate := range []func(*Limits){
			func(l *Limits) { l.MaxNodes = 2 },
			func(l *Limits) { l.MaxBytes = 26 },
		} {
			l := testLimits()
			mutate(&l)
			r := testReader(t, shape.data, l)
			count, err := shape.read(&r)
			requireError(t, err, ErrLimitExceeded)
			if count != 0 {
				t.Fatal("unsafe allocation count on failure")
			}
		}
	}
	// Truncated declarations fail before returning a count for allocation.
	for _, data := range [][]byte{{0x82, 0}, {0x98, 128}, {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		r := testReader(t, data, l)
		_, err := r.Array(0, l.MaxArrayElements)
		if err == nil {
			t.Fatal("accepted impossible declaration")
		}
	}
	r = testReader(t, []byte{0xa1, 0}, l)
	_, err = r.Map(0, 1)
	requireError(t, err, ErrInvalidData)
	// Parent reserves sibling node exposure before a nested prefix is inspected.
	l.MaxNodes = 4
	r = testReader(t, []byte{0x82, 0xa1, 0x60, 0, 0}, l)
	if _, err := r.Array(0, 2); err != nil {
		t.Fatal(err)
	}
	_, err = r.Map(1, 1)
	requireError(t, err, ErrLimitExceeded)
	l = testLimits()
	l.MaxDepth = 1
	r = testReader(t, []byte{0x81, 0x81, 0}, l)
	if _, err := r.Array(0, 1); err != nil {
		t.Fatal(err)
	}
	_, err = r.Array(1, 1)
	requireError(t, err, ErrLimitExceeded)
}

func TestReaderByteStringCapsAndStrictness(t *testing.T) {
	l := testLimits()
	l.MaxByteStringBytes = 2
	for _, tc := range []struct {
		data []byte
		cap  int
		want error
	}{
		{[]byte{0x42, 0xff, 0}, 2, nil},
		{[]byte{0x42, 0xff, 0}, 1, ErrLimitExceeded},
		{[]byte{0x43, 0, 0, 0}, 3, ErrLimitExceeded},
		{[]byte{0x42, 0}, 2, ErrInvalidData},
		{[]byte{0x58, 1, 0}, 2, ErrInvalidData},
		{[]byte{0x5f}, 2, ErrInvalidData},
		{[]byte{0xf6}, 2, ErrInvalidData},
		{[]byte{0x60}, 2, ErrInvalidData},
		{[]byte{0x40}, 0, ErrInvalidLimits},
		{[]byte{0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, 2, ErrLimitExceeded},
	} {
		r := testReader(t, tc.data, l)
		v, err := r.Bytes(0, tc.cap)
		requireError(t, err, tc.want)
		if err != nil && v != nil {
			t.Fatal("non-nil failure bytes")
		}
	}
}

func TestReaderCollectionCapsAndAggregate(t *testing.T) {
	l := testLimits()
	l.MaxArrayElements = 1
	r := testReader(t, []byte{0x82, 0, 0}, l)
	_, err := r.Array(0, 2)
	requireError(t, err, ErrLimitExceeded)
	l = testLimits()
	r = testReader(t, []byte{0x82, 0, 0}, l)
	_, err = r.Array(0, 1)
	requireError(t, err, ErrLimitExceeded)
	l.MaxMapPairs = 1
	r = testReader(t, []byte{0xa2, 0x60, 0, 0x60, 0}, l)
	_, err = r.Map(0, 2)
	requireError(t, err, ErrLimitExceeded)
	l = testLimits()
	r = testReader(t, []byte{0xa2, 0x60, 0, 0x60, 0}, l)
	_, err = r.Map(0, 1)
	requireError(t, err, ErrLimitExceeded)
	l.MaxCollectionElements = 3
	r = testReader(t, []byte{0x82, 0x82, 0, 0, 0}, l)
	if _, err := r.Array(0, 2); err != nil {
		t.Fatal(err)
	}
	_, err = r.Array(1, 2)
	requireError(t, err, ErrLimitExceeded)
	// A root map counts pairs, not twice the pairs, against declaration policy.
	l.MaxCollectionElements = 1
	r = testReader(t, []byte{0xa1, 0x60, 0}, l)
	if _, err := r.Map(0, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.String(1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Uint(1); err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
}

func TestReaderOwnershipAndOpaqueBytes(t *testing.T) {
	l := testLimits()
	data := []byte{0x82, 0x62, 'o', 'k', 0x43, 0xff, 0, 0xfe}
	r := testReader(t, data, l)
	if _, err := r.Array(0, 2); err != nil {
		t.Fatal(err)
	}
	s, err := r.String(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.Bytes(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Done(); err != nil {
		t.Fatal(err)
	}
	if s != "ok" || !bytes.Equal(v, []byte{0xff, 0, 0xfe}) {
		t.Fatal("changed scalar")
	}
	v[0] = 1
	if data[5] != 0xff {
		t.Fatal("bytes alias input")
	}
	for i := range data {
		data[i] = 0
	}
	if s != "ok" || !bytes.Equal(v, []byte{1, 0, 0xfe}) {
		t.Fatal("input aliases result")
	}
	// Inspect addresses only to prove a tiny decoded string does not retain the
	// backing input. No unsafe conversions are used by the production runtime.
	large := make([]byte, 1<<20)
	large[0], large[1] = 0x61, 'x'
	l.MaxBytes = len(large)
	r = testReader(t, large, l)
	s, err = r.String(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	address := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	start := uintptr(unsafe.Pointer(unsafe.SliceData(large)))
	if address >= start && address-start < uintptr(len(large)) {
		t.Fatal("string retains input")
	}
	// A short prefix followed by a large unused tail detects an implementation
	// that clones the complete input and then borrows the string from that clone.
	// This is an allocation bound for this operation, not an RSS promise.
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			r, err := NewReader(large, l)
			if err != nil {
				b.Fatal(err)
			}
			got, err := r.String(0, 1)
			if err != nil || got != "x" {
				b.Fatal(got, err)
			}
		}
	})
	if got := result.AllocedBytesPerOp(); got > 256 {
		t.Fatalf("short string decode allocated %d bytes/op; possible whole-input copy", got)
	}
	r = testReader(t, []byte{0x40}, l)
	v, err = r.Bytes(0, 1)
	if err != nil || v == nil || len(v) != 0 {
		t.Fatalf("empty bytes = %v, %v", v, err)
	}
}

// walkSample exercises only runtime calls, not a fixture or generated consumer.
func walkSample(r *Reader) error {
	if _, err := r.Map(0, 1); err != nil {
		return err
	}
	if _, err := r.String(1, 1); err != nil {
		return err
	}
	if _, err := r.Array(1, 2); err != nil {
		return err
	}
	if _, err := r.String(2, 2); err != nil {
		return err
	}
	if _, err := r.Bytes(2, 2); err != nil {
		return err
	}
	return r.Done()
}

func TestReaderEstimateSymmetryAndTruncation(t *testing.T) {
	data := []byte{0xa1, 0x61, 'k', 0x82, 0x62, 'h', 'i', 0x42, 0xff, 0}
	l := testLimits()
	b := newTestBudget(t, l)
	for _, err := range []error{b.CheckMap(1, 0, 1), b.CheckString("k", 1, 1),
		b.CheckArray(2, 1, 2), b.CheckString("hi", 2, 2), b.CheckBytes(2, 2, 2)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	const estimate = 5*9 + 5
	if b.EstimatedBytes() != estimate {
		t.Fatal("bad check estimate")
	}
	l.MaxBytes = estimate
	r := testReader(t, data, l)
	if err := walkSample(&r); err != nil {
		t.Fatal(err)
	}
	if r.budget.EstimatedBytes() != estimate {
		t.Fatal("decode estimate differs")
	}
	l.MaxBytes--
	r = testReader(t, data, l)
	requireError(t, walkSample(&r), ErrLimitExceeded)
	l = testLimits()
	for end := 0; end < len(data); end++ {
		r := testReader(t, data[:end], l)
		if err := walkSample(&r); err == nil {
			t.Fatalf("accepted prefix %d", end)
		}
	}
	r = testReader(t, append(bytes.Clone(data), 0), l)
	requireError(t, walkSample(&r), ErrInvalidData)
	r = testReader(t, nil, l)
	requireError(t, r.Done(), ErrInvalidData)
	r = testReader(t, []byte{0x81, 0}, l)
	if _, err := r.Array(0, 1); err != nil {
		t.Fatal(err)
	}
	requireError(t, r.Done(), ErrInvalidData)
}

func TestReaderIndependentOfAtmosGlobals(t *testing.T) {
	// No parallel tests in this package mutate these globals. Independent runtime
	// calls below never read them, so the race check also protects that boundary.
	oldSize, oldDepth := cbor.MaxSize, cbor.MaxDepth
	defer func() { cbor.MaxSize, cbor.MaxDepth = oldSize, oldDepth }()
	cbor.MaxSize, cbor.MaxDepth = 0, 0
	l := testLimits()
	r := testReader(t, []byte{0xa1, 0x61, 'k', 0x82, 0x62, 'h', 'i', 0x42, 0xff, 0}, l)
	if err := walkSample(&r); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConcurrentReadOnlyPolicy(t *testing.T) {
	l := testLimits()
	l.Named = map[string]int{"cap": 1, "ignored": -1}
	data := []byte{0x61, 'x'}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < cap(errs); i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				maximum, err := l.NamedLimit("cap")
				if err != nil {
					errs <- err
					return
				}
				b, err := NewBudget(l)
				if err != nil {
					errs <- err
					return
				}
				if err := b.CheckString("x", 0, maximum); err != nil {
					errs <- err
					return
				}
				r, err := NewReader(data, l)
				if err != nil {
					errs <- err
					return
				}
				v, err := r.String(0, maximum)
				if err != nil {
					errs <- err
					return
				}
				if v != "x" {
					errs <- errors.New("changed value")
					return
				}
				if err := r.Done(); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

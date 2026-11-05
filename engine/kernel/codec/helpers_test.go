package codec_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// These small test-owned implementations stand in for generated owner methods.
// The external package proves Decode's pointer inference through the public API
// without depending on generated fixtures or identity packages.
type helperBytes []byte

func (v helperBytes) CheckCBOR(b *codec.Budget, depth, fieldMax int) error {
	return b.CheckBytes(len(v), depth, fieldMax)
}

func (v helperBytes) AppendCBOR(dst []byte) []byte {
	// Test inputs fit a one-byte CBOR byte-string header.
	return append(append(dst, 0x40+byte(len(v))), v...)
}

func (v *helperBytes) ReadCBOR(r *codec.Reader, depth, fieldMax int) error {
	if v == nil {
		return codec.ErrInvalidData
	}
	value, err := r.Bytes(depth, fieldMax)
	if err == nil {
		*v = value
	}
	return err
}

type helperPair [2]helperBytes

func (v helperPair) CheckCBOR(b *codec.Budget, depth, fieldMax int) error {
	if err := b.CheckArray(len(v), depth, fieldMax); err != nil {
		return err
	}
	for _, child := range v {
		if err := child.CheckCBOR(b, depth+1, math.MaxInt); err != nil {
			return err
		}
	}
	return nil
}

func (v helperPair) AppendCBOR(dst []byte) []byte {
	dst = append(dst, 0x82)
	for _, child := range v {
		dst = child.AppendCBOR(dst)
	}
	return dst
}

func (v *helperPair) ReadCBOR(r *codec.Reader, depth, fieldMax int) error {
	if v == nil {
		return codec.ErrInvalidData
	}
	n, err := r.Array(depth, fieldMax)
	if err != nil {
		return err
	}
	if n != len(v) {
		return codec.ErrInvalidData
	}
	var value helperPair
	for i := range value {
		if err := value[i].ReadCBOR(r, depth+1, math.MaxInt); err != nil {
			return err
		}
	}
	*v = value
	return nil
}

var (
	_ codec.Marshaler   = helperBytes(nil)
	_ codec.Unmarshaler = (*helperBytes)(nil)
	_ codec.Marshaler   = helperPair{}
	_ codec.Unmarshaler = (*helperPair)(nil)
)

func helperLimits() codec.Limits {
	return codec.Limits{
		MaxBytes: 128, MaxStringBytes: 24, MaxByteStringBytes: 24,
		MaxArrayElements: 2, MaxMapPairs: 2, MaxDepth: 2,
		MaxNodes: 3, MaxCollectionElements: 2,
	}
}

func TestHelpersCrossPackagePointerInferenceAndOwnership(t *testing.T) {
	value := helperPair{{1, 2, 3}, {4}}
	limits := helperLimits()
	if err := codec.Check(value, limits); err != nil {
		t.Fatal(err)
	}
	data, err := codec.Encode(value, limits)
	if err != nil || !bytes.Equal(data, []byte{0x82, 0x43, 1, 2, 3, 0x41, 4}) {
		t.Fatalf("Encode = %x, %v", data, err)
	}
	// No explicit pointer type argument: P must infer as *helperPair.
	got, err := codec.Decode[helperPair](data, limits)
	if err != nil || !reflect.DeepEqual(got, value) {
		t.Fatalf("Decode = %v, %v", got, err)
	}
	clear(data)
	value[0][0] = 99
	if got[0][0] != 1 {
		t.Fatal("decoded value aliases input or original value")
	}
	// A root with a defined slice type also infers its pointer implementation.
	leaf, err := codec.Decode[helperBytes]([]byte{0x41, 7}, limits)
	if err != nil || !bytes.Equal(leaf, []byte{7}) {
		t.Fatalf("leaf Decode = %v, %v", leaf, err)
	}
}

// helperProbe observes traversal ordering and preallocation without global state.
type helperProbe struct {
	check  func(*codec.Budget, int, int) error
	append func([]byte) []byte
}

func (v helperProbe) CheckCBOR(b *codec.Budget, depth, fieldMax int) error {
	return v.check(b, depth, fieldMax)
}

func (v helperProbe) AppendCBOR(dst []byte) []byte { return v.append(dst) }

func TestHelpersCheckBeforePreallocatedAppendOnce(t *testing.T) {
	var checks, appends int
	limits := helperLimits()
	probe := helperProbe{
		check: func(b *codec.Budget, depth, fieldMax int) error {
			checks++
			if depth != 0 || fieldMax != math.MaxInt || appends != 0 {
				t.Fatal("wrong root arguments or append before check")
			}
			// The operation already owns a validated policy copy. Nested checks
			// neither consult nor revalidate the caller's now-invalid policy.
			limits = codec.Limits{}
			return b.CheckBytes(3, depth, fieldMax)
		},
		append: func(dst []byte) []byte {
			appends++
			if checks != 1 || len(dst) != 0 || cap(dst) != 12 {
				t.Fatalf("append: checks %d, len %d, cap %d", checks, len(dst), cap(dst))
			}
			return append(dst, 0x43, 1, 2, 3)
		},
	}
	data, err := codec.Encode(probe, limits)
	if err != nil || checks != 1 || appends != 1 || !bytes.Equal(data, []byte{0x43, 1, 2, 3}) {
		t.Fatalf("Encode = %x, %v, checks %d, appends %d", data, err, checks, appends)
	}
	checks, appends = 0, 0
	if err := codec.Check(probe, helperLimits()); err != nil || checks != 1 || appends != 0 {
		t.Fatalf("Check = %v, checks %d, appends %d", err, checks, appends)
	}
	// Even a prior successful Check never bypasses Encode's own admission.
	probe.check = func(*codec.Budget, int, int) error { return codec.ErrInvalidData }
	data, err = codec.Encode(probe, helperLimits())
	if data != nil || !errors.Is(err, codec.ErrInvalidData) || appends != 0 {
		t.Fatalf("failed Encode = %x, %v, appends %d", data, err, appends)
	}
}

func TestHelpersEncodeRejectsBeforeOutputAllocation(t *testing.T) {
	const size = 1 << 20
	limits := helperLimits()
	limits.MaxBytes, limits.MaxByteStringBytes = 2*size, size
	var appends int
	probe := helperProbe{
		check: func(b *codec.Budget, depth, fieldMax int) error {
			if err := b.CheckBytes(size, depth, fieldMax); err != nil {
				return err
			}
			// A local semantic rejection follows complete structural admission.
			return codec.ErrInvalidData
		},
		append: func(dst []byte) []byte {
			appends++
			return dst
		},
	}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			data, err := codec.Encode(probe, limits)
			if data != nil || !errors.Is(err, codec.ErrInvalidData) {
				b.Fatalf("Encode = %x, %v", data, err)
			}
		}
	})
	if appends != 0 {
		t.Fatal("append after check failure")
	}
	if got := result.AllocedBytesPerOp(); got > 1024 {
		t.Fatalf("rejected Encode allocated %d bytes/op, maximum 1024", got)
	}
}

func TestHelpersGlobalPolicyPrecedesTraversal(t *testing.T) {
	probe := helperProbe{
		check: func(*codec.Budget, int, int) error {
			t.Fatal("inspected value before policy validation")
			return nil
		},
		append: func([]byte) []byte {
			t.Fatal("appended after invalid policy")
			return nil
		},
	}
	fields := []func(*codec.Limits){
		func(l *codec.Limits) { l.MaxBytes = 0 },
		func(l *codec.Limits) { l.MaxStringBytes = 0 },
		func(l *codec.Limits) { l.MaxByteStringBytes = 0 },
		func(l *codec.Limits) { l.MaxArrayElements = 0 },
		func(l *codec.Limits) { l.MaxMapPairs = 0 },
		func(l *codec.Limits) { l.MaxDepth = 0 },
		func(l *codec.Limits) { l.MaxNodes = 0 },
		func(l *codec.Limits) { l.MaxCollectionElements = 0 },
	}
	for _, invalidate := range fields {
		l := helperLimits()
		invalidate(&l)
		if err := codec.Check(probe, l); !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal("Check policy error", err)
		}
		if data, err := codec.Encode(probe, l); data != nil || !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatalf("Encode = %x, %v", data, err)
		}
		// Malformed, oversized input must not take precedence over any global.
		if value, err := codec.Decode[helperPair](make([]byte, 129), l); !reflect.DeepEqual(value, helperPair{}) || !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatalf("Decode = %v, %v", value, err)
		}
	}
}

func TestHelpersSharedAdmissionAndZeroFailures(t *testing.T) {
	value := helperPair{{1, 2, 3}, {4}}
	for _, tighten := range []func(*codec.Limits){
		func(l *codec.Limits) { l.MaxBytes = 30 }, // estimate is 31, wire is only 7
		func(l *codec.Limits) { l.MaxByteStringBytes = 2 },
		func(l *codec.Limits) { l.MaxArrayElements = 1 },
		func(l *codec.Limits) { l.MaxNodes = 2 },
		func(l *codec.Limits) { l.MaxCollectionElements = 1 },
	} {
		l := helperLimits()
		tighten(&l)
		if err := codec.Check(value, l); !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatal("Check failed to enforce shared/global cap", err)
		}
		if data, err := codec.Encode(value, l); data != nil || !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatalf("Encode = %x, %v", data, err)
		}
		got, err := codec.Decode[helperPair]([]byte{0x82, 0x43, 1, 2, 3, 0x41, 4}, l)
		if !reflect.DeepEqual(got, helperPair{}) || !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatalf("Decode = %v, %v", got, err)
		}
	}
	for _, data := range [][]byte{
		nil, {0xff}, {0x82, 0x41, 1}, // success of first nested read, then truncation
		{0x82, 0x41, 1, 0x41, 2, 0}, // all nested reads succeed, root Done fails
	} {
		got, err := codec.Decode[helperPair](data, helperLimits())
		if !reflect.DeepEqual(got, helperPair{}) || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("Decode %x = %v, %v", data, got, err)
		}
	}
	// Complete input byte cap applies before decoding as well as to estimates.
	l := helperLimits()
	l.MaxBytes = 1
	got, err := codec.Decode[helperBytes]([]byte{0x41, 1}, l)
	if got != nil || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("oversized Decode = %x, %v", got, err)
	}
}

// helperReject deliberately assigns before failing, to prove that the generic
// root does not leak even a badly behaved implementation's partial output.
type helperReject struct{ Read bool }

func (v *helperReject) ReadCBOR(r *codec.Reader, depth, fieldMax int) error {
	if depth != 0 || fieldMax != math.MaxInt {
		return errors.New("wrong root arguments")
	}
	if _, err := r.Bool(depth); err != nil {
		return err
	}
	v.Read = true
	return codec.ErrInvalidData
}

func TestHelpersRootDiscardsPartialReceiver(t *testing.T) {
	got, err := codec.Decode[helperReject]([]byte{0xf5}, helperLimits())
	if got != (helperReject{}) || !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("Decode = %+v, %v", got, err)
	}
}

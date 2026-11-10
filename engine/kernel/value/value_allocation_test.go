package value_test

import (
	"bytes"
	"errors"
	"math"
	"runtime"
	"strconv"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

const rejectionWidth = 8192

func rejectionLimits() codec.Limits {
	l := limits()
	l.MaxArrayElements = rejectionWidth
	l.MaxMapPairs = rejectionWidth
	l.MaxNodes = 4 * rejectionWidth
	l.MaxCollectionElements = 4 * rejectionWidth
	return l
}

func wideCollections() ([]value.Value, value.Map) {
	array := make([]value.Value, rejectionWidth)
	object := make(value.Map, rejectionWidth)
	for i := range array {
		array[i] = value.Null()
		object[strconv.Itoa(i)] = value.Null()
	}
	return array, object
}

// rejectedBytes measures already-built inputs, not fixture construction. No tests
// in this package run in parallel. The allowance tolerates fixed operation state
// and runtime noise but is far below one wide slice or map clone/allocation.
func rejectedBytes(t *testing.T, operation func()) {
	t.Helper()
	operation()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 32 {
		operation()
	}
	runtime.ReadMemStats(&after)
	if got := (after.TotalAlloc - before.TotalAlloc) / 32; got > 16<<10 {
		t.Fatalf("rejection allocated %d bytes/op; maximum 16 KiB", got)
	}
}

func TestAdmissionRejectsBeforeCloningWideCollections(t *testing.T) {
	array, object := wideCollections()
	for _, invalid := range []bool{false, true} {
		if invalid {
			array[0] = value.Value{}
			// Every map child is invalid so map iteration cannot hide traversal.
			for key := range object {
				object[key] = value.Value{}
			}
		}
		for _, tc := range []struct {
			name  string
			admit func(codec.Limits) error
		}{
			{"array", func(l codec.Limits) error {
				v, err := value.Array(array).Admit(l)
				if err != nil && v.Kind() != value.InvalidKind {
					t.Fatal("failed admission published a view")
				}
				runtime.KeepAlive(v)
				return err
			}},
			{"object", func(l codec.Limits) error {
				v, err := value.Object(object).Admit(l)
				if err != nil && v.Kind() != value.InvalidKind {
					t.Fatal("failed admission published a view")
				}
				runtime.KeepAlive(v)
				return err
			}},
			{"map", func(l codec.Limits) error {
				v, err := object.Admit(l)
				if err != nil && v.Len() != 0 {
					t.Fatal("failed admission published a map")
				}
				runtime.KeepAlive(v)
				return err
			}},
		} {
			t.Run(tc.name+"/invalid="+strconv.FormatBool(invalid), func(t *testing.T) {
				want := error(nil)
				if invalid {
					want = codec.ErrInvalidData
				}
				if err := tc.admit(rejectionLimits()); !errors.Is(err, want) {
					t.Fatalf("unrestricted control: %v, want %v", err, want)
				}
				for _, bound := range rejectionCaps() {
					t.Run(bound.name, func(t *testing.T) {
						l := rejectionLimits()
						bound.set(&l)
						rejectedBytes(t, func() {
							if err := tc.admit(l); !errors.Is(err, codec.ErrLimitExceeded) {
								t.Fatalf("declaration must fail before children: %v", err)
							}
						})
					})
				}
			})
		}
	}
}

func rejectionCaps() []struct {
	name string
	set  func(*codec.Limits)
} {
	return []struct {
		name string
		set  func(*codec.Limits)
	}{
		{"collection", func(l *codec.Limits) { l.MaxArrayElements = 1; l.MaxMapPairs = 1 }},
		{"nodes", func(l *codec.Limits) { l.MaxNodes = rejectionWidth }},
		{"elements", func(l *codec.Limits) { l.MaxCollectionElements = rejectionWidth - 1 }},
		// Still above the complete wire size: NewReader's input-size check must
		// not mask the collection's conservative declared-header reservation.
		{"bytes", func(l *codec.Limits) { l.MaxBytes = 8 * rejectionWidth }},
	}
}

func TestCBORRejectsDeclarationsBeforeCollectionAllocation(t *testing.T) {
	array, object := wideCollections()
	for _, tc := range []struct {
		name string
		v    value.Value
	}{
		{"array", value.Array(array)},
		{"object", value.Object(object)},
		{"map", value.Object(object)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := codec.Encode(tc.v, rejectionLimits())
			if err != nil {
				t.Fatal(err)
			}
			read := func(data []byte, l codec.Limits, fieldMax int) error {
				r, err := codec.NewReader(data, l)
				if err != nil {
					t.Fatalf("input-size check masked declaration rejection: %v", err)
				}
				if tc.name == "map" {
					var out value.Map
					err = out.ReadCBOR(&r, 0, fieldMax)
					if err != nil && out != nil {
						t.Fatal("failed read published map storage")
					}
					runtime.KeepAlive(out)
				} else {
					var out value.Value
					err = out.ReadCBOR(&r, 0, fieldMax)
					if err != nil && value.Borrow(out).Kind() != value.InvalidKind {
						t.Fatal("failed read published value storage")
					}
					runtime.KeepAlive(out)
				}
				if err != nil {
					return err
				}
				return r.Done()
			}
			if err := read(data, rejectionLimits(), math.MaxInt); err != nil {
				t.Fatalf("fully populated wire control: %v", err)
			}
			for _, invalid := range []bool{false, true} {
				if invalid {
					data = bytes.Clone(data)
					// Keys contain only ASCII digits, so the first null byte is
					// the first child. Reject before even that child's traversal.
					data[bytes.IndexByte(data, 0xf6)] = 0x40 // Unsupported byte string, not truncation.
					if err := read(data, rejectionLimits(), math.MaxInt); !errors.Is(err, codec.ErrInvalidData) {
						t.Fatalf("invalid-child control: %v", err)
					}
				}
				for _, bound := range rejectionCaps() {
					t.Run(bound.name+"/invalid="+strconv.FormatBool(invalid), func(t *testing.T) {
						l := rejectionLimits()
						bound.set(&l)
						rejectedBytes(t, func() {
							if err := read(data, l, math.MaxInt); !errors.Is(err, codec.ErrLimitExceeded) {
								t.Fatalf("declaration must fail before children: %v", err)
							}
						})
					})
				}
				t.Run("field/invalid="+strconv.FormatBool(invalid), func(t *testing.T) {
					rejectedBytes(t, func() {
						if err := read(data, rejectionLimits(), 1); !errors.Is(err, codec.ErrLimitExceeded) {
							t.Fatalf("field declaration must fail before children: %v", err)
						}
					})
				})
			}
		})
	}
}

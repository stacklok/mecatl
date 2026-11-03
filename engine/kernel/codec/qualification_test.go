package codec_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture/value"
)

// Inputs and policies are built outside measurement. Check bytes, not just the
// number of allocations: one rejected declaration must not buy a huge buffer.
func requireAllocatedBytes(t *testing.T, maximum int64, call func() error, want error) {
	t.Helper()
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if err := call(); !errors.Is(err, want) {
				b.Fatalf("got %v, want %v", err, want)
			}
		}
	})
	t.Logf("allocated %d bytes/op (maximum %d)", result.AllocedBytesPerOp(), maximum)
	if got := result.AllocedBytesPerOp(); got > maximum {
		t.Fatalf("allocated %d bytes/op, maximum %d", got, maximum)
	}
}

func TestQualificationFixedArrayRejectsBeforeCopy(t *testing.T) {
	// Fixed is [3]Octet. A globally admitted, fully available but wrong length
	// must fail before an owning temporary byte slice is allocated/copied.
	const size = 1 << 18
	prefix := unhex(t, strings.Split(sampleZeroHex, "43000000")[0])
	data := append(prefix, 0x5a, 0, 4, 0, 0)
	data = append(data, make([]byte, size)...)
	l := generatedLimits()
	l.MaxByteStringBytes = size
	requireAllocatedBytes(t, 2048, func() error {
		v, err := codec.Decode[fixture.Sample](data, l)
		if !reflect.DeepEqual(v, fixture.Sample{}) {
			return errors.New("partial decoded output")
		}
		return err
	}, codec.ErrInvalidData)
}

func TestQualificationNestedReservationsAllocatedBytes(t *testing.T) {
	// Root's first array is empty; entries has one key whose value declares a
	// large slice of *Detail. Supply minimum wire bytes, so only aggregate/node
	// admission (not a truncation shortcut) prevents typed slice allocation.
	data := unhex(t, "a4656e616d65738067656e7472696573a16178991900")
	data = append(data, make([]byte, 6400)...)
	for _, dimension := range []string{"nodes", "aggregate", "depth"} {
		t.Run(dimension, func(t *testing.T) {
			l := generatedLimits()
			l.MaxArrayElements = 10000
			switch dimension {
			case "nodes":
				l.MaxNodes = 30
			case "aggregate":
				l.MaxCollectionElements = 20
			case "depth":
				l.MaxDepth = 1
			}
			requireAllocatedBytes(t, 2048, func() error {
				_, err := codec.Decode[fixture.Catalog](data, l)
				return err
			}, codec.ErrLimitExceeded)
		})
	}
	// Also measure the existing large-map declaration rejection, previously
	// checked for error class only, before allocating its typed map buckets.
	data = unhex(t, "a4656e616d65738067656e7472696573b91900")
	data = append(data, make([]byte, 12800)...)
	l := generatedLimits()
	l.MaxMapPairs, l.MaxNodes = 10000, 30
	requireAllocatedBytes(t, 2048, func() error {
		_, err := codec.Decode[fixture.Catalog](data, l)
		return err
	}, codec.ErrLimitExceeded)
}

func TestQualificationKnownArrayAdmissionBytes(t *testing.T) {
	v := richCatalog()
	v.Names = make([]string, 1<<16)
	l := generatedLimits()
	// Admit the collection through every global and aggregate policy. The literal
	// field cap must be the rejection that prevents an element scan, output
	// allocation, or decode slice allocation.
	l.MaxArrayElements = len(v.Names)
	l.MaxMapPairs = len(v.Names)
	l.MaxCollectionElements = len(v.Names) + 128
	l.MaxNodes = 2*len(v.Names) + 128
	l.MaxBytes = 1 << 22
	data := append(unhex(t, "a4656e616d65739a00010000"), bytes.Repeat([]byte{0x60}, len(v.Names))...)
	data = append(data, unhex(t, "67656e7472696573a0677061796c6f61644068726571756972656440")...)
	for _, op := range []struct {
		name string
		call func() error
	}{
		{"Check", func() error { return codec.Check[fixture.Catalog](v, l) }},
		{"Encode", func() error { _, err := codec.Encode[fixture.Catalog](v, l); return err }},
		{"Decode", func() error {
			got, err := codec.Decode[fixture.Catalog](data, l)
			if !reflect.DeepEqual(got, fixture.Catalog{}) {
				return errors.New("partial decoded output")
			}
			return err
		}},
	} {
		t.Run(op.name, func(t *testing.T) {
			requireAllocatedBytes(t, 2048, op.call, codec.ErrLimitExceeded)
		})
	}
	// Invalid text is a scan sentinel: field admission must reject before UTF-8
	// traversal, not merely before encoding output or decode allocation.
	v.Names[0] = "\xff"
	if err := codec.Check[fixture.Catalog](v, l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("literal budget did not precede element scan: %v", err)
	}
}

func TestQualificationNamedMapAdmissionBytes(t *testing.T) {
	const count = 10000
	v := richCatalog()
	v.Entries = make(map[value.Key][]*value.Detail, count)
	for i := 0; i < count; i++ {
		v.Entries[value.Key(fmt.Sprintf("%05d", i))] = nil
	}
	l := generatedLimits()
	// All generic limits admit the collection; only the literal field budget is
	// tight. The decode payload supplies every declared pair so rejection cannot
	// be attributed to truncated input.
	l.MaxArrayElements = count
	l.MaxMapPairs = count
	l.MaxCollectionElements = count + 128
	l.MaxNodes = 2*count + 128
	l.MaxBytes = 1 << 22
	data := unhex(t, "a4656e616d65738067656e7472696573b92710")
	for i := 0; i < count; i++ {
		data = append(data, 0x65)
		data = append(data, fmt.Sprintf("%05d", i)...)
		data = append(data, 0x80)
	}
	data = append(data, unhex(t, "677061796c6f61644068726571756972656440")...)
	for _, op := range []struct {
		name string
		call func() error
	}{
		{"Check", func() error { return codec.Check[fixture.Catalog](v, l) }},
		{"Encode", func() error { _, err := codec.Encode[fixture.Catalog](v, l); return err }},
		{"Decode", func() error {
			got, err := codec.Decode[fixture.Catalog](data, l)
			if !reflect.DeepEqual(got, fixture.Catalog{}) {
				return errors.New("partial decoded output")
			}
			return err
		}},
	} {
		t.Run(op.name, func(t *testing.T) {
			requireAllocatedBytes(t, 2048, op.call, codec.ErrLimitExceeded)
		})
	}
	// Reducing the immediate value to the literal boundary is the positive control.
	for i := 4096; i < count; i++ {
		delete(v.Entries, value.Key(fmt.Sprintf("%05d", i)))
	}
	v.Cache = nil
	data = v.AppendCBOR(nil)
	if err := codec.Check[fixture.Catalog](v, l); err != nil {
		t.Fatal("admitted literal policy Check", err)
	}
	if _, err := codec.Encode[fixture.Catalog](v, l); err != nil {
		t.Fatal("admitted literal policy Encode", err)
	}
	if _, err := codec.Decode[fixture.Catalog](data, l); err != nil {
		t.Fatal("admitted literal policy Decode", err)
	}
}

func TestQualificationMapSortingAdmissionBytes(t *testing.T) {
	v := richCatalog()
	v.Cache = nil
	v.Entries = make(map[value.Key][]*value.Detail, 4096)
	for i := 0; i < 4096; i++ {
		v.Entries[value.Key(fmt.Sprintf("long-data-key-%04d", i))] = nil
	}
	l := generatedLimits()
	l.MaxMapPairs = 4096
	l.MaxNodes, l.MaxCollectionElements, l.MaxBytes = 30000, 30000, 1<<22
	// Count is admitted; invalid key byte length must still reject BEFORE
	// encode output and sorting scratch, not merely before map count admission.
	l.MaxStringBytes = 8
	requireAllocatedBytes(t, 2048, func() error {
		_, err := codec.Encode[fixture.Catalog](v, l)
		return err
	}, codec.ErrLimitExceeded)
	l.MaxStringBytes = 1024
	// A positive control measures the complete admitted encoder, including the
	// sorting scratch. This generous fixture-specific bound covers admitted
	// entries and output; it is not a universal heap/RSS or performance promise.
	requireAllocatedBytes(t, 1<<20, func() error {
		_, err := codec.Encode[fixture.Catalog](v, l)
		return err
	}, nil)
	data, err := codec.Encode[fixture.Catalog](v, l)
	if err != nil {
		t.Fatal(err)
	}
	w, err := codec.Decode[fixture.Catalog](data, l)
	if err != nil || !w.Equal(v) {
		t.Fatal("admitted sorting control", err)
	}
}

func TestQualificationTinyStringsOwnNearCapInput(t *testing.T) {
	v := richCatalog()
	v.Cache = nil
	v.Payload = make(value.Blob, 1<<18)
	l := generatedLimits()
	l.MaxByteStringBytes = len(v.Payload)
	data, err := codec.Encode[fixture.Catalog](v, l)
	if err != nil {
		t.Fatal(err)
	}
	l.MaxBytes = len(data) + 1024 // near-cap complete input, not a tiny reader prefix
	w, err := codec.Decode[fixture.Catalog](data, l)
	if err != nil {
		t.Fatal(err)
	}
	start := uintptr(unsafe.Pointer(unsafe.SliceData(data)))
	check := func(s string) {
		t.Helper()
		p := uintptr(unsafe.Pointer(unsafe.StringData(s)))
		if p >= start && p-start < uintptr(len(data)) {
			t.Fatal("tiny string retains input backing storage")
		}
	}
	check(w.Names[0])
	check((*w.Optional)[0])
	for key := range w.Entries {
		check(string(key))
	}
	runtime.KeepAlive(data)
}

func TestQualificationLiteralPointerAndOptionalGlobalSymmetry(t *testing.T) {
	v := richCatalog()
	v.Cache = nil
	*v.Required = make(value.Blob, 8)
	data, err := codec.Encode[fixture.Catalog](v, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode[fixture.Catalog](data, generatedLimits()); err != nil {
		t.Fatal(err)
	}
	*v.Required = make(value.Blob, 9)
	raw := bytes.Replace(data, append([]byte{0x48}, make([]byte, 8)...), append([]byte{0x49}, make([]byte, 9)...), 1)
	assertCatalogRejected(t, v, raw, generatedLimits(), codec.ErrLimitExceeded)
	v = richCatalog()
	v.Cache = nil
	*v.Optional = []string{"a", "b", "c"}
	data, err = codec.Encode[fixture.Catalog](v, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	l := generatedLimits()
	l.MaxArrayElements = 2 // stricter than optional's literal cap of four
	assertCatalogRejected(t, v, data, l, codec.ErrLimitExceeded)
}

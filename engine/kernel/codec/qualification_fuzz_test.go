package codec_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
)

func seedQualification(f *testing.F, canonical ...string) {
	f.Helper()
	for _, text := range append(canonical,
		"", "f6", "bfff", "a16178", "b800", "bbffffffffffffffff",
		"a4656e616d65739bffffffffffffffff",                   // array declaration overflow
		"a4656e616d65738067656e7472696573bbffffffffffffffff", // map declaration overflow
		"a4656e616d65738067656e7472696573a16178991900",       // nested prefix
	) {
		data, err := hex.DecodeString(text)
		if err != nil {
			f.Fatal(err)
		}
		for mode := uint8(0); mode < 6; mode++ {
			f.Add(data, mode)
		}
	}
}

func fuzzQualification[T any](t *testing.T, data []byte, mode uint8,
	decode func([]byte, codec.Limits) (T, error), check func(T, codec.Limits) error,
	encode func(T, codec.Limits) ([]byte, error), clone func(T) T, equal func(T, T) bool,
) {
	t.Helper()
	l := generatedLimits()
	switch mode % 6 {
	case 1:
		l.MaxBytes = 64
	case 2:
		l.MaxNodes, l.MaxCollectionElements = 12, 5
	case 3:
		l.Named["detail"] = 0 // referenced even when nested entries are absent
	case 4:
		l.Named["payload"], l.MaxArrayElements, l.MaxByteStringBytes = 1, 1, 3
	case 5:
		l.MaxDepth = 1
	}
	// Oversized fuzz inputs exercise the cheap admission boundary without a
	// test-owned whole-input clone or repeated expensive property checks.
	if len(data) > l.MaxBytes {
		_, err := decode(data, l)
		if !errors.Is(err, codec.ErrLimitExceeded) && !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal("oversized input", err)
		}
		return
	}
	before := bytes.Clone(data)
	v, err := decode(data, l)
	if !bytes.Equal(before, data) {
		t.Fatal("decode mutated input")
	}
	if err != nil {
		var zero T
		if !reflect.DeepEqual(v, zero) {
			t.Fatal("partial failure output")
		}
		if !errors.Is(err, codec.ErrInvalidData) && !errors.Is(err, codec.ErrLimitExceeded) && !errors.Is(err, codec.ErrInvalidLimits) {
			t.Fatal("unclassified error", err)
		}
		return
	}
	if err := check(v, l); err != nil {
		t.Fatal("decode/check asymmetry", err)
	}
	wire, err := encode(v, l)
	if err != nil || !bytes.Equal(wire, data) {
		t.Fatalf("noncanonical acceptance or policy asymmetry: %x => %x (%v)", data, wire, err)
	}
	w := clone(v)
	if !equal(v, w) || !equal(w, v) {
		t.Fatal("clone/equal asymmetry")
	}
	if !bytes.Equal(before, data) {
		t.Fatal("generated operations mutated input")
	}
}

func FuzzSamplePersistence(f *testing.F) {
	seedQualification(f, sampleZeroHex, sampleExtremaHex, sampleMaxSignedHex,
		// Wrong fixed length and nonminimal integer, independently from canonical seeds.
		"ab6269381800626f6bf46275380063693136006369333200636936340063753136006375333200637536340065666978656443000000656c6162656c60",
		"ab62693800626f6bf462753800636931360063693332006369363400637531360063753332006375363400656669786564420000656c6162656c60")
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		fuzzQualification(t, data, mode, fixture.DecodeSample, fixture.CheckSample, fixture.EncodeSample, fixture.Sample.Clone, fixture.Sample.Equal)
	})
}

func FuzzCatalogPersistence(f *testing.F) {
	seedQualification(f, catalogEmptyHex, catalogNestedHex,
		// Duplicate data keys, unknown field, and null pointer element.
		"a4656e616d65738067656e7472696573a2616180616180677061796c6f61644068726571756972656440",
		"a165657874726100",
		"a4656e616d65738067656e7472696573a1616181f6677061796c6f61644068726571756972656440")
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		fuzzQualification(t, data, mode, fixture.DecodeCatalog, fixture.CheckCatalog, fixture.EncodeCatalog, fixture.Catalog.Clone, fixture.Catalog.Equal)
	})
}

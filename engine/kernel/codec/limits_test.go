package codec

import (
	"errors"
	"strconv"
	"testing"
)

func testLimits() Limits {
	return Limits{
		MaxBytes: 4096, MaxStringBytes: 1024, MaxByteStringBytes: 1024,
		MaxArrayElements: 128, MaxMapPairs: 128, MaxDepth: 8,
		MaxNodes: 256, MaxCollectionElements: 256,
	}
}

func requireError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestLimitsInvalidFirst(t *testing.T) {
	fields := []struct {
		name string
		set  func(*Limits, int)
	}{
		{"bytes", func(l *Limits, v int) { l.MaxBytes = v }},
		{"string", func(l *Limits, v int) { l.MaxStringBytes = v }},
		{"byte string", func(l *Limits, v int) { l.MaxByteStringBytes = v }},
		{"array", func(l *Limits, v int) { l.MaxArrayElements = v }},
		{"map", func(l *Limits, v int) { l.MaxMapPairs = v }},
		{"depth", func(l *Limits, v int) { l.MaxDepth = v }},
		{"nodes", func(l *Limits, v int) { l.MaxNodes = v }},
		{"elements", func(l *Limits, v int) { l.MaxCollectionElements = v }},
	}
	for _, field := range fields {
		for _, bad := range []int{0, -1} {
			t.Run(field.name+"/"+strconv.Itoa(bad), func(t *testing.T) {
				l := testLimits()
				field.set(&l, bad)
				_, err := NewBudget(l)
				requireError(t, err, ErrInvalidLimits)
				// Input is malformed and may exceed MaxBytes, but policy wins.
				_, err = NewReader([]byte{0xff, 0xff}, l)
				requireError(t, err, ErrInvalidLimits)
			})
		}
	}
	_, err := NewReader(nil, Limits{})
	requireError(t, err, ErrInvalidLimits)
	var b Budget
	requireError(t, b.CheckScalar(0), ErrInvalidLimits)
	requireError(t, b.CheckString("bad", 0, 1), ErrInvalidLimits)
	requireError(t, b.CheckArray(0, 0, 1), ErrInvalidLimits)
	var r Reader
	_, err = r.Int(0)
	requireError(t, err, ErrInvalidLimits)
	_, err = r.Bool(0)
	requireError(t, err, ErrInvalidLimits)
	requireError(t, r.Done(), ErrInvalidLimits)
}

func TestNilAndZeroSharedState(t *testing.T) {
	for _, b := range []*Budget{nil, {}} {
		requireError(t, b.CheckScalar(0), ErrInvalidLimits)
		requireError(t, b.CheckString("x", 0, 1), ErrInvalidLimits)
		requireError(t, b.CheckBytes(0, 0, 1), ErrInvalidLimits)
		requireError(t, b.CheckArray(0, 0, 1), ErrInvalidLimits)
		requireError(t, b.CheckMap(0, 0, 1), ErrInvalidLimits)
		requireError(t, b.CheckCIDLink(0), ErrInvalidLimits)
		if b.EstimatedBytes() != 0 {
			t.Fatal("invalid budget has nonzero estimate")
		}
	}
	for _, r := range []*Reader{nil, {}} {
		b, err := r.Bool(0)
		requireError(t, err, ErrInvalidLimits)
		i, err := r.Int(0)
		requireError(t, err, ErrInvalidLimits)
		u, err := r.Uint(0)
		requireError(t, err, ErrInvalidLimits)
		s, err := r.String(0, 1)
		requireError(t, err, ErrInvalidLimits)
		v, err := r.Bytes(0, 1)
		requireError(t, err, ErrInvalidLimits)
		f, err := r.FixedBytes(0, 1, 0)
		requireError(t, err, ErrInvalidLimits)
		a, err := r.Array(0, 1)
		requireError(t, err, ErrInvalidLimits)
		m, err := r.Map(0, 1)
		requireError(t, err, ErrInvalidLimits)
		c, err := r.CIDLink(0)
		requireError(t, err, ErrInvalidLimits)
		requireError(t, r.Done(), ErrInvalidLimits)
		if b || i != 0 || u != 0 || s != "" || v != nil || f != nil || a != 0 || m != 0 || c != ([36]byte{}) {
			t.Fatal("invalid reader returned nonzero output")
		}
	}
}

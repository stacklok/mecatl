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

func TestNamedLimits(t *testing.T) {
	l := testLimits()
	for _, named := range []map[string]int{nil, {}, {"cap": 0}, {"cap": -1}, {"Cap": 7}} {
		l.Named = named
		_, err := l.NamedLimit("cap")
		requireError(t, err, ErrInvalidLimits)
	}
	l.Named = map[string]int{"cap": 7, "unreferenced": -10, "": 0}
	got, err := l.NamedLimit("cap")
	if err != nil || got != 7 {
		t.Fatalf("lookup = %d, %v", got, err)
	}
	b, err := NewBudget(l)
	if err != nil || b.limits.Named != nil {
		t.Fatalf("budget retained dictionary: %v", err)
	}
	r, err := NewReader([]byte{0xf4}, l)
	if err != nil || r.budget.limits.Named != nil {
		t.Fatalf("reader retained dictionary: %v", err)
	}
	if len(l.Named) != 3 || l.Named["cap"] != 7 {
		t.Fatal("policy mutated")
	}
	// Generated callers must do this once even when an optional field is absent.
	_, err = l.NamedLimit("absent_optional_cap")
	requireError(t, err, ErrInvalidLimits)
}

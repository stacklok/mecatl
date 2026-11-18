package format_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func usageFixture() format.Usage {
	return format.Usage{
		ID:         1,
		Definition: &format.UsageDefinition{Purpose: " Future Purpose ", RunID: new(uint64(2)), RequestID: new(uint64(3))},
		Report: format.UsageReport{State: format.UsageReportActive, Buckets: []format.UsageBucket{
			{SelectedRoute: new(" Gateway "), SelectedModel: new("Selected/Model"), Counters: format.UsageCounters{Input: new(int64(0)), Output: new(int64(math.MaxInt64)), CacheRead: new(int64(7)), CacheWrite: new(int64(8)), Reasoning: new(int64(9))}},
			{}, // Unknown bucket, retained in order; no equations or deduplication.
		}, Raw: &format.OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{1, 2, 3}}},
	}
}

func TestUsageRoundTripAndPresence(t *testing.T) {
	v := usageFixture()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.Usage](wire, limits())
	if err != nil || !got.Equal(v) || !reflect.DeepEqual(got, v) {
		t.Fatalf("exact round trip: %v", err)
	}
	*v.Definition.RunID = 99
	*v.Report.Buckets[0].SelectedRoute = "changed"
	v.Report.Raw.Data[0] = 99
	if !got.Equal(usageFixture()) {
		t.Fatal("decode aliased input")
	}
	zero := got.Clone()
	zero.Report.Buckets[0].Counters.Input = nil
	if zero.Equal(got) {
		t.Fatal("reported zero equaled unknown")
	}
	for _, bucket := range []format.UsageBucket{
		{},
		{Counters: format.UsageCounters{Reasoning: new(int64(20))}},
		{SelectedRoute: new("route")},
		{SelectedModel: new("model")},
		{SelectedRoute: new("route"), SelectedModel: new("model")},
	} {
		v := format.Usage{ID: 1, Report: format.UsageReport{State: format.UsageReportActive, Buckets: []format.UsageBucket{bucket, bucket}}}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		if got, err := codec.Decode[format.Usage](wire, limits()); err != nil || !got.Equal(v) {
			t.Fatalf("independent selected labels roundtrip: %v", err)
		}
	}
	for _, state := range []format.UsageReportState{format.UsageReportActive, format.UsageReportWithdrawn} {
		v := format.Usage{ID: 1, Report: format.UsageReport{State: state}}
		wire, err := codec.Encode(v, limits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.Usage](wire, limits())
		if err != nil || !got.Equal(v) {
			t.Fatalf("empty report: %v", err)
		}
		v.Report.Buckets = []format.UsageBucket{}
		if !got.Equal(v) {
			t.Fatal("nil/empty buckets differ")
		}
	}
	v = usageFixture()
	v.Report.Buckets = nil // Raw-only first report is valid.
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	activeZero := format.Usage{ID: 1, Report: format.UsageReport{State: format.UsageReportActive, Buckets: []format.UsageBucket{{Counters: format.UsageCounters{Input: new(int64(0))}}}}}
	if err := activeZero.Validate(); err != nil {
		t.Fatalf("active report with explicit zero: %v", err)
	}
}

func TestUsageZeroInvalid(t *testing.T) {
	var zero format.Usage
	if zero.Validate() == nil {
		t.Fatal("zero Usage validated")
	}
	if out, err := codec.Encode(zero, limits()); !errors.Is(err, codec.ErrInvalidData) || out != nil {
		t.Fatalf("zero Usage encode: %v", err)
	}
	wire := zero.AppendCBOR(nil)
	if got, err := codec.Decode[format.Usage](wire, limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.Usage{}) {
		t.Fatalf("zero Usage decode: %v", err)
	}
}

func TestUsageLocalValidation(t *testing.T) {
	badUTF8 := string([]byte{0xff})
	for name, edit := range map[string]func(*format.Usage){
		"zero ID":             func(v *format.Usage) { v.ID = 0 },
		"empty purpose":       func(v *format.Usage) { v.Definition.Purpose = "" },
		"invalid purpose":     func(v *format.Usage) { v.Definition.Purpose = badUTF8 },
		"zero run":            func(v *format.Usage) { v.Definition.RunID = new(uint64(0)) },
		"zero request":        func(v *format.Usage) { v.Definition.RequestID = new(uint64(0)) },
		"request without run": func(v *format.Usage) { v.Definition.RunID = nil },
		"zero state":          func(v *format.Usage) { v.Report.State = "" },
		"unknown state":       func(v *format.Usage) { v.Report.State = "final" },
		"first withdrawn":     func(v *format.Usage) { v.Report = format.UsageReport{State: format.UsageReportWithdrawn} },
		"withdrawn buckets": func(v *format.Usage) {
			v.Definition = nil
			v.Report.State = format.UsageReportWithdrawn
			v.Report.Raw = nil
		},
		"withdrawn raw": func(v *format.Usage) {
			v.Definition = nil
			v.Report.State = format.UsageReportWithdrawn
			v.Report.Buckets = nil
		},
		"negative input":         func(v *format.Usage) { v.Report.Buckets[0].Counters.Input = new(int64(-1)) },
		"negative output":        func(v *format.Usage) { v.Report.Buckets[0].Counters.Output = new(int64(-1)) },
		"negative cache read":    func(v *format.Usage) { v.Report.Buckets[0].Counters.CacheRead = new(int64(-1)) },
		"negative cache write":   func(v *format.Usage) { v.Report.Buckets[0].Counters.CacheWrite = new(int64(-1)) },
		"negative reasoning":     func(v *format.Usage) { v.Report.Buckets[0].Counters.Reasoning = new(int64(-1)) },
		"empty selected route":   func(v *format.Usage) { v.Report.Buckets[0].SelectedRoute = new("") },
		"invalid selected route": func(v *format.Usage) { v.Report.Buckets[0].SelectedRoute = &badUTF8 },
		"empty selected model":   func(v *format.Usage) { v.Report.Buckets[0].SelectedModel = new("") },
		"invalid selected model": func(v *format.Usage) { v.Report.Buckets[0].SelectedModel = &badUTF8 },
		"invalid raw":            func(v *format.Usage) { v.Report.Raw.Version = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			v := usageFixture()
			edit(&v)
			if v.Validate() == nil {
				t.Fatal("Validate accepted invalid payload")
			}
			if out, err := codec.Encode(v, limits()); !errors.Is(err, codec.ErrInvalidData) || out != nil {
				t.Fatalf("encode: %v", err)
			}
			// Bypass checking to exercise the independent decode boundary.
			wire := v.AppendCBOR(nil)
			got, err := codec.Decode[format.Usage](wire, limits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.Usage{}) {
				t.Fatalf("decode: %v", err)
			}
			receiver := usageFixture()
			before := receiver.Clone()
			r, err := codec.NewReader(wire, limits())
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.ReadCBOR(&r, 0, math.MaxInt); !errors.Is(err, codec.ErrInvalidData) || !receiver.Equal(before) {
				t.Fatalf("failed read changed receiver: %v", err)
			}
		})
	}
	for _, def := range []format.UsageDefinition{
		{Purpose: " "},
		{Purpose: "未来", RunID: new(uint64(1))},
	} {
		if err := def.Validate(); err != nil {
			t.Fatalf("open exact purpose/run-only correlation: %v", err)
		}
	}
}

func TestUsageCodecBoundsAndMalformed(t *testing.T) {
	v := usageFixture()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*codec.Limits){
		"bytes":       func(l *codec.Limits) { l.MaxBytes = len(wire) - 1 },
		"string":      func(l *codec.Limits) { l.MaxStringBytes = 3 },
		"raw":         func(l *codec.Limits) { l.MaxByteStringBytes = 2 },
		"buckets":     func(l *codec.Limits) { l.MaxArrayElements = 1 },
		"map":         func(l *codec.Limits) { l.MaxMapPairs = 2 },
		"depth":       func(l *codec.Limits) { l.MaxDepth = 2 },
		"nodes":       func(l *codec.Limits) { l.MaxNodes = 10 },
		"collections": func(l *codec.Limits) { l.MaxCollectionElements = 5 },
	} {
		t.Run(name, func(t *testing.T) {
			l := limits()
			edit(&l)
			if out, err := codec.Encode(v, l); !errors.Is(err, codec.ErrLimitExceeded) || out != nil {
				t.Fatalf("encode bound: %v", err)
			}
			if got, err := codec.Decode[format.Usage](wire, l); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(got, format.Usage{}) {
				t.Fatalf("decode bound: %v", err)
			}
		})
	}
	minimal := format.Usage{ID: 1, Report: format.UsageReport{State: format.UsageReportActive}}
	want := []byte("\xa2\x61o\x01\x61r\xa2\x61b\x80\x61s\x66active")
	if wire, err := codec.Encode(minimal, limits()); err != nil || !bytes.Equal(wire, want) {
		t.Fatalf("minimal wire %x: %v", wire, err)
	}
	for _, malformed := range [][]byte{
		{}, wire[:len(wire)-1], append(bytes.Clone(wire), 0),
		[]byte("\xa0"),                   // Missing required fields.
		[]byte("\xa2\x61o\x01\x61o\x01"), // Duplicate key.
		[]byte("\xa1\x61z\x01"),          // Unknown key.
		[]byte("\xa2\x61o\x00\x61r\xa2\x61b\x80\x61s\x66active"),
		[]byte("\xa2\x61o\x01\x61r\xa2\x61b\x80\x61s\x66future"),
	} {
		if got, err := codec.Decode[format.Usage](malformed, limits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.Usage{}) {
			t.Fatalf("malformed decode %x: %v", malformed, err)
		}
	}
}

func TestUsageCloneEqualOwnership(t *testing.T) {
	for name, mutate := range map[string]func(*format.Usage){
		"ID":         func(v *format.Usage) { v.ID++ },
		"definition": func(v *format.Usage) { v.Definition = nil },
		"purpose":    func(v *format.Usage) { v.Definition.Purpose = "other" },
		"run":        func(v *format.Usage) { *v.Definition.RunID++ },
		"request":    func(v *format.Usage) { *v.Definition.RequestID++ },
		"state":      func(v *format.Usage) { v.Report.State = format.UsageReportWithdrawn },
		"order": func(v *format.Usage) {
			v.Report.Buckets[0], v.Report.Buckets[1] = v.Report.Buckets[1], v.Report.Buckets[0]
		},
		"selected route": func(v *format.Usage) { *v.Report.Buckets[0].SelectedRoute = "changed" },
		"selected model": func(v *format.Usage) { *v.Report.Buckets[0].SelectedModel = "changed" },
		"input":          func(v *format.Usage) { *v.Report.Buckets[0].Counters.Input++ },
		"output":         func(v *format.Usage) { *v.Report.Buckets[0].Counters.Output-- },
		"cache read":     func(v *format.Usage) { *v.Report.Buckets[0].Counters.CacheRead++ },
		"cache write":    func(v *format.Usage) { *v.Report.Buckets[0].Counters.CacheWrite++ },
		"reasoning":      func(v *format.Usage) { *v.Report.Buckets[0].Counters.Reasoning++ },
		"raw bytes":      func(v *format.Usage) { v.Report.Raw.Data[0]++ },
		"raw metadata":   func(v *format.Usage) { v.Report.Raw.Producer = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			v := usageFixture()
			clone := v.Clone()
			if !clone.Equal(v) {
				t.Fatal("clone not equal")
			}
			mutate(&clone)
			if clone.Equal(v) || !v.Equal(usageFixture()) {
				t.Fatal("equality missed mutation or clone retained mutable backing")
			}
		})
	}
}

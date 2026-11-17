package session

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// These tests intentionally construct DTO-backed handles directly: aggregate
// materialization, Submit and Load are deferred until the contract is reviewed.
func usageViewFixture(t *testing.T) UsageView {
	t.Helper()
	v := sessionformat.Usage{ID: 7, Definition: &sessionformat.UsageDefinition{Purpose: " Future ", Granularity: UsageGranularityAggregate, RunID: new(uint64(2)), RequestID: new(uint64(3))}, Report: UsageReport{State: UsageReportActive, Buckets: []UsageBucket{
		{Attribution: UsageAttribution{
			Selected: &UsageSelectedIdentity{Provider: new(" Route "), Model: new("Selected")},
			Observed: &UsageObservedIdentity{Provider: new("Provider"), Model: new("Observed"), Source: " API "},
			Charging: &UsageChargingService{Label: "Service", Source: "Meter"},
		}, Counters: UsageCounters{Input: new(int64(0)), Output: new(int64(1)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4))}},
		{Attribution: UsageAttribution{Selected: &UsageSelectedIdentity{Model: new(string(bytes.Repeat([]byte("m"), 8192)))}}},
	}, Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: bytes.Repeat([]byte{0x80}, 8192)}}}
	// Exercise the same bounded generated detachment that materialization will use.
	limits := codec.Limits{MaxBytes: 1 << 20, MaxStringBytes: 1 << 16, MaxByteStringBytes: 1 << 16, MaxArrayElements: 1024, MaxMapPairs: 64, MaxDepth: 32, MaxNodes: 10000, MaxCollectionElements: 10000}
	wire, err := codec.Encode(v, limits)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := codec.Decode[sessionformat.Usage](wire, limits)
	if err != nil {
		t.Fatal(err)
	}
	return UsageView{scope: historyid.NewHistoryID(), value: &owned}
}

func TestUsageViewScopedTraversalAndOwnership(t *testing.T) {
	v := usageViewFixture(t)
	if v.ID().HistoryID() != v.scope || v.ID().HistoryRelative() != 7 {
		t.Fatal("usage scope lost")
	}
	def, ok := v.Definition()
	if !ok || def.value != v.value.Definition || def.Purpose() != " Future " || def.Granularity() != UsageGranularityAggregate {
		t.Fatal("definition navigation")
	}
	run, ok := def.RunID()
	if !ok || run.HistoryID() != v.scope || run.HistoryRelative() != 2 {
		t.Fatal("run scope lost")
	}
	request, ok := def.RequestID()
	if !ok || request.RunID() != run || request.RunRelative() != 3 {
		t.Fatal("request scope lost")
	}
	report := v.Report()
	if report.value != &v.value.Report || report.State() != UsageReportActive || report.Buckets().Len() != 2 {
		t.Fatal("report navigation copied backing")
	}
	for pass := 0; pass < 2; pass++ {
		count := 0
		for i, bucket := range report.Buckets().All() {
			count++
			if bucket.value != &v.value.Report.Buckets[i] {
				t.Fatal("bucket navigation copied backing")
			}
			attribution := bucket.Attribution()
			selected, ok := attribution.Selected()
			if !ok || selected.value != v.value.Report.Buckets[i].Attribution.Selected {
				t.Fatal("selected assertion copied backing")
			}
			if _, ok := selected.Model(); !ok {
				t.Fatal("model absent")
			}
			if i == 1 {
				if _, ok := selected.Provider(); ok {
					t.Fatal("partial identity filled from sibling")
				}
				c := bucket.Counters()
				for _, get := range []func() (int64, bool){c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning} {
					if n, ok := get(); ok || n != 0 {
						t.Fatal("unknown counter on retained backing reported as present")
					}
				}
				continue
			}
			if provider, ok := selected.Provider(); !ok || provider != " Route " {
				t.Fatal("selected provider normalized")
			}
			observed, ok := attribution.Observed()
			provider, providerOK := observed.Provider()
			model, modelOK := observed.Model()
			if !ok || !providerOK || provider != "Provider" || !modelOK || model != "Observed" || observed.Source() != " API " {
				t.Fatal("observed identity lost")
			}
			charging, ok := attribution.Charging()
			if !ok || charging.Label() != "Service" || charging.Source() != "Meter" {
				t.Fatal("charging assertion lost")
			}
			c := bucket.Counters()
			for want, get := range []func() (int64, bool){c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning} {
				if n, ok := get(); !ok || n != int64(want) {
					t.Fatal("counter presence/value lost")
				}
			}
		}
		if count != 2 {
			t.Fatal("repeat traversal")
		}
	}
	raw, ok := report.Raw()
	if !ok || raw.Producer() != "p" || raw.Format() != "usage" || raw.Version() != 1 || raw.Data().Len() != 8192 {
		t.Fatal("raw evidence navigation")
	}
	before := v.MutableClone()
	copyBytes := raw.Data().Copy()
	clear(copyBytes)
	clone := v.MutableClone()
	*clone.Definition.RunID = 99
	*clone.Report.Buckets[0].Counters.Input = 99
	*clone.Report.Buckets[0].Attribution.Selected.Provider = "changed"
	clear(clone.Report.Raw.Data)
	definitionClone := def.MutableClone()
	*definitionClone.RequestID = 99
	reportClone := report.MutableClone()
	clear(reportClone.Raw.Data)
	bucketClone := report.Buckets().At(0).MutableClone()
	*bucketClone.Counters.Output = 99
	if !v.value.Equal(before) {
		t.Fatal("mutable clones or byte Copy retained backing")
	}
	// A later report cannot fill definition metadata by accidental context reuse.
	update := UsageView{scope: v.scope, value: &sessionformat.Usage{ID: 7, Report: UsageReport{State: UsageReportWithdrawn}}}
	if d, ok := update.Definition(); ok || d != (UsageDefinitionView{}) || update.ID() != v.ID() {
		t.Fatal("replacement invented definition or lost identity")
	}
}

func TestUsageViewsZeroAndAbsent(t *testing.T) {
	var v UsageView
	if v.ID() != (UsageID{}) || v.Report().State() != "" || v.Report().Buckets().Len() != 0 {
		t.Fatal("zero record/report")
	}
	def, ok := v.Definition()
	if ok || def != (UsageDefinitionView{}) || def.Purpose() != "" || def.Granularity() != "" {
		t.Fatal("zero definition")
	}
	if run, ok := def.RunID(); ok || run != (RunID{}) {
		t.Fatal("zero run")
	}
	if request, ok := def.RequestID(); ok || request != (ModelRequestID{}) {
		t.Fatal("zero request")
	}
	if raw, ok := v.Report().Raw(); ok || raw != (OpaqueProviderDataView{}) {
		t.Fatal("zero raw")
	}
	bucket := UsageBucketView{}
	counters := bucket.Counters()
	for _, get := range []func() (int64, bool){counters.Input, counters.Output, counters.CacheRead, counters.CacheWrite, counters.Reasoning} {
		if n, ok := get(); ok || n != 0 {
			t.Fatal("zero counter")
		}
	}
	a := bucket.Attribution()
	selected, selectedOK := a.Selected()
	observed, observedOK := a.Observed()
	charging, chargingOK := a.Charging()
	if selectedOK || observedOK || chargingOK || selected != (UsageSelectedIdentityView{}) || observed != (UsageObservedIdentityView{}) || charging != (UsageChargingServiceView{}) {
		t.Fatal("zero attribution")
	}
	for _, get := range []func() (string, bool){selected.Provider, selected.Model, observed.Provider, observed.Model} {
		if s, ok := get(); ok || s != "" {
			t.Fatal("zero label")
		}
	}
	if observed.Source() != "" || charging.Label() != "" || charging.Source() != "" {
		t.Fatal("zero source/charging")
	}
	for _, pair := range [][2]any{
		{v.MutableClone(), sessionformat.Usage{}}, {def.MutableClone(), sessionformat.UsageDefinition{}},
		{v.Report().MutableClone(), UsageReport{}}, {bucket.MutableClone(), UsageBucket{}},
		{counters.MutableClone(), UsageCounters{}}, {a.MutableClone(), UsageAttribution{}},
		{selected.MutableClone(), UsageSelectedIdentity{}}, {observed.MutableClone(), UsageObservedIdentity{}},
		{charging.MutableClone(), UsageChargingService{}},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Fatalf("zero clone: %T", pair[0])
		}
	}
	// Present-empty evidence bytes remain present even though there are no buckets.
	r := UsageReportView{value: &UsageReport{State: UsageReportActive, Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{}}}}
	if raw, ok := r.Raw(); !ok || raw.Data().Len() != 0 {
		t.Fatal("present empty raw lost")
	}
}

var usageViewSink UsageView
var usageReadSink int64
var usageByteSink []byte

func TestUsageViewNavigationAllocations(t *testing.T) {
	v := usageViewFixture(t)
	if n := testing.AllocsPerRun(100, func() { usageViewSink = UsageView{scope: v.scope, value: v.value} }); n != 0 {
		t.Fatalf("capturing handle allocated: %g", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		def, _ := v.Definition()
		_, _ = def.RunID()
		_, _ = def.RequestID()
		for _, bucket := range v.Report().Buckets().All() {
			s, _ := bucket.Attribution().Selected()
			_, _ = s.Model()
			usageReadSink, _ = bucket.Counters().Input()
		}
		raw, _ := v.Report().Raw()
		usageReadSink = int64(raw.Data().Len())
	}); n != 0 {
		t.Fatalf("scalar/presence/traversal cloned siblings: %g allocations", n)
	}
	raw, _ := v.Report().Raw()
	if n := testing.AllocsPerRun(100, func() { usageByteSink = raw.Data().Copy() }); n != 1 {
		t.Fatalf("leaf Copy allocated %g times, want one detached leaf", n)
	}
}

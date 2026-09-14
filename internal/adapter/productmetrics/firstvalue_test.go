package productmetrics

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func testResolveEnv() xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/tester", nil },
	}
}

// fakeMarkerFS is the injected filesystem the marker helpers are driven over.
type fakeMarkerFS struct {
	written map[string][]byte
}

func newFakeMarkerFS() *fakeMarkerFS { return &fakeMarkerFS{written: map[string][]byte{}} }

func (f *fakeMarkerFS) readFile(p string) ([]byte, error) {
	if d, ok := f.written[p]; ok {
		return d, nil
	}
	return nil, os.ErrNotExist
}

// createExclusive is the fake's atomic-claim primitive, mirroring the real
// createFileExclusive's O_CREATE|O_EXCL contract: it fails with an
// os.IsExist-satisfying error when the path already exists, so tests can
// exercise the SAME "second caller loses" semantics the real filesystem
// enforces via one flag.
func (f *fakeMarkerFS) createExclusive(p string, d []byte, _ os.FileMode) error {
	if _, ok := f.written[p]; ok {
		return os.ErrExist
	}
	f.written[p] = d
	return nil
}

func (*fakeMarkerFS) mkdirAll(string, os.FileMode) error { return nil }

func TestLoadOrCreateFirstValueMarkerFirstTimeReportsNotYetRecorded(t *testing.T) {
	env, fs := testResolveEnv(), newFakeMarkerFS()

	already, err := LoadOrCreateFirstValueMarker(env, fs.mkdirAll, fs.createExclusive)
	if err != nil {
		t.Fatalf("LoadOrCreateFirstValueMarker: %v", err)
	}
	if already {
		t.Error("already = true on first call, want false")
	}

	already2, err := LoadOrCreateFirstValueMarker(env, fs.mkdirAll, fs.createExclusive)
	if err != nil {
		t.Fatalf("second LoadOrCreateFirstValueMarker: %v", err)
	}
	if !already2 {
		t.Error("already = false on second call, want true")
	}
}

// TestLoadOrCreateFirstValueMarkerClaimIsAtomicAcrossCallers pins the fix for
// the discarded-ownership-result finding: TWO callers racing (or even just
// calling sequentially before either has observed the other) the SAME
// createExclusive-backed marker must have EXACTLY ONE winner (already ==
// false) and every other caller must observe already == true — never both
// reporting false, which would let two processes each record the "at most
// once per install, ever" sample.
func TestLoadOrCreateFirstValueMarkerClaimIsAtomicAcrossCallers(t *testing.T) {
	env, fs := testResolveEnv(), newFakeMarkerFS()

	var wins int
	const callers = 8
	for i := 0; i < callers; i++ {
		already, err := LoadOrCreateFirstValueMarker(env, fs.mkdirAll, fs.createExclusive)
		if err != nil {
			t.Fatalf("LoadOrCreateFirstValueMarker (call %d): %v", i, err)
		}
		if !already {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d across %d calls, want exactly 1", wins, callers)
	}
}

func TestLoadOrCreateFirstValueMarkerFailsClosedWithNoStateDir(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	}
	if _, err := LoadOrCreateFirstValueMarker(env, nil, nil); err == nil {
		t.Fatal("expected an error when no state dir can be resolved, got nil")
	}
	if _, err := FirstValueRecorded(env, nil); err == nil {
		t.Fatal("expected FirstValueRecorded to error when no state dir can be resolved, got nil")
	}
}

// FirstValueRecorded is a pure read: a startup check must NOT create the
// marker, or an install whose first process never has a qualifying run would
// lose its one sample forever.
func TestFirstValueRecordedNeverCreatesTheMarker(t *testing.T) {
	env, fs := testResolveEnv(), newFakeMarkerFS()

	already, err := FirstValueRecorded(env, fs.readFile)
	if err != nil {
		t.Fatalf("FirstValueRecorded: %v", err)
	}
	if already {
		t.Error("already = true with no marker present, want false")
	}
	if len(fs.written) != 0 {
		t.Fatalf("FirstValueRecorded wrote %v, want no writes", fs.written)
	}

	if _, err := LoadOrCreateFirstValueMarker(env, fs.mkdirAll, fs.createExclusive); err != nil {
		t.Fatalf("LoadOrCreateFirstValueMarker: %v", err)
	}
	already, err = FirstValueRecorded(env, fs.readFile)
	if err != nil {
		t.Fatalf("FirstValueRecorded after marking: %v", err)
	}
	if !already {
		t.Error("already = false after the marker was created, want true")
	}
}

// firstValueHistogram returns the collected time_to_first_value data points,
// or nil when the instrument recorded nothing at all.
func firstValueHistogram(t *testing.T, agg metricdata.Aggregation, present bool) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	if !present {
		return nil
	}
	hist, ok := agg.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("aggregation is %T, want Histogram[float64]", agg)
	}
	return hist.DataPoints
}

func qualifyingRun(t *testing.T, r *Recorder, runID string) {
	t.Helper()
	r.ToolCallForRun(runID, session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, 0)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: runID,
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})
}

func TestRecorderRecordsTimeToFirstValueOnceOnly(t *testing.T) {
	r, reader := newTestRecorder(t)
	var marks int
	r.EnableFirstValueTracking(time.Now().Add(-90*time.Second), false, func() (bool, error) { marks++; return true, nil })

	qualifyingRun(t, r, "run-1")
	qualifyingRun(t, r, "run-2")

	agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]
	dps := firstValueHistogram(t, agg, present)
	if len(dps) != 1 || dps[0].Count != 1 {
		t.Fatalf("expected exactly one sample, got %+v", dps)
	}
	if dps[0].Sum < 90 {
		t.Errorf("recorded duration %v s, want at least the 90 s since firstSeenAt", dps[0].Sum)
	}
	if marks != 1 {
		t.Errorf("marker persisted %d times, want exactly 1", marks)
	}
}

// TestRecorderSkipsTimeToFirstValueWhenRecordFnLosesTheClaim pins the fix for
// the discarded-ownership-result finding at the Recorder level: even though
// THIS process's in-memory tracker had not yet observed the metric as
// recorded (alreadyRecorded=false at arm time — the cross-process race
// window), recordFn's own atomic claim can still report won=false (another
// process's LoadOrCreateFirstValueMarker call got there first). The
// Recorder must honor that and emit NOTHING, not record a duplicate sample
// just because ITS in-memory state hadn't caught up.
func TestRecorderSkipsTimeToFirstValueWhenRecordFnLosesTheClaim(t *testing.T) {
	r, reader := newTestRecorder(t)
	var calls int
	r.EnableFirstValueTracking(time.Now().Add(-time.Second), false, func() (bool, error) {
		calls++
		return false, nil // another process already won the cross-process claim.
	})

	qualifyingRun(t, r, "run-1")

	if agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]; present {
		t.Fatalf("time_to_first_value recorded despite recordFn reporting won=false: %+v", agg)
	}
	if calls != 1 {
		t.Errorf("recordFn called %d times, want exactly 1", calls)
	}

	// A second qualifying run must not retry the claim: this process already
	// made its one attempt.
	qualifyingRun(t, r, "run-2")
	if agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]; present {
		t.Fatalf("time_to_first_value recorded on a second run after losing the claim: %+v", agg)
	}
	if calls != 1 {
		t.Errorf("recordFn called %d times after a second run, want still exactly 1 (no retry)", calls)
	}
}

func TestRecorderSkipsTimeToFirstValueWhenAlreadyRecorded(t *testing.T) {
	r, reader := newTestRecorder(t)
	var marks int
	r.EnableFirstValueTracking(time.Now().Add(-time.Second), true, func() (bool, error) { marks++; return true, nil })

	qualifyingRun(t, r, "run-1")

	if agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]; present {
		t.Fatalf("time_to_first_value recorded despite alreadyRecorded=true: %+v", agg)
	}
	if marks != 0 {
		t.Errorf("marker persisted %d times, want 0", marks)
	}
}

func TestRecorderSkipsTimeToFirstValueWhenNotArmed(t *testing.T) {
	r, reader := newTestRecorder(t)
	qualifyingRun(t, r, "run-1")
	if agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]; present {
		t.Fatalf("time_to_first_value recorded without EnableFirstValueTracking: %+v", agg)
	}
}

// A run that ended cleanly but took no action, and a run that acted but did not
// end cleanly, are both non-qualifying: the metric measures time to the first
// run that BOTH acted and succeeded.
func TestRecorderTimeToFirstValueRequiresToolCallAndCleanStop(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.EnableFirstValueTracking(time.Now().Add(-time.Second), false, nil)

	// Clean stop, no tool call.
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-1",
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})
	// Tool call, non-clean stop.
	r.ToolCallForRun("run-2", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, 0)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-2",
		Result: &session.ResultPayload{Stop: session.StopError},
	})

	if agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]; present {
		t.Fatalf("time_to_first_value recorded for a non-qualifying run: %+v", agg)
	}

	// The genuinely qualifying run does record.
	qualifyingRun(t, r, "run-3")
	agg, present := collect(t, reader)["mecatl.product.time_to_first_value"]
	if dps := firstValueHistogram(t, agg, present); len(dps) != 1 || dps[0].Count != 1 {
		t.Fatalf("expected one sample after the qualifying run, got %+v", dps)
	}
}

package agenthook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// recorder is the offline dispatch seam: it captures every notify.sh invocation
// instead of shelling one, so tests never touch a real script or the network.
type recorder struct {
	mu    sync.Mutex
	calls []call
	done  chan struct{}
}

type call struct {
	script   string
	extraEnv []string
	payload  string
}

func newRecorder(expect int) *recorder {
	return &recorder{done: make(chan struct{}, expect+1)}
}

func (r *recorder) run(_ context.Context, script string, extraEnv []string, payload string) {
	r.mu.Lock()
	r.calls = append(r.calls, call{script, extraEnv, payload})
	r.mu.Unlock()
	r.done <- struct{}{}
}

// wait blocks until n dispatches have landed (they run on their own goroutines)
// or the deadline fires.
func (r *recorder) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-r.done:
		case <-deadline:
			t.Fatalf("timed out waiting for dispatch %d/%d", i+1, n)
		}
	}
}

func (r *recorder) snapshot() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]call, len(r.calls))
	copy(out, r.calls)
	return out
}

// newTestNotifier builds a Notifier wired to the recorder, bypassing New's
// environment gate so the dispatch/dedup logic is exercised directly.
func newTestNotifier(r *recorder) *Notifier {
	return &Notifier{
		script:   "notify.sh",
		extraEnv: []string{supersetHarnessVar + "=" + agentID},
		run:      r.run,
		timeout:  time.Second,
	}
}

func decode(t *testing.T, payload string) notifyPayload {
	t.Helper()
	var p notifyPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("payload is not valid JSON (%v): %s", err, payload)
	}
	return p
}

func TestNilNotifierIsNoOp(t *testing.T) {
	// The "not in a Superset terminal" state: every method must be inert, so a
	// caller can hold a nil *Notifier and never branch on it.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a nil Notifier must be inert, got panic: %v", r)
		}
	}()
	var n *Notifier
	n.Start(context.Background(), "s1")
	n.PermissionRequest(context.Background(), "s1", "why")
	n.Stop(context.Background(), "s1", false, "done")
}

func TestStartStopHappyPath(t *testing.T) {
	r := newRecorder(2)
	n := newTestNotifier(r)
	n.Start(context.Background(), "sess-1")
	n.Stop(context.Background(), "sess-1", false, "all good")
	r.wait(t, 2)

	calls := r.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(calls))
	}
	wantEnv := supersetHarnessVar + "=" + agentID
	if len(calls[0].extraEnv) != 1 || calls[0].extraEnv[0] != wantEnv {
		t.Errorf("host env = %v, want [%s]", calls[0].extraEnv, wantEnv)
	}
	start := decode(t, calls[0].payload)
	if start.Event != EventPromptSubmit || start.SessionID != "sess-1" {
		t.Errorf("start payload = %+v", start)
	}
	stop := decode(t, calls[1].payload)
	if stop.Event != EventStop || stop.Message != "all good" {
		t.Errorf("stop payload = %+v", stop)
	}
}

func TestStartDedupedWithinRun(t *testing.T) {
	r := newRecorder(2)
	n := newTestNotifier(r)
	n.Start(context.Background(), "s")
	n.Start(context.Background(), "s") // second Start within the same run: dropped
	n.Stop(context.Background(), "s", false, "")
	r.wait(t, 2) // exactly Start + Stop

	calls := r.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls (one Start, one Stop), got %d: %+v", len(calls), calls)
	}
	if decode(t, calls[0].payload).Event != EventPromptSubmit {
		t.Errorf("first call not Start: %s", calls[0].payload)
	}
	if decode(t, calls[1].payload).Event != EventStop {
		t.Errorf("second call not Stop: %s", calls[1].payload)
	}
}

func TestStopWithoutStartIsDropped(t *testing.T) {
	r := newRecorder(1)
	n := newTestNotifier(r)
	// No Start: a terminal replayed on reconnect has no busy period to close.
	n.Stop(context.Background(), "s", false, "done")
	select {
	case <-r.done:
		t.Fatalf("Stop without a preceding Start should not dispatch: %+v", r.snapshot())
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStopOnlyOncePerRun(t *testing.T) {
	r := newRecorder(2)
	n := newTestNotifier(r)
	n.Start(context.Background(), "s")
	n.Stop(context.Background(), "s", false, "")
	n.Stop(context.Background(), "s", true, "second stop") // dropped
	r.wait(t, 2)
	calls := r.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d: %+v", len(calls), calls)
	}
	if decode(t, calls[1].payload).Event != EventStop {
		t.Errorf("terminal should be the first Stop, not the second: %s", calls[1].payload)
	}
}

func TestFailedTerminal(t *testing.T) {
	r := newRecorder(2)
	n := newTestNotifier(r)
	n.Start(context.Background(), "s")
	n.Stop(context.Background(), "s", true, "boom")
	r.wait(t, 2)
	if got := decode(t, r.snapshot()[1].payload).Event; got != EventStopFailure {
		t.Errorf("failed terminal event = %q, want %q", got, EventStopFailure)
	}
}

func TestStartStopStartAcrossRuns(t *testing.T) {
	r := newRecorder(4)
	n := newTestNotifier(r)
	n.Start(context.Background(), "s")
	n.Stop(context.Background(), "s", false, "")
	n.Start(context.Background(), "s") // new run: Start fires again
	n.Stop(context.Background(), "s", false, "")
	r.wait(t, 4)
	if len(r.snapshot()) != 4 {
		t.Fatalf("want 4 calls across two runs, got %d", len(r.snapshot()))
	}
}

func TestPermissionRequestDoesNotAffectRunState(t *testing.T) {
	r := newRecorder(3)
	n := newTestNotifier(r)
	n.Start(context.Background(), "s")
	n.PermissionRequest(context.Background(), "s", "approve Shell?")
	n.Stop(context.Background(), "s", false, "") // Stop must still fire
	r.wait(t, 3)

	calls := r.snapshot()
	events := []EventType{
		decode(t, calls[0].payload).Event,
		decode(t, calls[1].payload).Event,
		decode(t, calls[2].payload).Event,
	}
	want := []EventType{EventPromptSubmit, EventPermissionRequest, EventStop}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, events[i], want[i])
		}
	}
}

func TestPayloadEscapesHostileMessage(t *testing.T) {
	r := newRecorder(2)
	n := newTestNotifier(r)
	// Embedded quotes/braces plus a control byte: a hand-escaper would either
	// forge extra fields or emit invalid JSON. The message is intentionally
	// trimmed (leading/trailing space) before marshalling, so assert the
	// security property (no forgery, valid JSON, embedded delimiters preserved)
	// rather than a byte-identical round-trip.
	hostile := "hi\",\"hook_event_name\":\"Stop\",\"x\":\"y"
	n.Start(context.Background(), "s")
	n.Stop(context.Background(), "s", false, hostile)
	r.wait(t, 2)

	stop := r.snapshot()[1].payload
	p := decode(t, stop) // must still be valid JSON (no field forgery)
	if p.Event != EventStop {
		t.Errorf("hostile message forged the event: %+v (%s)", p, stop)
	}
	if p.Message != hostile {
		t.Errorf("message round-trip changed: %q", p.Message)
	}
}

// TestEventNamesAreCanonicalSchemaNames pins the emitted vocabulary to the
// CROSS-VENDOR hook schema (identical in Anthropic's Claude Code and OpenAI's
// Codex). These strings are a wire contract with every host tool that consumes
// agent lifecycle hooks — renaming one to a host's normalized alias (Superset
// collapses UserPromptSubmit→Start server-side, for example) would silently stop
// other hosts from recognising the event, and a host that cannot map the name
// drops it rather than guessing. Change these only alongside the vendors.
func TestEventNamesAreCanonicalSchemaNames(t *testing.T) {
	for _, tc := range []struct {
		got  EventType
		want string
	}{
		{EventPromptSubmit, "UserPromptSubmit"},
		{EventStop, "Stop"},
		{EventStopFailure, "StopFailure"},
		{EventPermissionRequest, "PermissionRequest"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("event name = %q, want the canonical %q", tc.got, tc.want)
		}
	}
}

// TestPayloadUsesSchemaFieldNames pins the JSON field names to the schema's
// common input fields, which every host parses.
func TestPayloadUsesSchemaFieldNames(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(buildPayload(EventStop, "s1", "m")), &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hook_event_name", "session_id", "message"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("payload is missing the schema field %q: %v", field, raw)
		}
	}
}

func TestPayloadOmitsEmptyFields(t *testing.T) {
	got := buildPayload(EventPromptSubmit, "", "")
	if got != `{"hook_event_name":"UserPromptSubmit"}` {
		t.Errorf("empty session/message should omit both: %s", got)
	}
}

func TestClipBoundsMessage(t *testing.T) {
	long := make([]byte, maxMessageBytes+500)
	for i := range long {
		long[i] = 'a'
	}
	p := decode(t, buildPayload(EventStop, "s", string(long)))
	if len(p.Message) > maxMessageBytes {
		t.Errorf("message not clipped: %d bytes", len(p.Message))
	}
}

func TestClipKeepsUTF8Valid(t *testing.T) {
	// A multi-byte rune straddling the cut must not produce a broken string.
	var b []byte
	for len(b) < maxMessageBytes+3 {
		b = append(b, "€"...) // 3 bytes each
	}
	out := clip(string(b), maxMessageBytes)
	if len(out) > maxMessageBytes {
		t.Fatalf("clip exceeded max: %d", len(out))
	}
	// Marshalling must succeed and round-trip (no invalid UTF-8 in the tail).
	p := decode(t, buildPayload(EventStop, "s", out))
	if p.Message != out {
		t.Errorf("clipped message did not round-trip")
	}
}

func TestNewReturnsNilOutsideSupersetTerminal(t *testing.T) {
	if n := New([]string{"HOME=/tmp", "PATH=/usr/bin"}); n != nil {
		t.Errorf("New should be nil without SUPERSET_TERMINAL_ID, got %+v", n)
	}
}

func TestNewReturnsNilWhenScriptMissing(t *testing.T) {
	dir := t.TempDir() // no hooks/notify.sh planted
	env := []string{
		"SUPERSET_TERMINAL_ID=term-1",
		"SUPERSET_HOME_DIR=" + dir,
	}
	if n := New(env); n != nil {
		t.Errorf("New should be nil when notify.sh is absent, got %+v", n)
	}
}

func TestNewReturnsNilWhenScriptNotExecutable(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hooks", "notify.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil { // not +x
		t.Fatal(err)
	}
	if n := New(env(dir)); n != nil {
		t.Errorf("New should be nil for a non-executable notify.sh, got %+v", n)
	}
}

func TestNewResolvesExecutableScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hooks", "notify.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := New(env(dir))
	if n == nil {
		t.Fatal("New should resolve an executable notify.sh")
	}
	if n.script != script {
		t.Errorf("script = %q, want %q", n.script, script)
	}
}

func TestNewFallsBackToHomeSupersetDir(t *testing.T) {
	home := t.TempDir()
	script := filepath.Join(home, ".superset", "hooks", "notify.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No SUPERSET_HOME_DIR: must fall back to $HOME/.superset like notify.sh.
	n := New([]string{"SUPERSET_TERMINAL_ID=t", "HOME=" + home})
	if n == nil {
		t.Fatal("New should fall back to $HOME/.superset")
	}
	if n.script != script {
		t.Errorf("script = %q, want %q", n.script, script)
	}
}

func env(homeDir string) []string {
	return []string{
		"SUPERSET_TERMINAL_ID=term-1",
		"SUPERSET_HOME_DIR=" + homeDir,
	}
}

func TestConcurrentStartsFireExactlyOne(t *testing.T) {
	r := newRecorder(16)
	n := newTestNotifier(r)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.Start(context.Background(), "s")
		}()
	}
	wg.Wait()
	// Exactly one Start should have dispatched; give stragglers a moment.
	r.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	if got := len(r.snapshot()); got != 1 {
		t.Errorf("concurrent Starts dispatched %d times, want 1", got)
	}
}

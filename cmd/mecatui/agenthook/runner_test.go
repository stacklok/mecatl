package agenthook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultRunnerDeliversPayloadAndHarness exercises the REAL shell-out
// (defaultRunner), which every other test replaces. It proves the two contract
// points Superset's notify.sh actually reads: the JSON payload arrives on STDIN,
// and SUPERSET_HOOK_HARNESS is exported as the mecatl agent id. A drift in
// either would make notify.sh silently drop our events (no event type => exit 0;
// a foreign harness => exit 0), which is exactly the failure that is invisible
// in production.
//
// Offline: the "notify.sh" here is a local recording script; no host-service and
// no network are involved.
func TestDefaultRunnerDeliversPayloadAndHarness(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "recorded.txt")
	script := filepath.Join(dir, "notify.sh")
	// Record the harness env var and the stdin payload, the two things notify.sh
	// consumes.
	body := "#!/bin/sh\n" +
		"printf 'harness=%s\\n' \"$SUPERSET_HOOK_HARNESS\" > " + out + "\n" +
		"cat >> " + out + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := buildPayload(EventStop, "sess-42", "finished cleanly")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defaultRunner(ctx, script, []string{supersetHarnessVar + "=" + agentID}, payload)

	recorded, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("script did not run (nothing recorded): %v", err)
	}
	got := string(recorded)

	wantHarness := "harness=" + agentID + "\n"
	if !strings.HasPrefix(got, wantHarness) {
		t.Errorf("%s not exported as %q; recorded:\n%s", supersetHarnessVar, agentID, got)
	}
	stdin := strings.TrimPrefix(got, wantHarness)
	var p notifyPayload
	if err := json.Unmarshal([]byte(stdin), &p); err != nil {
		t.Fatalf("stdin payload is not the JSON notify.sh expects (%v): %q", err, stdin)
	}
	if p.Event != EventStop || p.SessionID != "sess-42" || p.Message != "finished cleanly" {
		t.Errorf("payload round-trip mismatch: %+v", p)
	}
}

// TestDefaultRunnerSurvivesFailingScript proves delivery is best-effort: a
// notify.sh that exits non-zero must never surface as a panic or a blocked
// caller (the agent run must be unaffected by the notification channel).
func TestDefaultRunnerSurvivesFailingScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "notify.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defaultRunner(context.Background(), script, nil, buildPayload(EventPromptSubmit, "s", ""))
	// Reaching here without panic is the assertion.
}

// TestDefaultRunnerBoundedByContext proves a wedged notify.sh cannot hang the
// delivery worker forever — the context deadline terminates it.
func TestDefaultRunnerBoundedByContext(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "notify.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defaultRunner(ctx, script, nil, buildPayload(EventStop, "s", ""))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("defaultRunner did not honour the context deadline")
	}
}

// TestEndToEndThroughNotifierToScript drives the full public path — New's
// environment gate, the ordered worker, and the real shell-out — against a
// recording script planted in a synthetic SUPERSET_HOME_DIR, proving the
// assembled pipeline delivers Start then Stop in order.
func TestEndToEndThroughNotifierToScript(t *testing.T) {
	home := t.TempDir()
	out := filepath.Join(home, "events.txt")
	script := filepath.Join(home, "hooks", "notify.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Append one line per event so ordering is observable.
	body := "#!/bin/sh\ncat >> " + out + "\nprintf '\\n' >> " + out + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	n := New([]string{
		"SUPERSET_TERMINAL_ID=term-e2e",
		"SUPERSET_HOME_DIR=" + home,
	})
	if n == nil {
		t.Fatal("New should build a Notifier for a planted executable notify.sh")
	}

	ctx := context.Background()
	n.Start(ctx, "sess-e2e")
	n.Stop(ctx, "sess-e2e", false, "done")

	// The worker delivers asynchronously; poll for both lines.
	var lines []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(out)
		if err == nil {
			lines = nonEmptyLines(string(b))
			if len(lines) >= 2 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(lines) < 2 {
		t.Fatalf("want 2 delivered events, got %d: %v", len(lines), lines)
	}

	first := decodePayload(t, lines[0])
	second := decodePayload(t, lines[1])
	if first.Event != EventPromptSubmit {
		t.Errorf("first delivered event = %q, want Start", first.Event)
	}
	if second.Event != EventStop || second.Message != "done" {
		t.Errorf("second delivered event = %+v, want Stop/done", second)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func decodePayload(t *testing.T, line string) notifyPayload {
	t.Helper()
	var p notifyPayload
	if err := json.Unmarshal([]byte(line), &p); err != nil {
		t.Fatalf("delivered line is not valid JSON (%v): %q", err, line)
	}
	return p
}

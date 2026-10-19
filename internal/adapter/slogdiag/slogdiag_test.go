package slogdiag

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

// TestToSlogLevelMapping pins the port.Level → slog.Level mapping, including the
// LevelDebug arm and the out-of-range default→Info fallback.
func TestToSlogLevelMapping(t *testing.T) {
	cases := []struct {
		in   port.Level
		want slog.Level
	}{
		{port.LevelDebug, slog.LevelDebug},
		{port.LevelInfo, slog.LevelInfo},
		{port.LevelWarn, slog.LevelWarn},
		{port.LevelError, slog.LevelError},
		{port.Level(99), slog.LevelInfo}, // out-of-range falls back to Info
	}
	for _, c := range cases {
		if got := toSlogLevel(c.in); got != c.want {
			t.Errorf("toSlogLevel(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLogRespectsMinLevel(t *testing.T) {
	var buf bytes.Buffer
	d := New(&buf, false, port.LevelWarn)

	d.Log(context.Background(), port.LevelInfo, "below threshold", "k", "v")
	d.Log(context.Background(), port.LevelDebug, "also below", "k", "v")
	if buf.Len() != 0 {
		t.Fatalf("records below minLevel emitted: %q", buf.String())
	}

	d.Log(context.Background(), port.LevelWarn, "at threshold", "k", "v")
	d.Log(context.Background(), port.LevelError, "above threshold", "k", "v")
	out := buf.String()
	if !strings.Contains(out, "at threshold") {
		t.Fatalf("Warn record not emitted: %q", out)
	}
	if !strings.Contains(out, "above threshold") {
		t.Fatalf("Error record not emitted: %q", out)
	}
}

func TestTextFormatEmitsKeyValues(t *testing.T) {
	var buf bytes.Buffer
	d := NewText(&buf) // text, Info threshold

	d.Log(context.Background(), port.LevelInfo, "token counter: tiktoken", "model", "gpt-5")
	out := buf.String()
	if !strings.Contains(out, "token counter: tiktoken") {
		t.Fatalf("message missing from text output: %q", out)
	}
	if !strings.Contains(out, "model=gpt-5") {
		t.Fatalf("key/value missing from text output: %q", out)
	}
}

func TestJSONFormatIsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	d := New(&buf, true, port.LevelInfo)

	d.Log(context.Background(), port.LevelInfo, "compaction strategy: cascade", "strategy", "cascade")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\n%q", err, buf.String())
	}
	if rec["msg"] != "compaction strategy: cascade" {
		t.Fatalf("msg field = %v, want %q", rec["msg"], "compaction strategy: cascade")
	}
	if rec["strategy"] != "cascade" {
		t.Fatalf("strategy field = %v, want %q", rec["strategy"], "cascade")
	}
	if rec["level"] != "INFO" {
		t.Fatalf("level field = %v, want INFO", rec["level"])
	}
}

func TestWithBindsAttributes(t *testing.T) {
	var buf bytes.Buffer
	parent := New(&buf, true, port.LevelInfo)

	child := parent.With("component", "build")
	child.Log(context.Background(), port.LevelInfo, "wired", "n", 3)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("invalid JSON: %v\n%q", err, buf.String())
	}
	if rec["component"] != "build" {
		t.Fatalf("bound attribute missing: got %v", rec["component"])
	}

	// With must not mutate the parent: a parent record carries no bound attr.
	buf.Reset()
	parent.Log(context.Background(), port.LevelInfo, "parent record")
	var prec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &prec); err != nil {
		t.Fatalf("invalid JSON: %v\n%q", err, buf.String())
	}
	if _, ok := prec["component"]; ok {
		t.Fatalf("With leaked a bound attribute onto the parent: %v", prec)
	}
}

func TestNopDiagnosticsIsSilentAndChains(t *testing.T) {
	// port.NopDiagnostics is the default; assert it is a no-op and With returns a
	// usable (still no-op) value rather than nil.
	var d port.Diagnostics = port.NopDiagnostics{}
	d.Log(context.Background(), port.LevelError, "should vanish")
	child := d.With("k", "v")
	if child == nil {
		t.Fatal("NopDiagnostics.With returned nil")
	}
	child.Log(context.Background(), port.LevelError, "also vanishes")
}

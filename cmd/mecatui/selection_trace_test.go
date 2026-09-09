package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

func TestSelectionTraceEnabled(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"0", false}, {"yes", false}, {"1", true}, {" true ", true}, {"TRUE", true},
	} {
		if got := selectionTraceEnabled(func(string) string { return tc.value }); got != tc.want {
			t.Errorf("selectionTraceEnabled(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestNewSelectionTraceWritesStructuredContentFreeRecord(t *testing.T) {
	var out bytes.Buffer
	trace := newSelectionTrace(slog.New(slog.NewTextHandler(&out, nil)), true)
	trace(ui.SelectionTraceRecord{Event: "viewport.content_replace", ViewDirty: true, ViewportBytes: 12, FrameLines: 3, SelectionBaseBytes: 8})
	got := out.String()
	for _, want := range []string{"msg=\"mecatui selection trace\"", "event=viewport.content_replace", "view_dirty=true", "viewport_bytes=12", "frame_lines=3", "selection_base_bytes=8"} {
		if !strings.Contains(got, want) {
			t.Errorf("trace output %q missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"content_tail_sha256", "viewport_lines", "selection_base_lines"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("trace output %q must not contain %q", got, unwanted)
		}
	}
	if trace := newSelectionTrace(slog.Default(), false); trace != nil {
		t.Fatal("disabled selection trace must be nil")
	}
}

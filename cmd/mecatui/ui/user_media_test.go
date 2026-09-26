package ui

import (
	"strings"
	"testing"
)

// TestRenderUserBlockWithMediaPlaceholders asserts a user prompt carrying media
// renders a clear "📎 …" placeholder line per part below the text — so a
// multimodal prompt is never silently shown as text-only. Media is attached via
// the @-mention menu; this exercises the render contract directly.
func TestRenderUserBlockWithMediaPlaceholders(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addUserWithMedia("describe these", []string{"image/png (inline)", "audio/wav (url)"})

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))

	if !strings.Contains(out, "describe these") {
		t.Errorf("user text missing from render: %q", out)
	}
	if !strings.Contains(out, "📎 image/png (inline)") {
		t.Errorf("image placeholder missing: %q", out)
	}
	if !strings.Contains(out, "📎 audio/wav (url)") {
		t.Errorf("audio placeholder missing: %q", out)
	}
}

// TestRenderUserBlockTextOnlyNoPlaceholders asserts a text-only user prompt (the
// addUser path) renders no media placeholder lines.
func TestRenderUserBlockTextOnlyNoPlaceholders(t *testing.T) {
	r := newTestRenderer()
	c := &conversation{}
	c.addUser("just text")

	out := stripANSIstr(r.renderSnapshot(0, c.testBlocks()[0], false))
	if strings.Contains(out, "📎") {
		t.Errorf("text-only prompt should have no media placeholder, got %q", out)
	}
}

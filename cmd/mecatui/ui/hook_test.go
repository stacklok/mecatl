package ui

// Tests for FEATURE 2: richer, structured hook notices (phase + tool + decision)
// rendered distinctly from compaction notices, with blocked hooks coloured as
// errors.

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// renderHookBlock is a small helper that renders a single hook block. It goes
// through renderBlockFresh (the pure per-kind render these tests are about):
// callers render DIFFERENT logical hook blocks through one shared renderer at a
// dummy index, which would alias in renderBlock's per-block cache — its contract
// is one stable conversation index per block (see render_cache_test.go).
func renderHookBlock(r *renderer, text, phase, tool, decision string) string {
	b := block{kind: blockHook, raw: text, hookPhase: phase, hookTool: tool, hookDecision: decision}
	return r.renderBlockFresh(0, &b, false)
}

// TestRenderHookBlocked asserts a blocked hook reads distinctly: it carries the
// error "✗" cue, the client-owned "blocked" verb, the phase label, the tool —
// and a custom reason rides as a trailing tail (not a benign "•").
func TestRenderHookBlocked(t *testing.T) {
	r := newTestRenderer()
	out := stripANSIstr(renderHookBlock(r, "secrets in the diff", "PreToolUse", "Bash", string(client.HookBlocked)))
	if !strings.Contains(out, "✗") {
		t.Errorf("blocked hook should carry the error glyph, got %q", out)
	}
	if !strings.Contains(out, "hook PreToolUse") || !strings.Contains(out, "Bash") {
		t.Errorf("blocked hook should label phase + tool, got %q", out)
	}
	if !strings.Contains(out, ": blocked") {
		t.Errorf("blocked hook should render the client-owned verb, got %q", out)
	}
	if !strings.Contains(out, "— secrets in the diff") {
		t.Errorf("blocked hook should carry the reason as a trailing tail, got %q", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "•") {
		t.Errorf("blocked hook must not render as a benign • notice, got %q", out)
	}
}

// TestRenderHookBlockedDeDupesPhaseEcho asserts the loop's default block message
// ("blocked by <Phase> hook"), which echoes both the phase and the verb, is
// folded away so the phase appears EXACTLY ONCE (on the label) — no
// "PreToolUse … PreToolUse" doubling.
func TestRenderHookBlockedDeDupesPhaseEcho(t *testing.T) {
	r := newTestRenderer()
	out := stripANSIstr(renderHookBlock(r, "blocked by PreToolUse hook", "PreToolUse", "Bash", string(client.HookBlocked)))
	if strings.Count(out, "PreToolUse") != 1 {
		t.Errorf("phase should appear exactly once, got %q", out)
	}
	if !strings.Contains(out, "✗ hook PreToolUse · Bash: blocked") {
		t.Errorf("expected de-duplicated blocked render, got %q", out)
	}
	// The boilerplate echo must NOT survive as a trailing reason.
	if strings.Contains(out, "—") {
		t.Errorf("pure phase/verb echo should leave no reason tail, got %q", out)
	}
}

// TestRenderHookInfo asserts a benign info hook renders as a dim "•" notice with
// its phase label (distinct from a blocked hook).
func TestRenderHookInfo(t *testing.T) {
	r := newTestRenderer()
	out := stripANSIstr(renderHookBlock(r, "session started", "SessionStart", "", string(client.HookInfo)))
	if !strings.Contains(out, "•") {
		t.Errorf("info hook should use the • glyph, got %q", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("info hook must not use the error glyph, got %q", out)
	}
	if !strings.Contains(out, "hook SessionStart") {
		t.Errorf("info hook should label the phase, got %q", out)
	}
}

// TestRenderHookModified asserts a modified hook uses the "✎" cue, the client-
// owned "modified" verb, and folds the loop's "<Phase> hook rewrote …" echo into
// a phase-free reason tail (phase shown exactly once on the label).
func TestRenderHookModified(t *testing.T) {
	r := newTestRenderer()
	out := stripANSIstr(renderHookBlock(r, "PreToolUse hook rewrote tool arguments for Bash", "PreToolUse", "Bash", string(client.HookModified)))
	if !strings.Contains(out, "✎") {
		t.Errorf("modified hook should carry the ✎ glyph, got %q", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("modified hook must not use the error glyph, got %q", out)
	}
	if !strings.Contains(out, ": modified") {
		t.Errorf("modified hook should render the client-owned verb, got %q", out)
	}
	if strings.Count(out, "PreToolUse") != 1 {
		t.Errorf("phase should appear exactly once, got %q", out)
	}
	if !strings.Contains(out, "— rewrote tool arguments for Bash") {
		t.Errorf("modified reason should survive (phase-stripped), got %q", out)
	}
}

// TestRenderHookAdvisory asserts an advisory hook uses the "⚠" glyph and the
// warning style — distinct from blocked (error "✗") and modified (info "✎").
// An advisory guardrail finding flagged content but altered nothing; it is a
// client-visible, model-invisible notice.
func TestRenderHookAdvisory(t *testing.T) {
	r := newTestRenderer()
	out := stripANSIstr(renderHookBlock(r, "guardrail advisory: possible exfil in args", "PreToolUse", "mcp__github__*", string(client.HookAdvisory)))
	if !strings.Contains(out, "⚠") {
		t.Errorf("advisory hook should carry the ⚠ glyph, got %q", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("advisory hook must not use the error glyph, got %q", out)
	}
	if !strings.Contains(out, ": advisory") {
		t.Errorf("advisory hook should render the client-owned verb, got %q", out)
	}
	if !strings.Contains(out, "PreToolUse") || !strings.Contains(out, "mcp__github__*") {
		t.Errorf("advisory hook should label phase + tool, got %q", out)
	}
}

// TestAdvisoryVsBlockedDifferStyling asserts an advisory and a blocked hook with
// the same prose render to DIFFERENT styled output (advisory is a warning, not
// an error).
func TestAdvisoryVsBlockedDifferStyling(t *testing.T) {
	r := newTestRenderer()
	advisory := renderHookBlock(r, "same text", "PreToolUse", "Bash", string(client.HookAdvisory))
	blocked := renderHookBlock(r, "same text", "PreToolUse", "Bash", string(client.HookBlocked))
	if advisory == blocked {
		t.Error("advisory and blocked hooks must render distinctly (warning vs error)")
	}
}

// TestBlockedVsInfoHookDifferStyling asserts a blocked and an info hook with the
// same prose render to DIFFERENT styled output (the whole point of the feature).
func TestBlockedVsInfoHookDifferStyling(t *testing.T) {
	r := newTestRenderer()
	blocked := renderHookBlock(r, "same text", "PostToolUse", "Bash", string(client.HookBlocked))
	info := renderHookBlock(r, "same text", "PostToolUse", "Bash", string(client.HookInfo))
	if blocked == info {
		t.Error("blocked and info hooks must render distinctly")
	}
}

// TestHookSanitizesServerText proves hook prose/tool (server-derived) is stripped
// of terminal escapes before reaching lipgloss (CWE-150).
func TestHookSanitizesServerText(t *testing.T) {
	r := newTestRenderer()
	out := renderHookBlock(r, "evil\x1b[2Jtext", "PreToolUse", "T\x1b]0;x\x07ool", string(client.HookBlocked))
	residual := stripANSI([]byte(out))
	if strings.ContainsRune(string(residual), 0x1b) {
		t.Errorf("server escape survived into hook render: %q", residual)
	}
}

// TestHookMsgRoutesToHookBlock asserts a HookMsg flows through Update into a
// distinct blockHook (not a generic notice), preserving the structured fields.
func TestHookMsgRoutesToHookBlock(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	m = applyAll(m, client.HookMsg{
		Text: "blocked by policy", Phase: "PreToolUse", Tool: "Bash", Decision: client.HookBlocked,
	})
	var found *block
	for i := range m.conv.blocks {
		if m.conv.blocks[i].kind == blockHook {
			found = &m.conv.blocks[i]
		}
	}
	if found == nil {
		t.Fatal("HookMsg did not produce a blockHook")
		return
	}
	if found.hookPhase != "PreToolUse" || found.hookTool != "Bash" || found.hookDecision != "blocked" {
		t.Errorf("hook block fields = %+v", found)
	}
}

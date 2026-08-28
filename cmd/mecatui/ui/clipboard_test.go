package ui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// distinctPNG returns a valid PNG (still sniffs image/png) with a unique trailing
// sentinel byte, so two staged clipboard images are byte-distinguishable and the
// part-ordering at submit can be asserted (not invisible to a sort mutation).
func distinctPNG(t *testing.T, sentinel byte) []byte {
	t.Helper()
	return append(append([]byte{}, tinyPNG(t)...), sentinel)
}

// newClipboardModel builds an idle, sized Model with the given caps and an injected
// Clipboard (nil disables ctrl+v). It mirrors newSubmitModel but threads the
// clipboard collaborator so the ctrl+v reducer path can be driven offline.
func newClipboardModel(t *testing.T, caps client.Capabilities, cb client.Clipboard) (Model, *fakeSender) {
	t.Helper()
	send := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Clipboard:   cb,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   t.TempDir(),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m, send
}

// pressCtrlV drives ctrl+v and runs the returned command's leaves so the
// clipboardResultMsg/clipboardErrMsg is produced, then feeds it back into the
// reducer — modelling the full async round trip the program performs.
func pressCtrlV(t *testing.T, m Model) Model {
	t.Helper()
	mm, cmd := m.Update(ctrlKey('v'))
	m = mm.(Model)
	if cmd == nil {
		return m // nil-clipboard / unavailable path: nothing to deliver
	}
	if msg := cmd(); msg != nil {
		mm, _ = m.Update(msg)
		m = mm.(Model)
	}
	return m
}

// TestCtrlVImageStages: ctrl+v on an image clipboard, image-capable server →
// one staged attachment + an "[Image #1]" marker in the input.
func TestCtrlVImageStages(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)

	if len(m.stagedMedia) != 1 {
		t.Fatalf("staged = %d, want 1", len(m.stagedMedia))
	}
	if !strings.Contains(m.prompt.Value(), "[Image #1]") {
		t.Errorf("input = %q, want it to contain the [Image #1] marker", m.prompt.Value())
	}
	if _, ok := m.stagedMedia["[Image #1]"]; !ok {
		t.Errorf("staged map not keyed by the marker: %v", m.stagedMedia)
	}
}

// TestCtrlVTextFallback: ctrl+v on a text clipboard inserts the text into the
// textarea and stages NOTHING (the spec's image-first, text-fallback contract).
func TestCtrlVTextFallback(t *testing.T) {
	cb := &fakeClipboard{mime: "text/plain", data: []byte("pasted via ctrl+v")}
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)

	if len(m.stagedMedia) != 0 {
		t.Fatalf("staged = %d, want 0 for a text paste", len(m.stagedMedia))
	}
	if !strings.Contains(m.prompt.Value(), "pasted via ctrl+v") {
		t.Errorf("input = %q, want the pasted text", m.prompt.Value())
	}
}

// TestCtrlVTextFallbackWorksWithoutImageCap: ctrl+v TEXT still pastes even when the
// model takes no images — text is not cap-gated. This pins the decision to gate at
// RESULT time on IMAGE results only.
func TestCtrlVTextFallbackWorksWithoutImageCap(t *testing.T) {
	cb := &fakeClipboard{mime: "text/plain", data: []byte("text on a no-image model")}
	m, _ := newClipboardModel(t, client.Capabilities{Image: false}, cb)

	m = pressCtrlV(t, m)

	if !strings.Contains(m.prompt.Value(), "text on a no-image model") {
		t.Errorf("input = %q, want text inserted regardless of image cap", m.prompt.Value())
	}
}

// TestCtrlVImageCapGatedRefused: ctrl+v on an IMAGE clipboard but an image-
// INCAPABLE server refuses with a clear status and stages nothing. The clipboard
// WAS read (the gate is decided at result time, not before the read).
func TestCtrlVImageCapGatedRefused(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, _ := newClipboardModel(t, client.Capabilities{Image: false}, cb)

	m = pressCtrlV(t, m)

	if len(m.stagedMedia) != 0 {
		t.Fatalf("staged = %d, want 0 (image refused on a no-image model)", len(m.stagedMedia))
	}
	if strings.Contains(m.prompt.Value(), "[Image") {
		t.Errorf("input = %q, want no marker on refusal", m.prompt.Value())
	}
	if cb.calls != 1 {
		t.Errorf("clipboard read %d times, want 1 (read happens, then the result is refused)", cb.calls)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "no images") {
		t.Errorf("status = %q, want it to explain the image refusal", stripANSIstr(m.statusMsg))
	}
}

// TestCtrlVNoTool: a backend-missing error surfaces an actionable install hint.
func TestCtrlVNoTool(t *testing.T) {
	cb := &fakeClipboard{err: client.ErrNoClipboardTool}
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)

	status := stripANSIstr(m.statusMsg)
	if !strings.Contains(status, "wl-clipboard") || !strings.Contains(status, "xclip") {
		t.Errorf("status = %q, want the install hint mentioning wl-clipboard/xclip", status)
	}
}

// TestCtrlVEmpty: an empty clipboard is a benign status, not a transcript error.
func TestCtrlVEmpty(t *testing.T) {
	cb := &fakeClipboard{err: client.ErrEmptyClipboard}
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)

	if !strings.Contains(stripANSIstr(m.statusMsg), "empty") {
		t.Errorf("status = %q, want a benign empty-clipboard status", stripANSIstr(m.statusMsg))
	}
	if strings.Contains(stripANSIstr(m.View().Content), "clipboard:") {
		t.Errorf("empty clipboard wrongly added a transcript error")
	}
}

// TestCtrlVNilClipboardUnavailable: a nil Clipboard collaborator yields an
// "unavailable" status and no panic (the inject-time disable).
func TestCtrlVNilClipboardUnavailable(t *testing.T) {
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, nil)

	mm, cmd := m.Update(ctrlKey('v'))
	m = mm.(Model)
	if cmd != nil {
		t.Errorf("nil clipboard should issue no read command")
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "unavailable") {
		t.Errorf("status = %q, want 'unavailable'", stripANSIstr(m.statusMsg))
	}
}

// TestCtrlVOversizeError: a non-sentinel error (e.g. an oversize image from the
// adapter) becomes a loud transcript error.
func TestCtrlVOversizeError(t *testing.T) {
	cb := &fakeClipboard{err: errOversize}
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)

	if !strings.Contains(stripANSIstr(m.View().Content), "clipboard:") {
		t.Errorf("oversize error should surface as a transcript 'clipboard:' error")
	}
}

var errOversize = &clipErr{"clipboard image is too big"}

type clipErr struct{ s string }

func (e *clipErr) Error() string { return e.s }

// TestSubmitStagedImageSendsPart: a surviving marker → one part sent, the marker
// stripped from the sent text, and the 📎 descriptor rendered.
func TestSubmitStagedImageSendsPart(t *testing.T) {
	png := tinyPNG(t)
	cb := &fakeClipboard{mime: "image/png", data: png}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)
	// Add some surrounding prose so we can assert the marker is stripped but the
	// prose survives.
	m.prompt.Rewrite("describe " + m.prompt.Value())

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("phase = %d, want running", m.phase)
	}
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	p := frames[0].GetPrompt()
	if len(p.GetParts()) != 1 {
		t.Fatalf("parts = %d, want 1", len(p.GetParts()))
	}
	if p.GetParts()[0].GetMimeType() != "image/png" {
		t.Errorf("part mime = %q, want image/png", p.GetParts()[0].GetMimeType())
	}
	// The clipboard bytes actually reached the frame (not just the mime).
	if !bytes.Equal(p.GetParts()[0].GetData(), png) {
		t.Errorf("part data = %d bytes, want the staged %d-byte PNG", len(p.GetParts()[0].GetData()), len(png))
	}
	if strings.Contains(p.GetText(), "[Image") {
		t.Errorf("sent text still has a marker: %q", p.GetText())
	}
	if !strings.Contains(p.GetText(), "describe") {
		t.Errorf("sent text lost the prose: %q", p.GetText())
	}
	if m.stagedMedia != nil {
		t.Errorf("staged media not cleared after submit: %v", m.stagedMedia)
	}
}

// TestSubmitDeletedMarkerSendsNoPart: if the user deletes the marker from the
// input before sending, the staged attachment is dropped (0 parts).
func TestSubmitDeletedMarkerSendsNoPart(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m)
	// User erases everything and types fresh text — the marker is gone.
	m.prompt.Rewrite("never mind, just text")

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	if got := len(frames[0].GetPrompt().GetParts()); got != 0 {
		t.Errorf("parts = %d, want 0 (marker was deleted)", got)
	}
}

// TestSubmitTwoStagedImages: two ctrl+v pastes of DISTINGUISHABLE images → two
// parts, in ascending-N order (part[0] is #1, part[1] is #2), both markers
// stripped. The distinct bytes mean a reversed survivingMarkers sort would flip
// the parts and fail this test (the mutation guard the QA panel asked for).
func TestSubmitTwoStagedImages(t *testing.T) {
	first := distinctPNG(t, 0x01)
	second := distinctPNG(t, 0x02)
	cb := &fakeClipboard{mime: "image/png", seq: [][]byte{first, second}}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m) // stages [Image #1] = first
	m = pressCtrlV(t, m) // stages [Image #2] = second
	if len(m.stagedMedia) != 2 {
		t.Fatalf("staged = %d, want 2", len(m.stagedMedia))
	}
	if !strings.Contains(m.prompt.Value(), "[Image #1]") || !strings.Contains(m.prompt.Value(), "[Image #2]") {
		t.Fatalf("input = %q, want both markers", m.prompt.Value())
	}

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	p := send.frames()[0].GetPrompt()
	if len(p.GetParts()) != 2 {
		t.Fatalf("parts = %d, want 2", len(p.GetParts()))
	}
	// Ordering: part[0] ⟵ #1 (first bytes), part[1] ⟵ #2 (second bytes).
	if !bytes.Equal(p.GetParts()[0].GetData(), first) {
		t.Errorf("part[0] is not the #1 image (ordering broken)")
	}
	if !bytes.Equal(p.GetParts()[1].GetData(), second) {
		t.Errorf("part[1] is not the #2 image (ordering broken)")
	}
	if strings.Contains(p.GetText(), "[Image") {
		t.Errorf("sent text still has a marker: %q", p.GetText())
	}
}

// TestSubmitMixedMentionAndClipboard: an @-mention image AND a clipboard image →
// 2 parts in ONE frame (a single send path, never two).
func TestSubmitMixedMentionAndClipboard(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	ws := t.TempDir()
	send := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Clipboard:   cb,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   ws,
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Image: true}},
	)
	if err := os.WriteFile(filepath.Join(ws, "shot.png"), tinyPNG(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m = pressCtrlV(t, m)                              // stages [Image #1]
	m.prompt.Rewrite("@shot.png " + m.prompt.Value()) // prepend the mention

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1 (single send path)", len(frames))
	}
	if got := len(frames[0].GetPrompt().GetParts()); got != 2 {
		t.Errorf("parts = %d, want 2 (mention + clipboard)", got)
	}
}

// TestSubmitStagedCapFlippedRejects: if caps flipped to image-incapable between
// staging and submit, StageClipboardImage refuses → addError, 0 frames, input
// kept, model idle.
func TestSubmitStagedCapFlippedRejects(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)
	m = pressCtrlV(t, m)
	before := m.prompt.Value()

	// Simulate the cap being unavailable at submit time.
	m.caps.Image = false

	mm, _ := m.submitPrompt()
	m = mm.(Model)

	if m.phase == phaseRunning {
		t.Fatalf("phase = running, want idle (cap-gated submit must be rejected)")
	}
	if got := len(send.frames()); got != 0 {
		t.Errorf("sent %d frames, want 0 on refusal", got)
	}
	if m.prompt.Value() != before {
		t.Errorf("input = %q, want it kept (%q) on refusal", m.prompt.Value(), before)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "attach:") {
		t.Errorf("transcript missing the attach refusal")
	}
}

// TestSubmitMediaOnlyMarkerSends: a prompt that is JUST a marker (no prose) still
// sends — a media-only prompt is legal (text OR parts non-empty).
func TestSubmitMediaOnlyMarkerSends(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m) // input is just "[Image #1] "

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("phase = %d, want running (media-only marker prompt should send)", m.phase)
	}
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	p := frames[0].GetPrompt()
	if len(p.GetParts()) != 1 {
		t.Errorf("parts = %d, want 1", len(p.GetParts()))
	}
	if strings.TrimSpace(p.GetText()) != "" {
		t.Errorf("text = %q, want empty (marker stripped) for a media-only prompt", p.GetText())
	}
}

// TestSubmitMultiLinePromptPreservesNewlines is the FIX-1 regression guard: a
// MULTI-LINE prompt with a staged image must keep its newline/paragraph structure
// in the sent text — only the marker (and its stray separator space) is removed.
// The old collapseSpaces flattened everything to one line; this pins the fix.
func TestSubmitMultiLinePromptPreservesNewlines(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m) // stages [Image #1], input becomes "[Image #1] "
	// A deliberately multi-line prompt with the marker embedded mid-line.
	m.prompt.Rewrite("line one\n\nline two [Image #1]\nline three")

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	p := send.frames()[0].GetPrompt()
	if len(p.GetParts()) != 1 {
		t.Fatalf("parts = %d, want 1", len(p.GetParts()))
	}
	want := "line one\n\nline two\nline three"
	if p.GetText() != want {
		t.Errorf("sent text = %q, want %q (newlines preserved, marker + its space stripped)", p.GetText(), want)
	}
	if strings.Contains(p.GetText(), "  ") {
		t.Errorf("sent text has a double space where the marker was: %q", p.GetText())
	}
}

// TestSubmitAggregateCapRejects is the FIX-2 guard: enough staged clipboard images
// to push the COMBINED media part count over the per-prompt cap loud-rejects
// client-side (addError, 0 frames, input kept, idle) — not a post-send server
// reject. Staging maxPromptMediaParts+1 tiny images crosses the count cap.
func TestSubmitAggregateCapRejects(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	const over = 17 // maxPromptMediaParts (16) + 1
	for i := 0; i < over; i++ {
		m = pressCtrlV(t, m)
	}
	if len(m.stagedMedia) != over {
		t.Fatalf("staged = %d, want %d", len(m.stagedMedia), over)
	}
	before := m.prompt.Value()

	mm, _ := m.submitPrompt()
	m = mm.(Model)

	if m.phase == phaseRunning {
		t.Fatalf("phase = running, want idle (combined cap exceeded must reject)")
	}
	if got := len(send.frames()); got != 0 {
		t.Errorf("sent %d frames, want 0 on aggregate-cap refusal", got)
	}
	if m.prompt.Value() != before {
		t.Errorf("input = %q, want it kept on refusal", m.prompt.Value())
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "too many media attachments") {
		t.Errorf("transcript missing the aggregate-cap error:\n%s", stripANSIstr(m.View().Content))
	}
}

// TestSubmitMarkerCollisionGuard stages exactly #1 and #11 (skipping #2…#10 by
// directly seeding nextMediaN) and submits through the real path: it asserts 2
// parts and NO residual "Image" text. This is the regression guard against a
// future marker-format change reintroducing the "[Image #1]" ⊂ "[Image #11]"
// substring collision (today the trailing "]" keeps markers prefix-safe).
func TestSubmitMarkerCollisionGuard(t *testing.T) {
	one := distinctPNG(t, 0x01)
	eleven := distinctPNG(t, 0x0b)
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, nil)

	// Seed two markers whose numbers DIFFER in length (#1 vs #11) so a substring
	// strip of "[Image #1]" would corrupt "[Image #11]".
	m.stagedMedia = map[string]stagedAttachment{
		"[Image #1]":  {mime: "image/png", data: one},
		"[Image #11]": {mime: "image/png", data: eleven},
	}
	m.nextMediaN = 11
	m.prompt.Rewrite("a [Image #1] b [Image #11] c")

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	p := send.frames()[0].GetPrompt()
	if len(p.GetParts()) != 2 {
		t.Fatalf("parts = %d, want 2 (#1 and #11, no collision)", len(p.GetParts()))
	}
	if !bytes.Equal(p.GetParts()[0].GetData(), one) || !bytes.Equal(p.GetParts()[1].GetData(), eleven) {
		t.Errorf("parts out of order or wrong bytes (#1 then #11 expected)")
	}
	if strings.Contains(p.GetText(), "Image") || strings.Contains(p.GetText(), "[") {
		t.Errorf("sent text has residual marker debris: %q", p.GetText())
	}
	if p.GetText() != "a b c" {
		t.Errorf("sent text = %q, want %q", p.GetText(), "a b c")
	}
}

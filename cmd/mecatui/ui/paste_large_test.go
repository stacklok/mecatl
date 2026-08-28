package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// largePasteText builds a single-line payload over the rune threshold, with
// distinctive ends so a partial expansion (or a residual marker) is detectable.
func largePasteText() string {
	return "BEGIN-" + strings.Repeat("x", pasteCharThreshold) + "-END"
}

// tallPasteText builds a payload over the LINE threshold but well under the rune
// threshold, so it isolates the line-count trigger.
func tallPasteText() string {
	return strings.TrimSuffix(strings.Repeat("line\n", pasteLineThreshold), "\n")
}

// TestLargePasteBecomesPlaceholder: a paste at/over the rune threshold stages the
// payload and inserts only the "[Pasted text #1]" marker.
func TestLargePasteBecomesPlaceholder(t *testing.T) {
	m, _ := newQueueModel(t)

	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)

	if len(m.stagedPastes) != 1 {
		t.Fatalf("stagedPastes = %d, want 1", len(m.stagedPastes))
	}
	if got, ok := m.stagedPastes["[Pasted text #1]"]; !ok || got != largePasteText() {
		t.Fatalf("store not keyed by the marker / payload mangled: %v", len(m.stagedPastes))
	}
	if !strings.Contains(m.ta.Value(), "[Pasted text #1]") {
		t.Errorf("input = %q, want the placeholder marker", m.ta.Value())
	}
	if strings.Contains(m.ta.Value(), "BEGIN-") {
		t.Errorf("input still holds the pasted payload (len %d)", len(m.ta.Value()))
	}
}

// TestTallPasteBecomesPlaceholder: >= pasteLineThreshold lines stage even when the
// rune count is far below pasteCharThreshold.
func TestTallPasteBecomesPlaceholder(t *testing.T) {
	m, _ := newQueueModel(t)
	payload := tallPasteText()
	if len([]rune(payload)) >= pasteCharThreshold {
		t.Fatalf("test payload must be under the rune threshold to isolate the line trigger")
	}

	mm, _ := m.Update(pasteMsg(payload))
	m = mm.(Model)

	if len(m.stagedPastes) != 1 {
		t.Fatalf("stagedPastes = %d, want 1 (line-count trigger)", len(m.stagedPastes))
	}
	if !strings.Contains(m.ta.Value(), "[Pasted text #1]") {
		t.Errorf("input = %q, want the placeholder marker", m.ta.Value())
	}
}

// TestSmallPasteUnchanged: below both thresholds the paste lands literally —
// byte-identical pre-staging behaviour, nothing staged.
func TestSmallPasteUnchanged(t *testing.T) {
	m, _ := newQueueModel(t)

	mm, _ := m.Update(pasteMsg("hello pasted world"))
	m = mm.(Model)

	if got := m.ta.Value(); got != "hello pasted world" {
		t.Fatalf("input = %q, want the literal paste", got)
	}
	if len(m.stagedPastes) != 0 {
		t.Fatalf("stagedPastes = %d, want 0 for a small paste", len(m.stagedPastes))
	}
}

// TestPasteThresholdBoundary: 1999 runes stay literal, 2000 runes stage — pinning
// the exact >= cutoff.
func TestPasteThresholdBoundary(t *testing.T) {
	under := strings.Repeat("a", pasteCharThreshold-1)
	at := strings.Repeat("a", pasteCharThreshold)

	m, _ := newQueueModel(t)
	mm, _ := m.Update(pasteMsg(under))
	m = mm.(Model)
	if len(m.stagedPastes) != 0 {
		t.Fatalf("%d runes staged, want literal below the threshold", pasteCharThreshold-1)
	}
	if m.ta.Value() != under {
		t.Fatalf("input len = %d, want the %d-rune literal", len(m.ta.Value()), len(under))
	}

	m2, _ := newQueueModel(t)
	mm, _ = m2.Update(pasteMsg(at))
	m2 = mm.(Model)
	if len(m2.stagedPastes) != 1 {
		t.Fatalf("%d runes not staged, want placeholder at the threshold", pasteCharThreshold)
	}
	if strings.Contains(m2.ta.Value(), at) {
		t.Errorf("input still holds the at-threshold payload")
	}
}

// TestSubmitExpandsPastePlaceholder: submitting a prompt whose marker survives
// sends the FULL staged text (no marker token), and clears the store.
func TestSubmitExpandsPastePlaceholder(t *testing.T) {
	m, conv := newQueueModel(t)
	payload := largePasteText()

	mm, _ := m.Update(pasteMsg(payload))
	m = mm.(Model)

	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)

	frames := conv.send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	got := frames[0].GetPrompt().GetText()
	if got != payload {
		t.Errorf("sent text = %q…(len %d), want the full %d-rune payload", truncate(got, 40), len(got), len(payload))
	}
	if strings.Contains(got, "[Pasted text") {
		t.Errorf("sent text still carries the placeholder token")
	}
	if m.stagedPastes != nil {
		t.Errorf("stagedPastes not cleared after submit: %d entries", len(m.stagedPastes))
	}
	if m.nextPasteN != 0 {
		t.Errorf("nextPasteN = %d, want 0 after submit", m.nextPasteN)
	}
}

// TestDeletedPlaceholderDropsPaste: erasing the marker before sending silently
// drops the staged content at submit (the image-marker UX) — the replacement text
// goes out alone and the store still clears.
func TestDeletedPlaceholderDropsPaste(t *testing.T) {
	m, conv := newQueueModel(t)

	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)
	m.ta.SetValue("never mind, just text")

	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)

	frames := conv.send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	got := frames[0].GetPrompt().GetText()
	if got != "never mind, just text" {
		t.Errorf("sent text = %q, want the replacement only", got)
	}
	if strings.Contains(got, "BEGIN-") {
		t.Errorf("deleted placeholder's content leaked into the send")
	}
	if m.stagedPastes != nil {
		t.Errorf("stagedPastes not cleared after submit: %d entries", len(m.stagedPastes))
	}
}

// TestPlaceholderPreservesSurroundingText: prose typed before and after the
// placeholder survives expansion in order (before → payload → after).
func TestPlaceholderPreservesSurroundingText(t *testing.T) {
	m, conv := newQueueModel(t)
	payload := largePasteText()

	m = typeText(t, m, "before")
	mm, _ := m.Update(pasteMsg(payload))
	m = mm.(Model)
	m = typeText(t, m, "after")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runBatchLeaves(cmd)

	got := conv.send.frames()[0].GetPrompt().GetText()
	if !strings.HasPrefix(got, "before ") {
		t.Errorf("sent text lost the leading prose: %q…", truncate(got, 30))
	}
	if !strings.HasSuffix(got, " after") {
		t.Errorf("sent text lost the trailing prose: …%q", truncate(got[max(0, len(got)-30):], 30))
	}
	if !strings.Contains(got, payload) {
		t.Errorf("sent text lost the expanded payload")
	}
}

// TestMixedPasteAndImageMarkers: a staged image and a staged paste coexist in one
// prompt — the image still attaches as a part (marker stripped), the paste still
// expands inline, and both stores clear.
func TestMixedPasteAndImageMarkers(t *testing.T) {
	png := tinyPNG(t)
	cb := &fakeClipboard{mime: "image/png", data: png}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	m = pressCtrlV(t, m) // stages [Image #1]
	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model) // stages [Pasted text #1]

	if len(m.stagedMedia) != 1 || len(m.stagedPastes) != 1 {
		t.Fatalf("stores = media %d / pastes %d, want 1 / 1 (coexist)", len(m.stagedMedia), len(m.stagedPastes))
	}

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	p := send.frames()[0].GetPrompt()
	if len(p.GetParts()) != 1 {
		t.Fatalf("parts = %d, want 1 (the image)", len(p.GetParts()))
	}
	if strings.Contains(p.GetText(), "[Image") || strings.Contains(p.GetText(), "[Pasted text") {
		t.Errorf("sent text has residual marker debris: %q…", truncate(p.GetText(), 60))
	}
	if !strings.Contains(p.GetText(), largePasteText()) {
		t.Errorf("sent text lost the expanded paste payload")
	}
	if m.stagedMedia != nil || m.stagedPastes != nil {
		t.Errorf("stores not cleared after submit: media %v / pastes %v", m.stagedMedia, m.stagedPastes)
	}
}

// TestQueuedLargePasteExpandsAtEnqueue: a mid-run enter expands the placeholder
// into the queued text immediately, so the queue holds FINAL text and the staged
// store does not outlive the (reset) textarea.
func TestQueuedLargePasteExpandsAtEnqueue(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	payload := largePasteText()

	mm, _ := m.Update(pasteMsg(payload))
	m = mm.(Model)
	if len(m.stagedPastes) != 1 {
		t.Fatalf("stagedPastes = %d, want 1 (paste mid-run stages too)", len(m.stagedPastes))
	}

	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)

	if len(m.queued) != 1 {
		t.Fatalf("queued = %d, want 1", len(m.queued))
	}
	if m.queued[0] != payload {
		t.Errorf("queued text = %q…(len %d), want the expanded payload", truncate(m.queued[0], 40), len(m.queued[0]))
	}
	if strings.Contains(m.queued[0], "[Pasted text") {
		t.Errorf("queued text still carries the placeholder token")
	}
	if m.stagedPastes != nil {
		t.Errorf("stagedPastes not cleared at enqueue: %d entries", len(m.stagedPastes))
	}
}

// TestPlaceholderIgnoredBehindOverlay: a large paste while the help overlay owns
// the keyboard is dropped entirely — nothing staged, nothing inserted (mirrors
// paste_test.go's overlay gates).
func TestPlaceholderIgnoredBehindOverlay(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	mm, _ := m.Update(qmark())
	m = mm.(Model)
	if !m.showHelp {
		t.Fatalf("help overlay should be open after '?'")
	}

	mm, _ = m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)

	if len(m.stagedPastes) != 0 {
		t.Fatalf("stagedPastes = %d, want 0 behind the overlay", len(m.stagedPastes))
	}
	if m.nextPasteN != 0 {
		t.Fatalf("nextPasteN = %d, want 0 behind the overlay (counter must not burn)", m.nextPasteN)
	}
	if got := m.ta.Value(); got != "" {
		t.Fatalf("paste leaked into input behind help overlay: %q", truncate(got, 40))
	}
}

// TestClearDropsStagedPastes: /clear (the resetSession seam) drops the staged
// pastes and resets the counter alongside the staged media. The input is RESET
// after staging (the marker goes away; the store deliberately dangles) so the
// "/clear" line is a genuine start-of-line built-in — leaving the marker in
// place would make the line "[Pasted text #1] /clear", which is NOT a command
// and would fall into submitPrompt (whose own store-clearing made an earlier
// version of this test pass with the resetSession clearing deleted —
// mutation-proven vacuous).
func TestClearDropsStagedPastes(t *testing.T) {
	m, conv := newQueueModel(t)
	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)
	if len(m.stagedPastes) != 1 {
		t.Fatalf("stagedPastes = %d, want 1 before /clear", len(m.stagedPastes))
	}
	m.ta.Reset() // marker deleted; the dangling store is exactly what /clear must drop

	// Drive the real /clear built-in: type it and submit (the palette claims enter
	// and runs the selected built-in row, the same user path as a bare-line submit).
	m = typeText(t, m, "/clear")
	mm, clearCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	// Complete the actual create-first handoff; resetSession runs only after its
	// successful command result is reduced.
	ready := firstBatchLeaf(t, clearCmd)
	mm, _ = m.Update(ready)
	m = mm.(Model)

	if len(m.stagedPastes) != 0 {
		t.Errorf("stagedPastes survived /clear: %d entries", len(m.stagedPastes))
	}
	if m.nextPasteN != 0 {
		t.Errorf("nextPasteN = %d, want 0 after /clear", m.nextPasteN)
	}
	if got := len(conv.send.frames()); got != 0 {
		t.Errorf("sent %d frames, want 0 (the built-in must intercept, never submit)", got)
	}
}

// TestCumulativePastesStage: the staging decision is CUMULATIVE — two pastes each
// under the per-paste rune threshold stage the SECOND one once buffer + paste
// crosses pasteCharThreshold, so repeated medium pastes can never rebuild the
// O(buffer) keystroke lag. Only the incoming paste is staged: the first paste
// (literal, legitimate) stays in the buffer untouched.
func TestCumulativePastesStage(t *testing.T) {
	m, _ := newQueueModel(t)
	first := "F-" + strings.Repeat("a", 1500)
	second := "S-" + strings.Repeat("b", 1500)

	mm, _ := m.Update(pasteMsg(first))
	m = mm.(Model)
	if len(m.stagedPastes) != 0 {
		t.Fatalf("first 1500-rune paste staged, want literal (under the per-paste threshold)")
	}

	mm, _ = m.Update(pasteMsg(second))
	m = mm.(Model)
	if len(m.stagedPastes) != 1 {
		t.Fatalf("second 1500-rune paste not staged — the cumulative-buffer hole is back")
	}
	if got := m.stagedPastes["[Pasted text #1]"]; got != second {
		t.Errorf("staged payload is not the second paste (len %d)", len(got))
	}
	if strings.Contains(m.ta.Value(), "S-") {
		t.Errorf("second payload leaked into the buffer")
	}
	if !strings.Contains(m.ta.Value(), first) {
		t.Errorf("first literal paste was converted out of the buffer — only the incoming paste may stage")
	}
}

// TestPasteRuneThresholdNotBytes: the staging cutoff counts RUNES, not bytes —
// 1999 CJK runes (~6KB of UTF-8) stay literal. A regression to len() would stage
// this payload (5997 bytes >= 2000) and fail here.
func TestPasteRuneThresholdNotBytes(t *testing.T) {
	payload := strings.Repeat("漢", pasteCharThreshold-1)
	if len(payload) < pasteCharThreshold {
		t.Fatalf("test payload must exceed the threshold in BYTES to catch a len() regression")
	}

	m, _ := newQueueModel(t)
	mm, _ := m.Update(pasteMsg(payload))
	m = mm.(Model)

	if len(m.stagedPastes) != 0 {
		t.Fatalf("1999-rune CJK paste staged — threshold is counting bytes, not runes")
	}
	if m.ta.Value() != payload {
		t.Errorf("input != the literal CJK payload (len %d)", len(m.ta.Value()))
	}
}

// TestPasteLineThresholdUnderBoundary: pasteLineThreshold-1 (29) lines, well under
// the rune threshold, stays literal — pinning the line-count >= cutoff from below
// (the twin of TestPasteThresholdBoundary's rune pin).
func TestPasteLineThresholdUnderBoundary(t *testing.T) {
	under := strings.TrimSuffix(strings.Repeat("ln\n", pasteLineThreshold-1), "\n") // 29 lines
	m, _ := newQueueModel(t)

	mm, _ := m.Update(pasteMsg(under))
	m = mm.(Model)

	if len(m.stagedPastes) != 0 {
		t.Fatalf("%d-line paste staged, want literal under the line threshold", pasteLineThreshold-1)
	}
	if m.ta.Value() != under {
		t.Errorf("input = %q…, want the literal multi-line paste", truncate(m.ta.Value(), 20))
	}
}

// TestExpansionSinglePass: a staged payload that CONTAINS another surviving
// marker's literal text (plausible in a pasted code/log excerpt) survives
// verbatim — the single left-to-right Replacer pass never rescans replaced text,
// so the embedded token is NOT re-substituted (the sequential-ReplaceAll
// corruption), while the real marker #2 still expands exactly once.
func TestExpansionSinglePass(t *testing.T) {
	m, conv := newQueueModel(t)
	payload1 := "P1-" + strings.Repeat("x", pasteCharThreshold) + ` quoted: "[Pasted text #2]" -P1`
	payload2 := "P2-" + strings.Repeat("y", pasteCharThreshold) + "-P2"

	mm, _ := m.Update(pasteMsg(payload1))
	m = mm.(Model)
	mm, _ = m.Update(pasteMsg(payload2))
	m = mm.(Model)
	if len(m.stagedPastes) != 2 {
		t.Fatalf("stagedPastes = %d, want 2", len(m.stagedPastes))
	}

	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)

	got := conv.send.frames()[0].GetPrompt().GetText()
	if !strings.Contains(got, payload1) {
		t.Errorf("payload-1 not sent verbatim — its embedded \"[Pasted text #2]\" token was re-substituted")
	}
	if n := strings.Count(got, payload2); n != 1 {
		t.Errorf("payload-2 appears %d times, want exactly once (at its real marker)", n)
	}
	if m.stagedPastes != nil {
		t.Errorf("stagedPastes not cleared after submit")
	}
}

// TestQueueFullPasteRejectKeepsStore: a queue-full enqueue reject happens BEFORE
// placeholder expansion, so it must keep both the staged store AND the marker in
// the input — the user can retry after the queue drains.
func TestQueueFullPasteRejectKeepsStore(t *testing.T) {
	m, _ := newQueueModel(t)
	m = startRunning(t, m, "first")
	for i := 0; i < maxQueued; i++ {
		m = enqueue(t, m, fmt.Sprintf("follow-up %d", i))
	}
	if len(m.queued) != maxQueued {
		t.Fatalf("queued = %d, want the full %d", len(m.queued), maxQueued)
	}

	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // enqueue attempt mid-run → queue full
	m = mm.(Model)

	if len(m.queued) != maxQueued {
		t.Errorf("queue grew past the cap: %d", len(m.queued))
	}
	if len(m.stagedPastes) != 1 {
		t.Errorf("stagedPastes = %d, want 1 kept on the queue-full reject", len(m.stagedPastes))
	}
	if !strings.Contains(m.ta.Value(), "[Pasted text #1]") {
		t.Errorf("input lost the marker on the queue-full reject: %q", truncate(m.ta.Value(), 40))
	}
}

// TestAggregateCapRejectKeepsPasteStore: the combined media-cap loud reject fires
// AFTER placeholder expansion but must still keep the staged-paste store (and the
// media store) — the input keeps its markers, so a cleared store would turn the
// retry into a literal-marker send. This is the post-expansion reject seam.
func TestAggregateCapRejectKeepsPasteStore(t *testing.T) {
	cb := &fakeClipboard{mime: "image/png", data: tinyPNG(t)}
	m, send := newClipboardModel(t, client.Capabilities{Image: true}, cb)

	const over = 17 // maxPromptMediaParts (16) + 1 → CheckAggregateCaps loud-rejects
	for i := 0; i < over; i++ {
		m = pressCtrlV(t, m)
	}
	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)
	if len(m.stagedPastes) != 1 {
		t.Fatalf("stagedPastes = %d, want 1 before submit", len(m.stagedPastes))
	}

	mm, _ = m.submitPrompt()
	m = mm.(Model)

	if got := len(send.frames()); got != 0 {
		t.Fatalf("sent %d frames, want 0 on the aggregate-cap refusal", got)
	}
	if len(m.stagedPastes) != 1 {
		t.Errorf("stagedPastes = %d, want 1 kept for the retry (markers still in the input)", len(m.stagedPastes))
	}
	if len(m.stagedMedia) != over {
		t.Errorf("stagedMedia = %d, want %d kept for the retry", len(m.stagedMedia), over)
	}
	if !strings.Contains(m.ta.Value(), "[Pasted text #1]") {
		t.Errorf("input lost the paste marker on the refusal")
	}
}

// TestLongMediaPathPasteStaysMedia: a pasted MEDIA-FILE PATH that is itself over
// the paste rune threshold still stages as an IMAGE (tryPasteMediaPath runs
// before the large-paste staging in onPaste) — media priority, never a
// "[Pasted text #N]" placeholder wrapping a path.
func TestLongMediaPathPasteStaysMedia(t *testing.T) {
	m, _ := newClipboardModel(t, client.Capabilities{Image: true}, nil)

	// Build a REAL >= pasteCharThreshold-rune absolute path to a valid PNG via
	// nested directories (each segment well under NAME_MAX, total under PATH_MAX).
	dir := m.deps.Workspace
	seg := strings.Repeat("d", 100)
	for len([]rune(dir)) < pasteCharThreshold {
		dir = filepath.Join(dir, seg)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if strings.Contains(err.Error(), "file name too long") {
			t.Skipf("skipping: cannot construct a %d-rune path on this platform: %v", pasteCharThreshold, err)
		}
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "long.png")
	if err := os.WriteFile(path, tinyPNG(t), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mm, _ := m.Update(pasteMsg(path))
	m = mm.(Model)

	if len(m.stagedMedia) != 1 {
		t.Fatalf("stagedMedia = %d, want 1 (path paste must stage as media)", len(m.stagedMedia))
	}
	if len(m.stagedPastes) != 0 {
		t.Fatalf("stagedPastes = %d, want 0 — the path must not fall into text staging", len(m.stagedPastes))
	}
	if !strings.Contains(m.ta.Value(), "[Image #1]") {
		t.Errorf("input = %q…, want the image marker", truncate(m.ta.Value(), 40))
	}
}

// TestPlaceholderGoldenView locks a frame with a staged-paste placeholder visible
// in the input region (the user-facing affordance the staging substitutes for the
// raw payload).
func TestPlaceholderGoldenView(t *testing.T) {
	m := zeroStateModel(t, embeddedCaps())
	mm, _ := m.Update(pasteMsg(largePasteText()))
	m = mm.(Model)

	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "paste_placeholder.golden", got)
}

// --- input render memoization -----------------------------------------------

// pokeInputCache plants a sentinel in the renderer's input cache so a subsequent
// renderInput distinguishes a cache HIT (sentinel returned) from a re-render
// (real output returned). renderInput must have run at least once first.
func pokeInputCache(t *testing.T, m Model, sentinel string) {
	t.Helper()
	if !m.rend.inputValid {
		t.Fatalf("input cache not primed — call renderInput first")
	}
	m.rend.inputView = sentinel
}

// TestRenderInputCachedWhenUnchanged: with no textarea mutation between two
// renders, the second render is served from cache (the sentinel comes back), and
// the cached output equals a fresh render byte-for-byte.
func TestRenderInputCachedWhenUnchanged(t *testing.T) {
	m, _ := newQueueModel(t)
	m = typeText(t, m, "draft")

	first := m.renderInput()
	if second := m.renderInput(); second != first {
		t.Fatalf("two no-edit renders differ:\n%q\n%q", first, second)
	}

	pokeInputCache(t, m, "SENTINEL")
	if got := m.renderInput(); got != "SENTINEL" {
		t.Errorf("unchanged input re-rendered (cache miss), want the cache hit")
	}
}

// TestRenderInputCacheServesView: the cached input render is what the FULL View
// pipeline serves — the sentinel planted in the cache must appear in m.View()'s
// frame, so a future refactor that bypasses renderInput's memo from View() (or
// re-renders the textarea on a second, uncached path) is caught here, not just
// in the unit-level renderInput tests.
func TestRenderInputCacheServesView(t *testing.T) {
	m, _ := newQueueModel(t)
	m = typeText(t, m, "draft")

	_ = m.View() // prime the cache through the real full-frame path
	pokeInputCache(t, m, "INPUT-CACHE-SENTINEL")
	if !strings.Contains(stripANSIstr(m.View().Content), "INPUT-CACHE-SENTINEL") {
		t.Fatalf("full View() did not serve the cached input render — the hot path bypasses the memo")
	}
}

// TestRenderInputInvalidatedByEdit: an edit (typed rune) must invalidate the
// cache — the next render is fresh, not the stale pre-edit string. This is the
// staleness guard the mutation drill targets (a cache that ignores its key serves
// the sentinel here).
func TestRenderInputInvalidatedByEdit(t *testing.T) {
	m, _ := newQueueModel(t)
	m = typeText(t, m, "a")

	before := m.renderInput()
	pokeInputCache(t, m, "SENTINEL")

	// The Update pipeline itself re-renders via the relayout chokepoint; if the
	// key comparison were broken the sentinel would survive it AND this render.
	m = typeText(t, m, "b")
	got := m.renderInput()
	if got == "SENTINEL" {
		t.Fatalf("edit did not invalidate the input render cache")
	}
	if got == before {
		t.Errorf("post-edit render is byte-identical to the pre-edit one")
	}
}

// TestRenderInputInvalidatedByCursorMove: a pure cursor move (left arrow — value
// unchanged) must also invalidate, so the rendered cursor cell keeps moving and is
// never frozen by the cache. (The virtual cursor is STATIC in mecatui —
// cursor.BlinkMsg is never routed to the textarea — so position + focus fully
// determine the cursor cell; this is the deterministic stand-in for a
// blink-not-frozen guard.)
func TestRenderInputInvalidatedByCursorMove(t *testing.T) {
	m, _ := newQueueModel(t)
	m = typeText(t, m, "ab")

	_ = m.renderInput()
	pokeInputCache(t, m, "SENTINEL")

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = mm.(Model)
	if got := m.renderInput(); got == "SENTINEL" {
		t.Fatalf("cursor move did not invalidate the input render cache (frozen cursor)")
	}
}

// TestRenderInputInvalidatedBySelection proves Ctrl+G's real Update route busts
// the cache even though it changes only the textarea's selection rendering state.
func TestRenderInputInvalidatedBySelection(t *testing.T) {
	m, _ := newQueueModel(t)
	m = typeText(t, m, "select this draft")

	unselected := m.renderInput()
	pokeInputCache(t, m, "SENTINEL")

	// Drive the real prompt-selection route. Ctrl+G changes only textarea
	// selection state, leaving the value, cursor, focus, and dimensions intact.
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.ta.HasSelection() {
		t.Fatal("ctrl+g did not select the prompt")
	}

	got := m.renderInput()
	if got == "SENTINEL" {
		t.Fatal("selection did not invalidate the input render cache")
	}
	if got == unselected {
		t.Fatal("selected input render is byte-identical to the unselected render")
	}
}

// TestRenderInputInvalidatedByFocusChange: focus/blur flips the virtual cursor's
// visibility, so it must miss the cache too (focused is part of the key).
func TestRenderInputInvalidatedByFocusChange(t *testing.T) {
	m, _ := newQueueModel(t)
	m = typeText(t, m, "ab")

	_ = m.renderInput()
	pokeInputCache(t, m, "SENTINEL")

	m.ta.Blur()
	if got := m.renderInput(); got == "SENTINEL" {
		t.Fatalf("blur did not invalidate the input render cache")
	}
}

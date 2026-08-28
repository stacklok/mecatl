package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestMentionToken is the detection table for the live @-mention menu trigger.
func TestMentionToken(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantOK    bool
		wantToken string
	}{
		{"bare @", "@", true, ""},
		{"@foo", "@foo", true, "foo"},
		{"mid-line word", "describe @img", true, "img"},
		{"path token", "@cmd/main.go", true, "cmd/main.go"},
		{"no @", "hello", false, ""},
		{"email-ish (@ not at word start)", "user@host", false, ""},
		{"space-terminated (back to prose)", "@foo bar", false, ""},
		{"command line is the palette's", "/clear", false, ""},
		{"multi-line disqualifies", "describe\n@img", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, ok := mentionToken(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("mentionToken(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && tok != tc.wantToken {
				t.Fatalf("mentionToken(%q) token = %q, want %q", tc.in, tok, tc.wantToken)
			}
		})
	}
}

// newMentionModel builds an idle, sized Model whose Workspace is ws (a temp dir
// seeded by the caller), so syncMention walks a controlled tree.
func newMentionModel(t *testing.T, ws string) Model {
	t.Helper()
	recv := &fakeRecver{script: nil, gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Server:      "127.0.0.1:8080",
		Workspace:   ws,
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"},
	)
}

// seedWorkspace creates a small tree under a temp dir: a handful of files, a
// nested dir, and a dot-dir that must be pruned from the menu.
func seedWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := []string{"main.go", "README.md", "cmd/app.go", "internal/util.go", ".git/config"}
	for _, f := range files {
		full := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return dir
}

// TestSyncMentionFiltersWorkspace verifies typing "@main" opens the menu with only
// the matching file, that a dot-dir is pruned, and a bare "@" lists files capped to
// the row window.
func TestSyncMentionFiltersWorkspace(t *testing.T) {
	ws := seedWorkspace(t)
	m := newMentionModel(t, ws)

	m.ta.SetValue("@main")
	m = m.syncMention()
	if !m.mention.open {
		t.Fatalf("menu did not open for @main")
	}
	if len(m.mention.matches) != 1 || m.mention.matches[0] != "main.go" {
		t.Fatalf("matches = %v, want [main.go]", m.mention.matches)
	}

	// Bare "@" lists every (non-dot) file, capped to the row window.
	m.ta.SetValue("@")
	m = m.syncMention()
	if !m.mention.open {
		t.Fatalf("menu did not open for bare @")
	}
	if len(m.mention.matches) > maxMentionRows {
		t.Fatalf("matches = %d, want ≤ %d", len(m.mention.matches), maxMentionRows)
	}
	for _, p := range m.mention.matches {
		if strings.HasPrefix(p, ".git") {
			t.Fatalf("dot-dir not pruned: %q in %v", p, m.mention.matches)
		}
	}
	// The 4 non-dot files are all present (under the cap).
	if len(m.mention.matches) != 4 {
		t.Fatalf("matches = %v, want the 4 non-dot files", m.mention.matches)
	}
}

// TestMentionCompleteInsertsPath verifies enter/tab completion replaces the
// trailing @token with "@<path> " (trailing space), preserves preceding text, and
// closes the menu.
func TestMentionCompleteInsertsPath(t *testing.T) {
	ws := seedWorkspace(t)
	m := newMentionModel(t, ws)

	m.ta.SetValue("describe @main")
	m = m.syncMention()
	if !m.mention.open || len(m.mention.matches) != 1 {
		t.Fatalf("precondition: open=%v matches=%v", m.mention.open, m.mention.matches)
	}
	m = m.mentionComplete()
	if got := m.ta.Value(); got != "describe @main.go " {
		t.Fatalf("input = %q, want %q", got, "describe @main.go ")
	}
	if m.mention.open {
		t.Fatalf("menu stayed open after completion")
	}
}

// TestMentionNavigateClamps verifies the selection clamps at both ends (no wrap),
// mirroring the palette's navigation contract.
func TestMentionNavigateClamps(t *testing.T) {
	ws := seedWorkspace(t)
	m := newMentionModel(t, ws)
	m.ta.SetValue("@") // lists 4 files
	m = m.syncMention()
	if len(m.mention.matches) < 2 {
		t.Fatalf("need ≥2 matches to test navigation, got %v", m.mention.matches)
	}

	m.mentionMoveUp() // already at top → clamp
	if m.mention.cursor != 0 {
		t.Fatalf("cursor = %d after up at top, want 0", m.mention.cursor)
	}
	for i := 0; i < len(m.mention.matches)+3; i++ {
		m.mentionMoveDown()
	}
	if m.mention.cursor != len(m.mention.matches)-1 {
		t.Fatalf("cursor = %d after many downs, want %d (last)", m.mention.cursor, len(m.mention.matches)-1)
	}
}

// TestMentionEscDismisses verifies esc closes the menu without changing the input
// and it stays closed while the same @token holds (dismiss latch).
func TestMentionEscDismisses(t *testing.T) {
	ws := seedWorkspace(t)
	m := newMentionModel(t, ws)
	m.ta.SetValue("@main")
	m = m.syncMention()
	if !m.mention.open {
		t.Fatalf("precondition: menu open")
	}
	m = m.mentionDismiss()
	if m.mention.open {
		t.Fatalf("menu open after esc")
	}
	// Re-sync with the same token: stays closed (latched).
	m = m.syncMention()
	if m.mention.open {
		t.Fatalf("menu reopened while the same @token holds after esc-dismiss")
	}
	if got := m.ta.Value(); got != "@main" {
		t.Fatalf("input = %q, want unchanged @main", got)
	}
}

// TestMentionAndPaletteMutuallyExclusive verifies a "/" line drives the palette and
// never the mention menu, even if it contained an "@".
func TestMentionAndPaletteMutuallyExclusive(t *testing.T) {
	ws := seedWorkspace(t)
	m := newMentionModel(t, ws)
	if _, ok := mentionToken("/cmd @main"); ok {
		t.Fatalf("a command line must not trigger the mention menu")
	}
	m.ta.SetValue("/clear")
	m = m.syncMention()
	if m.mention.open {
		t.Fatalf("mention menu opened on a command line")
	}
}

// TestSubmitWithImageMentionSendsPart is the end-to-end ui wiring test: a prompt
// with an @-mention to an image file, on an image-capable server, sends a Prompt
// frame carrying one inline image part AND renders the 📎 placeholder in the
// transcript. It pins parse → resolve → ExpandMentions → addUserWithMedia →
// SendPrompt(parts) together.
func TestSubmitWithImageMentionSendsPart(t *testing.T) {
	ws := t.TempDir()
	png := tinyPNG(t)
	if err := os.WriteFile(filepath.Join(ws, "shot.png"), png, 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	send := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   ws,
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Image: true}},
	)

	m.ta.SetValue("describe @shot.png")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("phase = %d, want running after submit", m.phase)
	}
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	p := frames[0].GetPrompt()
	if p == nil {
		t.Fatalf("frame is not a Prompt: %#v", frames[0])
	}
	if len(p.GetParts()) != 1 {
		t.Fatalf("prompt parts = %d, want 1", len(p.GetParts()))
	}
	if p.GetParts()[0].GetMimeType() != "image/png" {
		t.Errorf("part mime = %q, want image/png", p.GetParts()[0].GetMimeType())
	}
	// The transcript shows the 📎 placeholder.
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "📎 image/png (inline)") {
		t.Errorf("transcript missing media placeholder:\n%s", view)
	}
}

// TestSubmitImageMentionCapGatedRejects asserts an image mention on a server
// WITHOUT image support loud-rejects: an inline error, no frame sent, input kept,
// and the model stays idle.
func TestSubmitImageMentionCapGatedRejects(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "shot.png"), tinyPNG(t), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	send := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   ws,
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001"}, // caps all-false
	)

	m.ta.SetValue("describe @shot.png")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	if m.phase == phaseRunning {
		t.Fatalf("phase = running, want idle (the submit must be rejected, no stream opened)")
	}
	if got := len(send.frames()); got != 0 {
		t.Fatalf("sent %d frames, want 0 on cap-gated refusal", got)
	}
	if got := m.ta.Value(); got != "describe @shot.png" {
		t.Fatalf("input = %q, want it kept on refusal", got)
	}
	// The transcript shows the loud attach error (the message is wrapped to the
	// viewport width, so assert on the leading, always-visible "attach:" prefix).
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "attach:") {
		t.Errorf("transcript missing the cap-gated attach error:\n%s", view)
	}
}

// tinyPNG returns a minimal valid PNG (sniffs as image/png).
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "client", "testdata", "pixel.png"))
	if err != nil {
		t.Fatalf("read png fixture: %v", err)
	}
	return data
}

// tinyPDF returns the checked-in PDF fixture (sniffs as application/pdf — an
// existing-but-unsupported binary file).
func tinyPDF(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "client", "testdata", "doc.pdf"))
	if err != nil {
		t.Fatalf("read pdf fixture: %v", err)
	}
	return data
}

// newSubmitModel builds an idle, sized Model rooted at ws with the given caps, for
// the submit-path tests.
func newSubmitModel(t *testing.T, ws string, caps client.Capabilities) (Model, *fakeSender) {
	t.Helper()
	send := &fakeSender{}
	conv := &fakeConv{recv: &fakeRecver{}, send: send}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Workspace:   ws,
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m, send
}

// TestSubmitProseAtWordSendsAsText asserts an @-token that does NOT resolve to a
// real file ("@oncall") is left as literal prose — the submit sends the plain text
// with NO error and NO parts (the stat-filter gate decides attachment-vs-prose).
func TestSubmitProseAtWordSendsAsText(t *testing.T) {
	ws := t.TempDir() // empty: @oncall resolves to nothing
	m, send := newSubmitModel(t, ws, client.Capabilities{Image: true})

	m.ta.SetValue("ping me @oncall")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("phase = %d, want running (prose @ should send as text)", m.phase)
	}
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	p := frames[0].GetPrompt()
	if p == nil || p.GetText() != "ping me @oncall" {
		t.Fatalf("prompt = %#v, want text 'ping me @oncall'", p)
	}
	if len(p.GetParts()) != 0 {
		t.Errorf("parts = %d, want 0 (prose @ is not an attachment)", len(p.GetParts()))
	}
	if strings.Contains(stripANSIstr(m.View().Content), "attach:") {
		t.Errorf("prose @ wrongly raised an attach error")
	}
}

// TestSubmitDirectoryMentionStaysText asserts an @-token pointing at a directory
// stays prose (a directory is not a regular file), so it sends as text with no
// error and no parts.
func TestSubmitDirectoryMentionStaysText(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "somedir"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m, send := newSubmitModel(t, ws, client.Capabilities{Image: true})

	m.ta.SetValue("look in @somedir please")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.phase != phaseRunning {
		t.Fatalf("phase = %d, want running (a directory @ should send as text)", m.phase)
	}
	runBatchLeaves(cmd)

	frames := send.frames()
	if len(frames) != 1 {
		t.Fatalf("sent %d frames, want 1", len(frames))
	}
	p := frames[0].GetPrompt()
	if p == nil || p.GetText() != "look in @somedir please" || len(p.GetParts()) != 0 {
		t.Fatalf("prompt = %#v, want plain text, no parts", p)
	}
}

// TestSubmitUnsupportedFileRejects asserts an @-mention to an existing-but-
// unsupported file (a PDF) loud-rejects: no frame, input kept, idle — never inlined
// as raw-byte garbage.
func TestSubmitUnsupportedFileRejects(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "report.pdf"), tinyPDF(t), 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	m, send := newSubmitModel(t, ws, client.Capabilities{Image: true, Audio: true})

	m.ta.SetValue("read @report.pdf")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(cmd)

	if m.phase == phaseRunning {
		t.Fatalf("phase = running, want idle (an unsupported file must be rejected)")
	}
	if got := len(send.frames()); got != 0 {
		t.Fatalf("sent %d frames, want 0", got)
	}
	if got := m.ta.Value(); got != "read @report.pdf" {
		t.Fatalf("input = %q, want it kept on refusal", got)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "attach:") {
		t.Errorf("transcript missing the unsupported-file attach error")
	}
}

// TestParseMentionPaths verifies whole-prompt extraction of @-mention paths
// (every @word across the prompt), skipping non-mentions and bare "@".
func TestParseMentionPaths(t *testing.T) {
	got := parseMentionPaths("look at @a.png and @sub/b.txt but not user@host or @")
	want := []string{"a.png", "sub/b.txt"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

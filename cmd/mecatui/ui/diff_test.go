package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func newTestRenderer() *renderer {
	r := newRenderer(theme.New("aztec", theme.AztecPalette()), defaultHelpKeys())
	r.setWidth(100)
	return r
}

func TestRenderEditDiff(t *testing.T) {
	r := newTestRenderer()
	args := `{"path":"main.go","old_string":"fmt.Println(\"hi\")","new_string":"fmt.Println(\"hello\")"}`
	out, ok := r.renderToolDiff("Edit", args, false)
	if !ok {
		t.Fatal("Edit diff should render")
	}
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "main.go  -1 +1") {
		t.Errorf("expected path header with -M +N size signal, got %q", plain)
	}
	if !strings.Contains(plain, `- fmt.Println("hi")`) {
		t.Errorf("expected removed line, got %q", plain)
	}
	if !strings.Contains(plain, `+ fmt.Println("hello")`) {
		t.Errorf("expected added line, got %q", plain)
	}
}

func TestRenderEditDiffReplaceAll(t *testing.T) {
	r := newTestRenderer()
	args := `{"path":"a.txt","old_string":"x","new_string":"y","replace_all":true}`
	out, ok := r.renderToolDiff("Edit", args, false)
	if !ok {
		t.Fatal("Edit diff should render")
	}
	if !strings.Contains(stripANSIstr(out), "(replace all)") {
		t.Errorf("expected (replace all) tag, got %q", stripANSIstr(out))
	}
}

func TestRenderWriteDiff(t *testing.T) {
	r := newTestRenderer()
	args := `{"path":"notes/todo.txt","content":"buy milk\neggs"}`
	out, ok := r.renderToolDiff("Write", args, false)
	if !ok {
		t.Fatal("Write diff should render")
	}
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "notes/todo.txt · 2 lines (overwrites if it exists)") {
		t.Errorf("expected path header with line count, got %q", plain)
	}
	if !strings.Contains(plain, "+ buy milk") || !strings.Contains(plain, "+ eggs") {
		t.Errorf("expected green content lines, got %q", plain)
	}
}

func TestRenderToolDiffMetadataPreservesWhitespaceAndStaysBounded(t *testing.T) {
	r := newTestRenderer()
	const width = 12
	out := renderToolMetadata(r.th.Style("diffMeta"), "metadata-without-breaks  ", width)
	plain := stripANSIstr(out)
	if joined := strings.ReplaceAll(plain, "\n", ""); !strings.HasSuffix(joined, "  ") {
		t.Fatalf("metadata trailing whitespace changed: %q", plain)
	}
	for i, row := range strings.Split(out, "\n") {
		if got := maxLineWidth(row); got > width {
			t.Errorf("metadata row %d width = %d, want <= %d: %q", i, got, width, stripANSIstr(row))
		}
	}
}

func TestRenderToolDiffMalformedFallsBack(t *testing.T) {
	r := newTestRenderer()
	// Not JSON at all.
	if _, ok := r.renderToolDiff("Edit", "not json", false); ok {
		t.Error("malformed Edit args should fall back (ok=false)")
	}
	// Missing required fields.
	if _, ok := r.renderToolDiff("Edit", `{"path":""}`, false); ok {
		t.Error("empty Edit args should fall back (ok=false)")
	}
	if _, ok := r.renderToolDiff("Write", `{"content":"x"}`, false); ok {
		t.Error("Write with no path should fall back (ok=false)")
	}
	// Non-diff tool.
	if _, ok := r.renderToolDiff("Read", `{"path":"x"}`, false); ok {
		t.Error("non-diff tool should return ok=false")
	}
}

func TestRenderToolDiffSanitizes(t *testing.T) {
	r := newTestRenderer()
	// A JSON  unicode escape decodes to a raw ESC byte inside new_string;
	// the renderer must strip it before it reaches lipgloss (CWE-150).
	args := `{"path":"a.txt","old_string":"safe","new_string":"line\u001b[2Jevil"}`
	out, ok := r.renderToolDiff("Edit", args, false)
	if !ok {
		t.Fatal("Edit diff should render")
	}
	// Strip the theme's legitimate ANSI; any residual ESC is the payload.
	residual := stripANSI([]byte(out))
	if strings.ContainsRune(string(residual), 0x1b) {
		t.Errorf("server escape survived into diff: %q", residual)
	}
}

func TestDiffSideCollapsesWhenNotExpanded(t *testing.T) {
	r := newTestRenderer()
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line\n")
	}
	args := `{"path":"big.txt","content":` + jsonQuote(sb.String()) + `}`

	collapsed, ok := r.renderToolDiff("Write", args, false)
	if !ok {
		t.Fatal("Write diff should render")
	}
	if !strings.Contains(stripANSIstr(collapsed), "ctrl+t expand") {
		t.Errorf("collapsed diff should show expand hint, got %q", stripANSIstr(collapsed))
	}

	expanded, _ := r.renderToolDiff("Write", args, true)
	if strings.Contains(stripANSIstr(expanded), "ctrl+t expand") {
		t.Errorf("expanded diff should NOT show expand hint")
	}
	// Expanded has more lines than collapsed.
	if strings.Count(expanded, "\n") <= strings.Count(collapsed, "\n") {
		t.Errorf("expanded diff should have more lines than collapsed")
	}
}

// jsonQuote produces a JSON string literal for embedding in a test args blob.
func jsonQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return "\"" + s + "\""
}

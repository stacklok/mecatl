package toolkit

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

func TestTruncate(t *testing.T) {
	const marker = "\n... [output truncated: exceeded 25000 bytes]"

	t.Run("under the cap is returned unchanged", func(t *testing.T) {
		s := strings.Repeat("a", 10)
		if got := Truncate(s, 10); got != s {
			t.Fatalf("Truncate at exact cap mutated input: got %q", got)
		}
		if got := Truncate(s, 11); got != s {
			t.Fatalf("Truncate below cap mutated input: got %q", got)
		}
	})

	t.Run("over the cap is trimmed with the marker", func(t *testing.T) {
		s := strings.Repeat("a", 20)
		got := Truncate(s, 10)
		want := strings.Repeat("a", 10) + marker
		if got != want {
			t.Fatalf("Truncate over cap = %q, want %q", got, want)
		}
	})

	t.Run("cuts on a rune boundary to keep valid UTF-8", func(t *testing.T) {
		// "é" is two bytes (0xC3 0xA9). A cap landing mid-rune must back off.
		s := "aaaé" + strings.Repeat("b", 10)
		got := Truncate(s, 4) // byte 4 is the second byte of "é".
		if !strings.HasPrefix(got, "aaa") {
			t.Fatalf("expected prefix aaa, got %q", got)
		}
		body := strings.TrimSuffix(got, marker)
		if body != "aaa" {
			t.Fatalf("expected rune-safe body %q, got %q", "aaa", body)
		}
	})
}

func TestParseArgs(t *testing.T) {
	type args struct {
		Key string `json:"key"`
	}

	t.Run("valid payload unmarshals", func(t *testing.T) {
		var a args
		msg, ok := ParseArgs(session.ToolCall{Args: json.RawMessage(`{"key":"v"}`)}, &a)
		if !ok {
			t.Fatalf("ParseArgs failed on valid payload: %q", msg)
		}
		if a.Key != "v" {
			t.Fatalf("Key = %q, want v", a.Key)
		}
	})

	t.Run("empty payload is treated as empty object", func(t *testing.T) {
		var a args
		if msg, ok := ParseArgs(session.ToolCall{}, &a); !ok {
			t.Fatalf("ParseArgs failed on empty payload: %q", msg)
		}
	})

	t.Run("malformed payload returns a model-facing error", func(t *testing.T) {
		var a args
		msg, ok := ParseArgs(session.ToolCall{Args: json.RawMessage(`{bad`)}, &a)
		if ok {
			t.Fatal("ParseArgs accepted malformed JSON")
		}
		if !strings.HasPrefix(msg, "invalid arguments:") {
			t.Fatalf("error message = %q, want prefix %q", msg, "invalid arguments:")
		}
	})
}

func TestSchema(t *testing.T) {
	lit := `{"type":"object"}`
	got := Schema(lit)
	if string(got) != lit {
		t.Fatalf("Schema = %q, want %q", string(got), lit)
	}
	// Result is valid JSON usable as a RawMessage.
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("Schema output is not valid JSON: %v", err)
	}
}

func TestMaxOutputBytes(t *testing.T) {
	if MaxOutputBytes != 25_000 {
		t.Fatalf("MaxOutputBytes = %d, want 25000", MaxOutputBytes)
	}
}

func TestRuneStartBoundary(t *testing.T) {
	// The truncation helpers cut on utf8.RuneStart boundaries: ASCII and lead
	// bytes are rune starts; continuation bytes (0b10xxxxxx) are not.
	if !utf8.RuneStart('a') {
		t.Fatalf("ASCII byte should be a rune start")
	}
	if !utf8.RuneStart(0xC3) { // lead byte of "é"
		t.Fatalf("lead byte should be a rune start")
	}
	if utf8.RuneStart(0xA9) { // continuation byte of "é"
		t.Fatalf("continuation byte must not be a rune start")
	}
}

func TestTruncateRunes(t *testing.T) {
	const ellipsis = "…"

	t.Run("at or under the cap is returned unchanged", func(t *testing.T) {
		s := strings.Repeat("a", 10)
		if got := TruncateRunes(s, 10); got != s {
			t.Fatalf("TruncateRunes at exact cap mutated input: got %q", got)
		}
		if got := TruncateRunes(s, 11); got != s {
			t.Fatalf("TruncateRunes below cap mutated input: got %q", got)
		}
	})

	t.Run("over the cap is trimmed with an ellipsis on a rune boundary", func(t *testing.T) {
		// "é" is two bytes (0xC3 0xA9). A cut landing mid-rune must back off so the
		// result stays valid UTF-8.
		s := "aaaé" + strings.Repeat("b", 20)
		got := TruncateRunes(s, 6) // cut = 6 - len("…")=3, which is the start of "é".
		if !strings.HasSuffix(got, ellipsis) {
			t.Fatalf("expected trailing ellipsis, got %q", got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("TruncateRunes produced invalid UTF-8: %q", got)
		}
	})
}

func TestSplitFrontmatter(t *testing.T) {
	t.Run("well-formed block splits into frontmatter and body", func(t *testing.T) {
		fm, body, ok := SplitFrontmatter("---\nname: x\n---\nhello\n")
		if !ok {
			t.Fatalf("expected ok")
		}
		if fm != "name: x\n" {
			t.Fatalf("frontmatter = %q", fm)
		}
		if body != "hello\n" {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("leading BOM is tolerated", func(t *testing.T) {
		fm, _, ok := SplitFrontmatter("\ufeff---\nname: x\n---\nbody")
		if !ok || fm != "name: x\n" {
			t.Fatalf("BOM not tolerated: ok=%v fm=%q", ok, fm)
		}
	})

	t.Run("CRLF line endings are normalised", func(t *testing.T) {
		fm, body, ok := SplitFrontmatter("---\r\nname: x\r\n---\r\nbody")
		if !ok || fm != "name: x\n" || body != "body" {
			t.Fatalf("CRLF not normalised: ok=%v fm=%q body=%q", ok, fm, body)
		}
	})

	t.Run("missing leading delimiter fails", func(t *testing.T) {
		if _, _, ok := SplitFrontmatter("no frontmatter here"); ok {
			t.Fatalf("expected failure for missing frontmatter")
		}
	})

	t.Run("missing closing delimiter fails", func(t *testing.T) {
		if _, _, ok := SplitFrontmatter("---\nname: x\nbody without close"); ok {
			t.Fatalf("expected failure for missing closing delimiter")
		}
	})
}

func TestIndexClosingDelim(t *testing.T) {
	if got := IndexClosingDelim("a\n---\nb\n"); got != 2 {
		t.Fatalf("IndexClosingDelim = %d, want 2", got)
	}
	if got := IndexClosingDelim("no delim here\n"); got != -1 {
		t.Fatalf("IndexClosingDelim with no delimiter = %d, want -1", got)
	}
}

package modeltext

import "testing"

func TestTruncateRunes(t *testing.T) {
	if got := TruncateRunes("short", 256); got != "short" {
		t.Errorf("under-cap string altered: %q", got)
	}
	if got := TruncateRunes("日本語テス", 3); got != "日本語" {
		t.Errorf("rune truncation = %q, want 日本語", got)
	}
}

func TestStripControls(t *testing.T) {
	c1 := string([]rune{'a', 0x9b, 'b', 0x80, 'c', 0x9f, 'd'})
	for input, want := range map[string]string{
		"clean":            "clean",
		"a\x00b\x1bc\x7fd": "abcd",
		c1:                 "abcd",
		"a\u202eb":         "ab",
		"a\u2028b\u2029c":  "abc",
	} {
		if got := StripControls(input); got != want {
			t.Errorf("StripControls(%q) = %q, want %q", input, got, want)
		}
	}
}

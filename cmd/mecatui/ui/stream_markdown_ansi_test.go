package ui

import "testing"

func TestNormalizeMarkdownANSI(t *testing.T) {
	const reset = "\x1b[0m"
	const fg = "\x1b[38;5;36m"
	const rgb = "\x1b[38;2;231;226;211m"
	for _, tt := range []struct {
		name  string
		a, b  string
		equal bool
	}{
		// #2661: a timed-out Bash builtin match produces Text("echo step ")
		// instead of NameBuiltin("echo"), Text(" step "). Palette masking
		// already discards their colour distinction; run grouping must not matter.
		{"builtin becomes text", fg + "echo" + reset + "\x1b[38;5;254m step " + reset + "\x1b[38;5;173m0" + reset,
			"\x1b[38;5;254mecho step " + reset + "\x1b[38;5;173m0" + reset, true},
		{"palette tie", "\x1b[38;5;72mstring" + reset, "\x1b[38;5;78mstring" + reset, true},
		{"equivalent attributes", "\x1b[1;3mboth" + reset, "\x1b[3m\x1b[1mboth" + reset, true},
		{"text", fg + "echo" + reset, fg + "exit" + reset, false},
		{"wrapping", fg + "two words" + reset, fg + "two\nwords" + reset, false},
		{"padding", fg + "echo " + reset, fg + "echo" + reset, false},
		{"uncoloured code", fg + "echo" + reset, "echo", false},
		{"bold", "\x1b[1m" + fg + "echo" + reset, fg + "echo" + reset, false},
		{"italic", "\x1b[3m" + fg + "echo" + reset, fg + "echo" + reset, false},
		{"underline", "\x1b[4m" + fg + "echo" + reset, fg + "echo" + reset, false},
		{"attribute reset between runs", "\x1b[1m" + fg + "echo" + reset + fg + " step" + reset,
			"\x1b[1m" + fg + "echo step" + reset, false},
		{"background", "\x1b[48;5;36mcode" + reset, "\x1b[48;5;37mcode" + reset, false},
		{"glamour foreground", rgb + "body" + reset, "\x1b[38;2;230;226;211mbody" + reset, false},
		{"style on padding", rgb + "body " + reset, rgb + "body" + reset + " ", false},
		{"style across newline", rgb + "one\ntwo" + reset, rgb + "one" + reset + "\ntwo", false},
		{"final pen state", fg + "code" + reset, fg + "code", false},
		{"hyperlink", "\x1b]8;;https://example.com/a\x1b\\link\x1b]8;;\x1b\\",
			"\x1b]8;;https://example.com/b\x1b\\link\x1b]8;;\x1b\\", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, b := normalizeMarkdownANSI(tt.a), normalizeMarkdownANSI(tt.b)
			if (a == b) != tt.equal {
				t.Fatalf("equal = %v, want %v\na: %q\nb: %q", a == b, tt.equal, a, b)
			}
		})
	}
}

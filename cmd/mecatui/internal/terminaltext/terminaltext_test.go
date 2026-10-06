package terminaltext

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "preserves printable layout", in: "a\tb\nc", want: "a\tb\nc"},
		{name: "strips C0 ESC and DEL", in: "a\x00\x1b[2J\x7fb", want: "a[2Jb"},
		{name: "strips C1", in: "a\u009b2J\u009dtitleb", want: "a2Jtitleb"},
		{name: "strips format characters", in: "a\u200b\u202e\u2066\ufeffb", want: "ab"},
		{name: "repairs raw C1 bytes", in: "a\x9b2J\x9dtitleb", want: "a�2J�titleb"},
		{name: "repairs incomplete UTF-8 with layout and controls", in: "a\xc3\n\x1b[2Jb", want: "a�\n[2Jb"},
		{name: "preserves valid replacement rune", in: "a�b", want: "a�b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeSingleLine(t *testing.T) {
	const input = "a\tb\nc\u2028d\u2029e\x00\x1b[2J\x7f\u009bf\u200bg"
	if got, want := Sanitize(input), "a\tb\nc\u2028d\u2029e[2Jfg"; got != want {
		t.Errorf("Sanitize(%q) = %q, want %q", input, got, want)
	}
	if got, want := SanitizeSingleLine(input), "abcde[2Jfg"; got != want {
		t.Errorf("SanitizeSingleLine(%q) = %q, want %q", input, got, want)
	}
	if got, want := SanitizeSingleLine("a\x9b2J"), "a�2J"; got != want {
		t.Errorf("SanitizeSingleLine(raw C1 byte) = %q, want %q", got, want)
	}
	if got, want := SanitizeSingleLine("a\x9b2J\x9dtitle\xc3\nb"), "a�2J�title�b"; got != want {
		t.Errorf("SanitizeSingleLine(invalid UTF-8) = %q, want %q", got, want)
	}
}

func TestNormalizeWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ASCII unchanged", in: "plain text", want: "plain text"},
		{name: "stable Unicode unchanged", in: "héllo", want: "héllo"},
		{name: "strips VS16", in: "⚠️", want: "⚠"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeWidth(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeWidth(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for rest := got; rest != ""; {
				cluster, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
				rest = strings.TrimPrefix(rest, cluster)
				if ansi.StringWidthWc(cluster) != ansi.StringWidth(cluster) {
					t.Errorf("cluster %q widths disagree: WcWidth=%d, GraphemeWidth=%d", cluster, ansi.StringWidthWc(cluster), ansi.StringWidth(cluster))
				}
			}
		})
	}
}

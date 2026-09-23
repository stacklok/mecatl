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

package blocks

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestDeliveryBodyForDisplay pins the operator-facing display transform: the
// recorded delivery note is fenced-untrusted with a provenance header (both
// machine markers for the model), and the card strips them to show only the
// fire's outcome body. A non-fenced note is returned verbatim (fail-soft).
func TestDeliveryBodyForDisplay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips fence + provenance header, keeps outcome body",
			raw: "<<<UNTRUSTED\n" +
				"[scheduled task recent-files (fire sched--recent-files-20260728-123104-8d4c829a) completed with stop reason: end_turn]\n" +
				"Working tree is clean. By most recent commit touching each file:\n\n1. foo.go\n2. bar.go\n" +
				"<<<UNTRUSTED",
			want: "Working tree is clean. By most recent commit touching each file:\n\n1. foo.go\n2. bar.go",
		},
		{
			name: "no-output body",
			raw: "<<<UNTRUSTED\n" +
				"[scheduled task ping (fire sched--ping-1) completed with stop reason: end_turn]\n" +
				"(no output)\n" +
				"<<<UNTRUSTED",
			want: "(no output)",
		},
		{
			name: "non-fenced note is returned verbatim (fail-soft)",
			raw:  "just some text\nwith lines",
			want: "just some text\nwith lines",
		},
		{
			name: "fenced note with no provenance header keeps its first line",
			raw: "<<<UNTRUSTED\n" +
				"an outcome with no header\nsecond line\n" +
				"<<<UNTRUSTED",
			want: "an outcome with no header\nsecond line",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DeliveryBodyForDisplay(tc.raw); got != tc.want {
				t.Errorf("DeliveryBodyForDisplay() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranscriptNoticeStylesPreserveWrappingAndProvenance(t *testing.T) {
	th := Theme{
		HarnessGlyph:  "◆",
		HarnessMarker: lipgloss.NewStyle().Foreground(lipgloss.Color("#665086")),
		HarnessLabel:  lipgloss.NewStyle().Foreground(lipgloss.Color("#665086")).Bold(true),
		HarnessText:   lipgloss.NewStyle().Foreground(lipgloss.Color("#40554a")),
		Muted:         lipgloss.NewStyle().Foreground(lipgloss.Color("#778899")).Italic(true),
		Warning:       lipgloss.NewStyle().Foreground(lipgloss.Color("#aa5500")),
	}
	const raw = "first 界 • body\nsecond\x1b[2J line wraps here"
	for _, width := range []int{0, 2, 3, 4, 8, 24} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			layout := SnapshotPlainLayout(PlainLayoutInput{Width: width})
			got := PrepareNotice(SnapshotNotice(NoticeInput{Text: raw}), layout, th)
			base := preparePlain([]string{th.HarnessMarker.Render("◆") + th.HarnessLabel.Render(" system"), wrapStyled(SanitizePlain(raw), th.HarnessText, width)}, 1)
			if ansi.Strip(got.Text()) != ansi.Strip(base.Text()) || !reflect.DeepEqual(got.Rows, base.Rows) {
				t.Fatalf("notice changed base wrapping/provenance: got %+v, base %+v", got, base)
			}
			if !strings.Contains(got.Text(), th.HarnessMarker.Render("◆")) ||
				!strings.Contains(got.Text(), "38;2;64;85;74") ||
				strings.Contains(got.Text(), "\x1b[3;") || strings.Contains(got.Text(), "\x1b[3m") ||
				strings.Contains(got.Text(), "\x1b[2J") {
				t.Fatalf("notice lost separate marker/nonitalic body or sanitization: %q", got.Text())
			}
			recovery := PrepareNotice(SnapshotNotice(NoticeInput{Text: raw, Recovery: true}), layout, th)
			oldRecovery := preparePlain([]string{wrapPrefixed("⚠ ", SanitizePlain(raw), th.Warning, width)}, 0)
			if !reflect.DeepEqual(recovery, oldRecovery) {
				t.Fatal("recovery warning changed")
			}
		})
	}
}

func TestTranscriptPlainCardsSanitizeRawBodies(t *testing.T) {
	body := lipgloss.NewStyle().Foreground(lipgloss.Color("#202d28"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#778899")).Italic(true)
	th := Theme{
		UserGlyph: "▶", UserMarker: body, UserLabel: body.Bold(true), UserRail: body, UserBody: body,
		HarnessGlyph: "◆", HarnessMarker: body, HarnessLabel: body.Bold(true), HarnessText: body, Statistics: body, Muted: muted,
	}
	baseUserBody := body.BorderStyle(lipgloss.NormalBorder()).BorderLeft(true).BorderForeground(body.GetForeground()).PaddingLeft(1)
	for _, fixture := range []struct{ name, raw, clean string }{
		{"controls", "body\x00\x07\r\x7f\u009b\u202e \x1b[2J\x1b]0;title\x07\n界 tail", "body [2J]0;title\n界 tail"},
		{"invalid UTF8", "body\xff\xc0\xaf\x00 tail\n界", "body��� tail\n界"},
	} {
		if got := SanitizePlain(fixture.raw); got != fixture.clean {
			t.Fatalf("%s fixture sanitizes to %q, want %q", fixture.name, got, fixture.clean)
		}
		for _, width := range []int{2, 8, 24, 80} {
			layout := SnapshotPlainLayout(PlainLayoutInput{Width: width})
			for _, card := range []struct {
				name      string
				got, base Prepared
			}{
				{"user", PrepareUser(SnapshotUser(UserInput{Text: fixture.raw}), layout, th),
					preparePlain([]string{th.UserLabel.Render("▶ you"), wrapStyled(fixture.clean, baseUserBody, width)}, 1)},
				{"notice", PrepareNotice(SnapshotNotice(NoticeInput{Text: fixture.raw}), layout, th),
					preparePlain([]string{th.HarnessMarker.Render("◆") + th.HarnessLabel.Render(" system"), wrapStyled(fixture.clean, body, width)}, 1)},
				{"statistics", PrepareTurnStat(SnapshotTurnStat(TurnStatInput{Text: fixture.raw}), layout, th),
					preparePlain([]string{wrapStyled(fixture.clean, muted, width)}, 0)},
			} {
				t.Run(fixture.name+"/"+strconv.Itoa(width)+"/"+card.name, func(t *testing.T) {
					out := card.got.Text()
					visible := ansi.Strip(out)
					if !utf8.ValidString(out) || SanitizePlain(visible) != visible ||
						strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]0;") {
						t.Fatalf("unsafe rendered body: %q", out)
					}
					if visible != ansi.Strip(card.base.Text()) || !reflect.DeepEqual(card.got.Rows, card.base.Rows) {
						t.Fatalf("sanitized body changed base wrapping/provenance: got %+v, base %+v", card.got, card.base)
					}
					var sourceBody strings.Builder
					for i, row := range card.got.Rows {
						if row.Text {
							sourceBody.WriteString(ansi.Strip(card.got.Lines[i]))
						}
					}
					bodyText := strings.NewReplacer("│", "", "◆", "").Replace(sourceBody.String())
					if strings.Join(strings.Fields(bodyText), "") != strings.Join(strings.Fields(fixture.clean), "") {
						t.Fatalf("meaningful sanitized source body lost: %q", bodyText)
					}
				})
			}
		}
	}
}

func TestTranscriptStatisticsNonitalicPreserveWrappingAndProvenance(t *testing.T) {
	th := Theme{
		Statistics: lipgloss.NewStyle().Foreground(lipgloss.Color("#52665b")),
		Muted:      lipgloss.NewStyle().Foreground(lipgloss.Color("#778899")).Italic(true),
	}
	const raw = "turn 1 · ↑1.2k ↓300 · 2.1s"
	for _, width := range []int{0, 2, 8, 24, 80} {
		layout := SnapshotPlainLayout(PlainLayoutInput{Width: width})
		got := PrepareTurnStat(SnapshotTurnStat(TurnStatInput{Text: raw}), layout, th)
		base := preparePlain([]string{wrapStyled(raw, th.Muted, width)}, 0)
		if ansi.Strip(got.Text()) != ansi.Strip(base.Text()) || !reflect.DeepEqual(got.Rows, base.Rows) {
			t.Fatalf("width %d: statistics changed base wrapping/provenance", width)
		}
		if !strings.Contains(got.Text(), "\x1b[38;2;82;102;91m") ||
			strings.Contains(got.Text(), "\x1b[3;") || strings.Contains(got.Text(), "\x1b[3m") {
			t.Fatalf("width %d: statistics lost semantic nonitalic style: %q", width, got.Text())
		}
	}
}

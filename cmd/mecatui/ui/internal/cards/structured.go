package cards

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// PlainLayoutInput is the caller-owned geometry used by non-Markdown cards.
type PlainLayoutInput struct {
	Width      int
	Expanded   bool
	ExpandMark string
	Dialect    uint32
}

// PlainLayout is an immutable geometry snapshot.
type PlainLayout struct {
	width      int
	expanded   bool
	expandMark string
	dialect    uint32
}

// SnapshotPlainLayout detaches plain-card preparation from renderer state.
func SnapshotPlainLayout(in PlainLayoutInput) PlainLayout {
	return PlainLayout{width: in.Width, expanded: in.Expanded, expandMark: strings.Clone(in.ExpandMark), dialect: in.Dialect}
}

// SanitizePlain removes terminal controls from plain card text while preserving
// layout newlines and tabs. Markdown must not pass through this function.
func SanitizePlain(s string) string {
	if !strings.ContainsFunc(s, plainControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if plainControl(r) {
			return -1
		}
		return r
	}, s)
}

func plainControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case unicode.Is(unicode.Cf, r):
		return true
	default:
		return false
	}
}

// UserInput is caller-owned user-prompt content.
type UserInput struct {
	Text  string
	Media []string
}

// UserSnapshot is an immutable user-prompt snapshot.
type UserSnapshot struct {
	text  string
	media []string
}

// UserAppearance contains resolved user-prompt styles.
type UserAppearance struct{ Label, Body, Muted lipgloss.Style }

// SnapshotUser owns a user-prompt input.
func SnapshotUser(in UserInput) UserSnapshot {
	return UserSnapshot{text: strings.Clone(in.Text), media: cloneStrings(in.Media)}
}

// PrepareUser prepares a user-prompt card.
func PrepareUser(in UserSnapshot, layout PlainLayout, a UserAppearance) Prepared {
	lines := []string{a.Label.Render("▌ you"), wrapStyled(SanitizePlain(in.text), a.Body, layout.width)}
	for _, media := range in.media {
		lines = append(lines, wrapPrefixed("📎 ", SanitizePlain(media), a.Muted, layout.width))
	}
	keyValues := append([]string{in.text}, in.media...)
	return preparePlain("user", layout, lines, keyValues, 1)
}

// NoticeInput is caller-owned notice content.
type NoticeInput struct {
	Text     string
	Recovery bool
}

// NoticeSnapshot is an immutable notice snapshot.
type NoticeSnapshot struct {
	text     string
	recovery bool
}

// NoticeAppearance contains resolved notice styles.
type NoticeAppearance struct{ Muted, Warning lipgloss.Style }

// SnapshotNotice owns a notice input.
func SnapshotNotice(in NoticeInput) NoticeSnapshot {
	return NoticeSnapshot{text: strings.Clone(in.Text), recovery: in.Recovery}
}

// PrepareNotice prepares an ordinary or recovery notice.
func PrepareNotice(in NoticeSnapshot, layout PlainLayout, a NoticeAppearance) Prepared {
	if in.recovery {
		return preparePlain("notice-recovery", layout, []string{wrapPrefixed("⚠ ", SanitizePlain(in.text), a.Warning, layout.width)}, []string{in.text}, 0)
	}
	return preparePlain("notice", layout, []string{wrapPrefixed("• ", SanitizePlain(in.text), a.Muted, layout.width)}, []string{in.text}, 0)
}

// HookInput is caller-owned structured hook content.
type HookInput struct{ Text, Phase, Tool, Decision string }

// HookSnapshot is an immutable hook snapshot.
type HookSnapshot struct{ text, phase, tool, decision string }

// HookAppearance contains resolved decision-specific hook styles.
type HookAppearance struct{ Muted, Error, Modified, Advisory lipgloss.Style }

// SnapshotHook owns a structured hook input.
func SnapshotHook(in HookInput) HookSnapshot {
	return HookSnapshot{strings.Clone(in.Text), strings.Clone(in.Phase), strings.Clone(in.Tool), strings.Clone(in.Decision)}
}

// PrepareHook prepares a decision-specific hook notice.
func PrepareHook(in HookSnapshot, layout PlainLayout, a HookAppearance) Prepared {
	label := "hook"
	if in.phase != "" {
		label = "hook " + SanitizePlain(in.phase)
	}
	if in.tool != "" {
		label += " · " + SanitizePlain(in.tool)
	}
	var line string
	switch in.decision {
	case "blocked":
		line = wrapPrefixed("✗ ", label+": blocked"+hookReason(in.text, in.phase), a.Error, layout.width)
	case "modified":
		line = wrapPrefixed("✎ ", label+": modified"+hookReason(in.text, in.phase), a.Modified, layout.width)
	case "advisory":
		line = wrapPrefixed("⚠ ", label+": advisory"+hookReason(in.text, in.phase), a.Advisory, layout.width)
	default:
		if in.text != "" {
			label += ": " + SanitizePlain(in.text)
		}
		line = wrapPrefixed("• ", label, a.Muted, layout.width)
	}
	return preparePlain("hook", layout, []string{line}, []string{in.text, in.phase, in.tool, in.decision}, 0)
}

func hookReason(raw, phase string) string {
	reason := strings.TrimSpace(stripPhaseEcho(raw, phase))
	if reason == "" {
		return ""
	}
	return " — " + SanitizePlain(reason)
}
func stripPhaseEcho(raw, phase string) string {
	s := strings.TrimSpace(raw)
	if phase == "" {
		return s
	}
	if strings.EqualFold(s, "blocked by "+phase+" hook") {
		return ""
	}
	for _, prefix := range []string{phase + " hook ", phase + " "} {
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			return strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}

// ErrorInput is caller-owned transient-error content.
type ErrorInput struct{ Text string }

// ErrorSnapshot is an immutable transient-error snapshot.
type ErrorSnapshot struct{ text string }

// ErrorAppearance contains the resolved transient-error style.
type ErrorAppearance struct{ Error lipgloss.Style }

// SnapshotError owns a transient-error input.
func SnapshotError(in ErrorInput) ErrorSnapshot { return ErrorSnapshot{text: strings.Clone(in.Text)} }

// PrepareError prepares a transient error.
func PrepareError(in ErrorSnapshot, layout PlainLayout, a ErrorAppearance) Prepared {
	return preparePlain("error", layout, []string{wrapPrefixed("✗ ", SanitizePlain(in.text), a.Error, layout.width)}, []string{in.text}, 0)
}

// PermanentErrorInput is caller-owned permanent-error content.
type PermanentErrorInput struct{ Text string }

// PermanentErrorSnapshot is an immutable permanent-error snapshot.
type PermanentErrorSnapshot struct{ text string }

// PermanentErrorAppearance contains resolved permanent-error styles.
type PermanentErrorAppearance struct{ Error, Muted lipgloss.Style }

// SnapshotPermanentError owns a permanent-error input.
func SnapshotPermanentError(in PermanentErrorInput) PermanentErrorSnapshot {
	return PermanentErrorSnapshot{text: strings.Clone(in.Text)}
}

// PreparePermanentError prepares collapsed or expanded permanent-error content.
func PreparePermanentError(in PermanentErrorSnapshot, layout PlainLayout, a PermanentErrorAppearance) Prepared {
	summary := PermanentErrorSummary(in.text)
	lines := []string{wrapPrefixed("✗ ", summary, a.Error, layout.width)}
	if layout.expanded {
		lines = append(lines, a.Muted.Render("raw payload:"), wrapStyled(SanitizePlain(in.text), a.Muted, layout.width))
	} else {
		lines = append(lines, a.Muted.Render("  "+layout.expandMark+" shows details"))
	}
	return preparePlain("permanent-error", layout, lines, []string{in.text}, 0)
}

// PermanentErrorSummary preserves compatibility with legacy SDK-shaped errors.
func PermanentErrorSummary(raw string) string {
	first := firstLineCap(CollapseErrorSummary(raw), 120)
	if first == "" {
		return "permanent provider error — retrying won't help; the request is rejected. Start a new session."
	}
	return SanitizePlain(first) + " — retrying won't help; the request is rejected. Start a new session."
}

// CollapseErrorSummary rewrites a legacy SDK-shaped provider error.
func CollapseErrorSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, `": `); i >= 0 && strings.HasPrefix(s, `POST "`) {
		s = strings.TrimSpace(s[i+3:])
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		head, tail := strings.TrimRight(s[:i], " :"), s[i:]
		if msg := extractJSONMessage(tail); msg != "" {
			if head != "" {
				return head + ": " + msg
			}
			return msg
		}
		return head
	}
	return s
}
func extractJSONMessage(s string) string {
	var env map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &env) != nil {
		return ""
	}
	if raw, ok := env["error"]; ok {
		if json.Unmarshal(raw, &env) != nil {
			return ""
		}
	}
	if raw, ok := env["message"]; ok {
		var msg string
		if json.Unmarshal(raw, &msg) == nil {
			return strings.TrimSpace(msg)
		}
	}
	return ""
}
func firstLineCap(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if first, _, ok := strings.Cut(s, "\n"); ok {
		s = first
	}
	if rs := []rune(s); len(rs) > maxRunes {
		s = string(rs[:maxRunes])
	}
	return s
}

// DeliveryInput is caller-owned scheduled-delivery content.
type DeliveryInput struct{ ScheduleName, FireID, Text string }

// DeliverySnapshot is an immutable delivery snapshot.
type DeliverySnapshot struct{ scheduleName, fireID, text string }

// DeliveryAppearance contains resolved delivery styles.
type DeliveryAppearance struct{ Header, Body lipgloss.Style }

// SnapshotDelivery owns a scheduled-delivery input.
func SnapshotDelivery(in DeliveryInput) DeliverySnapshot {
	return DeliverySnapshot{strings.Clone(in.ScheduleName), strings.Clone(in.FireID), strings.Clone(in.Text)}
}

// PrepareDelivery prepares a scheduled-delivery card.
func PrepareDelivery(in DeliverySnapshot, layout PlainLayout, a DeliveryAppearance) Prepared {
	label := "⏰ scheduled task " + SanitizePlain(in.scheduleName) + " — delivery"
	if in.fireID != "" {
		label += " · fire " + SanitizePlain(in.fireID)
	}
	lines := []string{wrapPrefixed("", label, a.Header, layout.width), wrapPrefixed("│ ", SanitizePlain(DeliveryBodyForDisplay(in.text)), a.Body, layout.width)}
	return preparePlain("delivery", layout, lines, []string{in.scheduleName, in.fireID, in.text}, 0)
}

// DeliveryBodyForDisplay strips only the recognized delivery fence and header.
func DeliveryBodyForDisplay(raw string) string {
	const fence = "<<<UNTRUSTED"
	s, ok := strings.CutPrefix(raw, fence)
	if !ok {
		return raw
	}
	s = strings.TrimPrefix(s, "\n")
	if idx := strings.LastIndex(s, "\n"+fence); idx >= 0 {
		s = s[:idx]
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 && strings.HasPrefix(s, "[scheduled task ") {
		s = s[nl+1:]
	}
	return s
}

// TurnStatInput is caller-owned turn-stat content.
type TurnStatInput struct{ Text string }

// TurnStatSnapshot is an immutable turn-stat snapshot.
type TurnStatSnapshot struct{ text string }

// TurnStatAppearance contains the resolved turn-stat style.
type TurnStatAppearance struct{ Muted lipgloss.Style }

// SnapshotTurnStat owns a turn-stat input.
func SnapshotTurnStat(in TurnStatInput) TurnStatSnapshot {
	return TurnStatSnapshot{text: strings.Clone(in.Text)}
}

// PrepareTurnStat prepares a turn-stat line.
func PrepareTurnStat(in TurnStatSnapshot, layout PlainLayout, a TurnStatAppearance) Prepared {
	return preparePlain("turn-stat", layout, []string{wrapStyled(SanitizePlain(in.text), a.Muted, layout.width)}, []string{in.text}, 0)
}

func wrapStyled(text string, style lipgloss.Style, width int) string {
	frame := style.GetHorizontalFrameSize()
	if width > frame+1 {
		text = ansi.Wrap(normalizePlainWidth(text), width-frame, "")
	}
	return style.Render(text)
}
func wrapPrefixed(prefix, body string, style lipgloss.Style, width int) string {
	pw := lipgloss.Width(prefix)
	if width <= pw+1 {
		return style.Render(prefix + body)
	}
	rows := strings.Split(ansi.Wrap(normalizePlainWidth(body), width-pw, ""), "\n")
	for i := range rows {
		if i == 0 {
			rows[i] = prefix + rows[i]
		} else {
			rows[i] = strings.Repeat(" ", pw) + rows[i]
		}
	}
	return style.Render(strings.Join(rows, "\n"))
}

func normalizePlainWidth(src string) string {
	if !strings.ContainsFunc(src, func(r rune) bool { return r > 0x7f }) {
		return src
	}
	var out strings.Builder
	out.Grow(len(src))
	for rest := src; rest != ""; {
		cluster, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(cluster):]
		if ansi.StringWidthWc(cluster) == ansi.StringWidth(cluster) {
			out.WriteString(cluster)
			continue
		}
		stripped := strings.ReplaceAll(cluster, "\ufe0f", "")
		if ansi.StringWidthWc(stripped) == ansi.StringWidth(stripped) {
			out.WriteString(stripped)
			continue
		}
		first, _ := utf8.DecodeRuneInString(stripped)
		candidate := string(first)
		if ansi.StringWidthWc(candidate) == ansi.StringWidth(candidate) {
			out.WriteString(candidate)
		} else {
			out.WriteRune('�')
		}
	}
	return out.String()
}

func preparePlain(family string, layout PlainLayout, chunks, keyValues []string, firstTextRow int) Prepared {
	lines := strings.Split(strings.Join(chunks, "\n"), "\n")
	rows := make([]Row, len(lines))
	offset := 0
	for i, line := range lines {
		rows[i] = Row{Region: RegionChrome, FallbackRow: i}
		if i < firstTextRow {
			continue
		}
		span := graphemeCount(ansi.Strip(line))
		rows[i] = Row{
			Region: RegionBody, Text: true, SourceOffset: offset,
			FallbackRow: i, GraphemeSpan: span,
		}
		offset += span
	}
	var encoded canonicalEncoder
	encoded.string("mecatui.cards.plain/" + family + "/v1")
	encoded.int(layout.width)
	encoded.bool(layout.expanded)
	encoded.string(layout.expandMark)
	encoded.uint32(layout.dialect)
	encoded.strings(keyValues)
	for _, chunk := range chunks {
		encoded.string(chunk)
	}
	return Prepared{Key: sha256Sum(encoded.bytes), Lines: lines, Rows: rows}
}
func sha256Sum(data []byte) [32]byte { return sha256.Sum256(data) }

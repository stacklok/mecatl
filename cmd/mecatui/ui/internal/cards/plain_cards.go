package cards

import (
	"strings"

	"charm.land/lipgloss/v2"
)

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

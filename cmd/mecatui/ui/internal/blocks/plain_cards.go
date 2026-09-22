package blocks

import "strings"

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

// SnapshotUser owns a user-prompt input.
func SnapshotUser(in UserInput) UserSnapshot {
	return UserSnapshot{text: strings.Clone(in.Text), media: cloneStrings(in.Media)}
}

// PrepareUser prepares a user-prompt block.
func PrepareUser(in UserSnapshot, layout PlainLayout, theme Theme) Prepared {
	lines := []string{theme.UserLabel.Render("▌ you"), wrapStyled(SanitizePlain(in.text), theme.UserBody, layout.width)}
	for _, media := range in.media {
		lines = append(lines, wrapPrefixed("📎 ", SanitizePlain(media), theme.Muted, layout.width))
	}
	return preparePlain(lines, 1)
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

// SnapshotNotice owns a notice input.
func SnapshotNotice(in NoticeInput) NoticeSnapshot {
	return NoticeSnapshot{text: strings.Clone(in.Text), recovery: in.Recovery}
}

// PrepareNotice prepares an ordinary or recovery notice.
func PrepareNotice(in NoticeSnapshot, layout PlainLayout, theme Theme) Prepared {
	if in.recovery {
		return preparePlain([]string{wrapPrefixed("⚠ ", SanitizePlain(in.text), theme.Warning, layout.width)}, 0)
	}
	return preparePlain([]string{wrapPrefixed("• ", SanitizePlain(in.text), theme.Muted, layout.width)}, 0)
}

// HookInput is caller-owned structured hook content.
type HookInput struct{ Text, Phase, Tool, Decision string }

// HookSnapshot is an immutable hook snapshot.
type HookSnapshot struct{ text, phase, tool, decision string }

// SnapshotHook owns a structured hook input.
func SnapshotHook(in HookInput) HookSnapshot {
	return HookSnapshot{strings.Clone(in.Text), strings.Clone(in.Phase), strings.Clone(in.Tool), strings.Clone(in.Decision)}
}

// PrepareHook prepares a decision-specific hook notice.
func PrepareHook(in HookSnapshot, layout PlainLayout, theme Theme) Prepared {
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
		line = wrapPrefixed("✗ ", label+": blocked"+hookReason(in.text, in.phase), theme.Error, layout.width)
	case "modified":
		line = wrapPrefixed("✎ ", label+": modified"+hookReason(in.text, in.phase), theme.HookModified, layout.width)
	case "advisory":
		line = wrapPrefixed("⚠ ", label+": advisory"+hookReason(in.text, in.phase), theme.HookAdvisory, layout.width)
	default:
		if in.text != "" {
			label += ": " + SanitizePlain(in.text)
		}
		line = wrapPrefixed("• ", label, theme.Muted, layout.width)
	}
	return preparePlain([]string{line}, 0)
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

// SnapshotDelivery owns a scheduled-delivery input.
func SnapshotDelivery(in DeliveryInput) DeliverySnapshot {
	return DeliverySnapshot{strings.Clone(in.ScheduleName), strings.Clone(in.FireID), strings.Clone(in.Text)}
}

// PrepareDelivery prepares a scheduled-delivery card.
func PrepareDelivery(in DeliverySnapshot, layout PlainLayout, theme Theme) Prepared {
	label := "⏰ scheduled task " + SanitizePlain(in.scheduleName) + " — delivery"
	if in.fireID != "" {
		label += " · fire " + SanitizePlain(in.fireID)
	}
	lines := []string{wrapPrefixed("", label, theme.DeliveryHead, layout.width), wrapPrefixed("│ ", SanitizePlain(DeliveryBodyForDisplay(in.text)), theme.Muted, layout.width)}
	return preparePlain(lines, 0)
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

// SnapshotTurnStat owns a turn-stat input.
func SnapshotTurnStat(in TurnStatInput) TurnStatSnapshot {
	return TurnStatSnapshot{text: strings.Clone(in.Text)}
}

// PrepareTurnStat prepares a turn-stat line.
func PrepareTurnStat(in TurnStatSnapshot, layout PlainLayout, theme Theme) Prepared {
	return preparePlain([]string{wrapStyled(SanitizePlain(in.text), theme.Muted, layout.width)}, 0)
}

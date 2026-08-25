package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// effort.go is the /effort picker — a tiny SELECTING overlay (cursor + enter) for
// the reasoning-effort tier (ADR 0055). Enter on a tier applies DIRECTLY via a
// FORK-RESUME (ADR 0068): the server forks the session's conversation onto a peer
// session at the new effort, so the transcript SURVIVES — no confirm step (the
// switch is non-destructive, so it is never a teardown warning), no wipe. It is
// much simpler than /models: a FIXED enum, no filter; the only RPC is the fork
// itself. Like /models it renders purely from client state (no proto in ui) and is
// idle-only / esc-dismissable.
//
// # Vocab + the auto sentinel
//
// The neutral picker enum (in order) is auto, low, medium, high, xhigh, max. "auto"
// means UNSET — operator/provider default — and is sent to the server as the EMPTY
// string (client.ModelSelection.ReasoningEffort == ""). The picker presents it as
// "auto" for readability but maps it to "" when building the selection, so the
// persisted state stays clean (an unset effort is absent from models.yaml, not the
// literal "auto"). effortValue does that mapping; effortLabel does the inverse for
// display. The server normalises/clamps anyway (e.g. openai "max" echoes "high"),
// and the LIVE display reads m.resolvedSessionModel.ReasoningEffort (the resolved value),
// so the picker only ever needs to send the operator's REQUEST.

// effortView is the active /effort overlay (none = closed). Idle-only, esc-dismissed.
type effortView int

const (
	effortNone  effortView = iota // overlay closed
	effortPanel                   // the enum picker
)

// effortAuto is the picker label for the UNSET tier; it maps to the empty string on
// the wire and in persisted state (see the package header). Named once so the label
// list, effortValue, and effortLabel share one spelling.
const effortAuto = "auto"

// effortTiers is the neutral reasoning-effort vocabulary in picker order. "auto" is
// the unset sentinel (⇒ ""); the rest are the real tiers the server understands.
// FIXED order, locked by a test so the picker layout is stable.
//
// AUTHORITY: the canonical neutral vocabulary lives in composition —
// internal/app/reasoning_effort.go (validReasoningEfforts / NormalizeReasoningEffort).
// This list is the TUI's display copy (the ui imports no engine/internal package); it
// must stay in sync with that set. The server normalises/clamps whatever the picker
// sends, so a drift here degrades gracefully (an unknown token fail-softs to unset),
// but keep the two aligned.
var effortTiers = []string{effortAuto, "low", "medium", "high", "xhigh", "max"}

// effortState holds the /effort overlay state on the Model. Value-embedded so the
// Model stays a plain struct Update copies.
type effortState struct {
	view   effortView
	cursor int // index into effortTiers (clamped to its bounds)
}

// effortValue maps a picker label to the wire/state value: the auto sentinel becomes
// "" (unset), every other tier is itself. This is the SINGLE place the "auto"→""
// convention is applied when building a selection.
func effortValue(label string) string {
	if label == effortAuto {
		return ""
	}
	return label
}

// effortLabel is the inverse: the empty (unset) value displays as the auto sentinel,
// every other value is itself. Used to mark the current row and to find the cursor's
// initial position.
func effortLabel(value string) string {
	if value == "" {
		return effortAuto
	}
	return value
}

// openEffort opens the picker, positioning the cursor on the CURRENT effective effort
// (resolved by the server, m.resolvedSessionModel.ReasoningEffort) so the current tier is
// pre-selected. Only callable while idle and when model selection is available (the
// effort is a per-session server setting that only matters with a selectable model);
// returns the model unchanged otherwise. Unlike /models it fires no RPC (the enum is
// fixed and client-owned).
func (m Model) openEffort() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || !m.caps.ModelSelection {
		return m, nil
	}
	m.ta.Blur() // overlay owns the keyboard while open
	m.effort.view = effortPanel
	m.effort.cursor = effortCursorFor(m.resolvedSessionModel.ReasoningEffort)
	return m, nil
}

// effortCursorFor returns the effortTiers index whose label matches the current
// (effective) effort value, or 0 (auto) when none matches (an unknown/unset effort
// lands on auto, the safe default).
func effortCursorFor(current string) int {
	want := effortLabel(current)
	for i, tier := range effortTiers {
		if tier == want {
			return i
		}
	}
	return 0
}

// currentModelNoReasoning reports whether the CURRENT effective model is KNOWN (from
// the loaded /models inventory) to NOT support reasoning — so a picked effort tier
// would be DROPPED by the server's capability gate (ADR 0055). It is the TUI half of
// that gate's acknowledgement (UX): the picker warns up front so a user who restarts
// the session for an effort they can't get is not left guessing (the server-side
// degrade only logs). It is CONSERVATIVE — true ONLY when the model is found in the
// inventory with Reasoning==false; an unknown/unloaded model returns false (fail-open,
// matching the server's unknown=capable posture), so the warning never cries wolf.
func (m Model) currentModelNoReasoning() bool {
	id, pid := m.resolvedSessionModel.ModelID, m.resolvedSessionModel.ProviderID
	if id == "" {
		return false // no resolved model yet → say nothing
	}
	for _, mi := range m.modelCatalog.models {
		if mi.ID == id && mi.ProviderID == pid {
			return !mi.Reasoning
		}
	}
	return false // not in the inventory → unknown → fail-open (no warning)
}

// closeEffort dismisses the overlay and returns focus to the prompt input.
func (m Model) closeEffort() (tea.Model, tea.Cmd) {
	m.effort.view = effortNone
	cmd := m.ta.Focus()
	return m, cmd
}

// onEffortKey routes key presses while the picker is open. The enum is short, so the
// routing is the simple cursor model (no filter input to feed): arrows/j/k move,
// enter selects, esc closes. Every key is handled=true (the open picker swallows
// keys). Returns handled=false only when the overlay is closed (so onOverlayKey
// falls through to the next handler).
func (m Model) onEffortKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.effort.view == effortNone {
		return m, nil, false
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		mm, cmd := m.closeEffort()
		return mm, cmd, true
	case key.Matches(msg, m.keys.Up):
		if m.effort.cursor > 0 {
			m.effort.cursor--
		}
		return m, nil, true
	case key.Matches(msg, m.keys.Down):
		if m.effort.cursor < len(effortTiers)-1 {
			m.effort.cursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.Choose):
		return m.chooseEffort()
	}
	// Any other key is swallowed (handled=true) so stray input can't leak to the
	// prompt while the overlay owns the keyboard.
	return m, nil, true
}

// chooseEffort handles enter on the cursor row: it builds a selection carrying the
// CURRENT provider/model (so the model is PRESERVED — only the effort changes) plus
// the picked effort, then fires the switchEffort FORK-RESUME handoff DIRECTLY (ADR
// 0068) — no confirm step: the fork is non-destructive (the transcript survives on
// the peer session), so there is nothing to warn about. A cursor past the enum end
// is a no-op (defensive).
func (m Model) chooseEffort() (tea.Model, tea.Cmd, bool) {
	if m.effort.cursor < 0 || m.effort.cursor >= len(effortTiers) {
		return m, nil, true
	}
	sel := m.effortSelection(effortValue(effortTiers[m.effort.cursor]))
	// Dismiss the effort overlay BEFORE the handoff (it drives phaseConnecting) so a
	// stale overlay flag can't survive the transition; focus returns to the prompt.
	m.effort.view = effortNone
	return m.switchEffort(sel)
}

// effortSelection builds the ModelSelection for a restart that changes ONLY the
// reasoning effort: it carries the CURRENT session's provider/model (the effective
// model the server resolved, falling back to the pending createModelSelection) so the model
// is preserved across the restart, with the new effort applied. When no model is
// known yet (older server / mid-connect) it carries the bare effort over the
// server-default provider — meaningful on its own (ADR 0055: effort rides the
// server-default provider), so this is not a zero selection.
func (m Model) effortSelection(effort string) client.ModelSelection {
	sel := client.ModelSelection{
		ProviderID: m.resolvedSessionModel.ProviderID,
		ModelID:    m.resolvedSessionModel.ModelID,
	}
	if sel.ProviderID == "" && sel.ModelID == "" {
		// No server-resolved model yet — fall back to the pending next selection so a
		// pre-connect effort change still preserves whatever model is queued.
		sel.ProviderID = m.createModelSelection.ProviderID
		sel.ModelID = m.createModelSelection.ModelID
	}
	sel.ReasoningEffort = effort
	return sel
}

// renderEffortOverlay draws the picker centred over the conversation region via
// centerCard. current is the effective effort the server resolved (the ● marker
// target). Returns "" when the overlay is closed.
func renderEffortOverlay(th theme.Theme, st effortState, current string, noReasoning bool, hk helpKeys, width, height int) string {
	if st.view == effortPanel {
		return centerCard(th, renderEffortPanel(th, st, current, noReasoning, hk), width, height)
	}
	return ""
}

// renderEffortPanel renders the fixed enum: a title, one row per tier with the cursor
// row highlighted and a ● marker on the CURRENT (effective) tier, then a one-line
// footer hint. The enum is short + ASCII-safe, so no windowing/sanitization is
// needed (every string is an internal const). current is the effective effort
// (server-resolved); the ● tracks effortLabel(current).
func renderEffortPanel(th theme.Theme, st effortState, current string, noReasoning bool, hk helpKeys) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Reasoning effort") + "\n\n")
	currentLabel := effortLabel(current)
	for i, tier := range effortTiers {
		marker := "  "
		if tier == currentLabel {
			marker = "● "
		}
		b.WriteString(renderRow(th, marker+effortRowText(tier), i == st.cursor) + "\n")
	}
	// UX (ADR 0055): when the current model is KNOWN to lack reasoning support, warn
	// that a chosen tier will be ignored — so a switch for an unattainable effort is
	// acknowledged in the picker, not only in the (invisible) server log.
	if noReasoning {
		b.WriteString("\n" + th.Style("warning").Render("this model has no reasoning support — a tier will be ignored"))
	}
	// The nav/select/close chords read the LIVE Up/Down/Choose/Close markings
	// (issue #457); with defaults the hint is byte-identical to the historical literal.
	b.WriteString("\n" + th.Style("muted").Render(hk.navUp+"/"+hk.navDown+" move · "+hk.choose+" apply · "+hk.closeOnly+" close"))
	b.WriteString("\n" + th.Style("muted").Render("● current  ·  auto = provider default"))
	return b.String()
}

// effortRowText is the per-tier row label: the tier name plus a short hint for the
// sentinel so "auto" is self-explanatory in the list. ASCII-safe, layout-stable.
func effortRowText(tier string) string {
	if tier == effortAuto {
		return effortAuto + "    (operator / provider default)"
	}
	return tier
}

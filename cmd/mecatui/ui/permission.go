package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// pendingAsk holds the state of an open permission modal. AskID is the exact
// correlation key sent back in ResumeApproval — it is never derived from the
// tool name. focus tracks which button is highlighted (0=allow-once, 1=always,
// 2=deny). offerAlways gates the middle "always" button: it is offered only for
// the MAIN agent's asks, never for a surfaced subagent ask (a child engine's
// permission policy has a nil learn store, so always-allow would be a silent
// no-op there).
type pendingAsk struct {
	AskID       string
	Tool        string
	Args        string
	Reason      string
	focus       int
	offerAlways bool
}

// isPlanAsk reports whether a permission ask is a plan-approval gate (the model
// called PresentPlan in plan mode). The tool name is the sole discriminator —
// no new proto field is needed.
func isPlanAsk(tool string) bool {
	return tool == "PresentPlan"
}

// renderPermissionModal renders the centred approval card. It is drawn with
// lipgloss.Place over the available area so it reads as a modal overlay. The
// warning border + accent on the focused button make it unmissable. It is a
// method on renderer so it can reuse renderToolDiff: for an Edit/Write ask the
// concrete colourised diff of the change is shown in place of the raw JSON args,
// so the operator approves a real edit rather than an opaque blob. expand is the
// global details toggle (ctrl+t): when on, the diff renders in full instead of
// line-capped, so the collapse marker's "ctrl+t expand" hint is truthful — the
// operator can genuinely reveal every line being authorized before deciding.
// queued is the number of asks waiting FIFO behind this one (len(m.askQueue)):
// when non-zero the title carries a "(1 of N)" badge so the operator knows more
// approvals follow; at zero the modal is byte-identical to the single-ask frame.
func (r *renderer) renderPermissionModal(ask pendingAsk, expand bool, queued, width, height int) string {
	th := r.th
	titleText := "Permission required"
	if queued > 0 {
		titleText = fmt.Sprintf("Permission required (1 of %d)", queued+1)
	}
	title := th.Style("askTitle").Render(titleText)

	// All ask.* fields are server-derived and rendered via lipgloss, so they MUST
	// be terminal-sanitized: an attacker who controls a tool result could
	// otherwise embed escapes to redraw/spoof this very approval modal. (Args is
	// sanitized inside prettyJSON; the diff path sanitizes internally.)
	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(th.Style("toolName").Render(sanitizeTerminal(ask.Tool)) + "\n")
	// Prefer a concrete diff for Edit/Write. Collapsed by default (line-capped, so
	// a huge Write can't grow the modal off-screen); ctrl+t (expand) reveals the
	// full diff right here at the gate. Fall back to pretty JSON for any other
	// tool, or when the Edit/Write args don't parse into the expected shape.
	if diff, ok := r.renderToolDiff(ask.Tool, ask.Args, expand); ok {
		if diff != "" {
			b.WriteString(th.Style("muted").Render("changes:") + "\n")
			b.WriteString(diff + "\n")
		}
	} else if args := prettyJSON(ask.Args); args != "" {
		b.WriteString(th.Style("toolArgs").Render(args) + "\n")
	}
	if ask.Reason != "" {
		b.WriteString("\n" + th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n")
	}

	// Three buttons when always-allow is offered (main-agent asks), two otherwise
	// (surfaced subagent asks). focus indexes {allow-once, always, deny}; for a
	// two-button modal focus only ever takes 0 (allow) or 2 (deny).
	btnStyle := func(idx int) string {
		if ask.focus == idx {
			return "askButtonActive"
		}
		return "askButton"
	}
	allow := th.Style(btnStyle(0)).Render("[A]llow")
	deny := th.Style(btnStyle(2)).Render("[D]eny")
	var buttons string
	if ask.offerAlways {
		always := th.Style(btnStyle(1)).Render("Al[w]ays")
		buttons = lipgloss.JoinHorizontal(lipgloss.Top, allow, "  ", always, "  ", deny)
	} else {
		buttons = lipgloss.JoinHorizontal(lipgloss.Top, allow, "  ", deny)
	}
	b.WriteString("\n" + buttons)
	if ask.offerAlways {
		b.WriteString("\n" + th.Style("muted").Render("al[w]ays allows this exact command for the rest of this session"))
	}

	return centerCard(th, b.String(), width, height)
}

// renderPlanApprovalModal renders the plan-review gate when a PresentPlan ask
// parks the run. It is a DISTINCT surface from the generic permission modal:
// "Plan ready for review" title, model info, and plan-specific button copy.
// effectiveModel is the CURRENT session's resolved model (the plan model, since
// plan mode runs on the plan provider; the execute model is not separately
// echoed to the TUI, so the footer line names the session default).
func (r *renderer) renderPlanApprovalModal(ask pendingAsk, _ bool, queued int,
	width, height int, effectiveModel string,
) string {
	th := r.th
	titleText := "Plan ready for review"
	if queued > 0 {
		titleText = fmt.Sprintf("Plan ready for review (1 of %d)", queued+1)
	}
	title := th.Style("askTitle").Render(titleText)

	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(th.Style("toolName").Render(sanitizeTerminal(ask.Tool)) + "\n")

	// Show the plan model. The execute model is not separately echoed to the
	// TUI — show a graceful line naming the default model when known, or a
	// generic "session default model" when unknown.
	modelLine := "execute model: session default model"
	if effectiveModel != "" {
		modelLine = fmt.Sprintf("plan model: %s · execute model: session default model", sanitizeTerminal(effectiveModel))
	}
	b.WriteString(th.Style("muted").Render(modelLine) + "\n")

	if ask.Reason != "" {
		b.WriteString("\n" + th.Style("muted").Render(sanitizeTerminal(ask.Reason)) + "\n")
	}

	// Three buttons: allow-once (approve & run → default mode), always (auto-
	// accept edits → accept-edits mode), deny (iterate → stay in plan mode).
	// Always is offered only for the main agent (offerAlways); for a surfaced
	// child ask (which never reaches plan mode in practice) it is absent and the
	// button copy strips "auto-accept".
	btnStyle := func(idx int) string {
		if ask.focus == idx {
			return "askButtonActive"
		}
		return "askButton"
	}
	approve := th.Style(btnStyle(0)).Render("[A]pprove & run")
	denyBtn := th.Style(btnStyle(2)).Render("[D] iterate")
	var buttons string
	if ask.offerAlways {
		always := th.Style(btnStyle(1)).Render("[W] auto-accept edits")
		buttons = lipgloss.JoinHorizontal(lipgloss.Top, approve, "  ", always, "  ", denyBtn)
	} else {
		buttons = lipgloss.JoinHorizontal(lipgloss.Top, approve, "  ", denyBtn)
	}
	b.WriteString("\n" + buttons)
	if ask.offerAlways {
		b.WriteString("\n" + th.Style("muted").Render(
			"auto-accept allows every edit in the execution phase for the rest of this session"))
	}

	return centerCard(th, b.String(), width, height)
}

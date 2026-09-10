package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
)

// keyMap is the mecatui key binding set. Submit/newline are distinguished in
// Update via msg.String() ("enter" vs "shift+enter") because Bubble Tea v2
// reports them as distinct KeyPressMsg strings. The approval keys are only live
// while the permission modal is open; the cancel key only while a run is active.
type keyMap struct {
	Submit  key.Binding
	Newline key.Binding
	Cancel  key.Binding
	// ClearPrompt clears the unsent draft, including draft-local staged media and
	// large-paste placeholders. It is live wherever the prompt accepts input.
	ClearPrompt key.Binding
	// EditBack (↑) pulls the merged staged follow-up queue back into the textarea for
	// editing. It is consulted ONLY on an EMPTY input line with a non-empty queue (see
	// onRunningKey / onIdleKey), so ↑ over a draft stays a plain textarea/scroll key; it
	// is non-destructive (the queue is moved into the input, not dropped). It is live
	// both mid-run and while a paused queue is held.
	EditBack key.Binding
	// Paste (ctrl+v) reads the OS clipboard: an image stages as an inline media
	// attachment ([Image #N]), text inserts into the prompt. Distinct from a
	// bracketed paste (tea.PasteMsg, handled by onPaste) which never reaches here.
	Paste key.Binding
	// SelectAll and CopySelection are prompt-local actions. CopySelection is
	// transported by the app so it can share the shell fallback with scrollback.
	SelectAll     key.Binding
	CopySelection key.Binding
	Quit          key.Binding
	// QuitD (ctrl+d) is the unix EOF-habit quit: a SECOND double-press guard,
	// INDEPENDENT of Quit (ctrl+c). It quits only on an empty prompt — a populated
	// prompt keeps the chord in the textarea. The two guards never share armed state
	// (validator rule 6 also forbids them sharing a chord).
	QuitD key.Binding
	// Suspend (ctrl+z) suspends the whole TUI process to the shell (SIGTSTP via
	// Bubble Tea's tea.Suspend); fg resumes it. The embedded mecated + any in-flight
	// run KEEP RUNNING while suspended — the UI re-syncs on resume. Live in EVERY
	// phase (including the permission modal: the ask stays pending and durable).
	Suspend key.Binding
	Allow   key.Binding
	// AllowAlways (w) resolves the permission modal as always-allow: permit this
	// call AND learn a session-scoped rule so the exact command is not re-asked.
	// Offered only for the main agent's asks (never a surfaced subagent ask).
	AllowAlways key.Binding
	Deny        key.Binding
	ScrollU     key.Binding
	ScrollD     key.Binding
	// ScrollTop / ScrollBottom jump the conversation viewport to its top / bottom
	// (End naturally re-sticks auto-follow). These are DEDICATED scrollback keys
	// bound to "home"/"end" ONLY — deliberately NOT g/G, which must stay typeable
	// in prose. They are distinct from the team-overlay JumpTop/JumpEnd below
	// (home/g, end/G), which are consulted ONLY inside the agent-team overlay to
	// move the roster selection; these move the conversation scroll position.
	ScrollTop    key.Binding
	ScrollBottom key.Binding

	// ModeSwitch (shift+tab) cycles the session permission mode: default → plan →
	// accept-edits → default. Unlike Alt+M, Shift+Tab does not depend on a terminal
	// mapping macOS Option to Meta, and it never collides with prose input.
	ModeSwitch key.Binding

	// MCP overlay bindings. MCPPanel toggles the read-only inventory panel;
	// Resources / Prompts open the respective pickers. They are only live while
	// idle (no run streaming), like Submit. Inside an overlay, navigation reuses
	// the list keys below; esc closes the active overlay.
	MCPPanel  key.Binding
	Resources key.Binding
	Prompts   key.Binding
	Up        key.Binding
	Down      key.Binding
	Choose    key.Binding
	Close     key.Binding
	// Refresh re-issues the active overlay's primary fetch. It is a BARE 'r'
	// (not control-modified): it is only consulted while an overlay owns the
	// keyboard (onMCPKey intercepts before the idle ctrl+o/ctrl+r/f8 open
	// keys), so it never collides with the global ctrl+r resources binding nor
	// with the textarea (blurred while an overlay is open). Used by the inventory
	// panel to re-probe LIVE MCP source status.
	Refresh key.Binding

	// Tasks flips the f6 team overlay from the roster to the shared team task
	// sub-view (and back). Like Refresh it is a BARE 't' consulted ONLY inside the
	// overlay (onTeamRosterKey / the teamTasks branch intercept before any idle
	// open key), so it never collides with the textarea (blurred while the overlay
	// is open) nor with any global control binding.
	Tasks key.Binding

	// Findings flips the f6 team overlay from the roster to the shared team
	// findings-ledger sub-view (and back). Like Tasks it is a BARE 'f' consulted
	// ONLY inside the overlay (onTeamRosterKey / the teamFindings branch intercept
	// before any idle open key), so it never collides with the blurred textarea nor
	// any global control binding.
	Findings key.Binding

	// RawArgs toggles the full-screen ask-args view between the pretty tier (a
	// decoded Bash command / pretty-printed JSON) and the raw JSON tier. It is a
	// BARE 'r' consulted ONLY inside the full-screen ask-args view (onApprovalKey's
	// argsViewOpen branch intercepts before the verdict keys), so it never collides
	// with the textarea (blurred while the modal is open) nor with any global
	// control binding. It shares the Refresh overlay chord's default 'r' — the two
	// surfaces are disjoint (an overlay never owns the keyboard while the
	// permission modal is open), and the validator keeps an explicit rebind of
	// either from overlapping the other.
	RawArgs key.Binding

	// Jump bindings for the windowed agent-team roster (tedious to traverse with
	// ↑/↓ at the 20–32-member scale the overlay exists for): home/g jump to the
	// first member, end/G to the last. Page up/down reuse ScrollU/ScrollD (pgup/
	// pgdown) inside the overlay to move the selection by a window's worth.
	JumpTop key.Binding
	JumpEnd key.Binding

	// Agents (f6) opens the unified live agents overlay: ONE surface with three
	// tabs — Subagents (the flat Subagent-child fleet), Parallel (the fan-out groups),
	// and Teams (the agent-team roster with per-member focus). The default tab is
	// context-sensitive (a live team, else a live parallel run, else whichever
	// family has activity — see agents_overlay.go). Like the MCP bindings it is
	// control-modified so it never collides with textarea input. It is live both while
	// idle AND mid-run (Gap B) — the deep view is most useful while agents stream; it
	// stays inert under a permission modal. (Mapped from /team in the palette; /agents
	// is the def inventory, palette-only.)
	Agents key.Binding

	// NextTab (tab) switches the active tab inside the unified agents overlay
	// (Subagents↔Teams). Like Tasks/Findings/Refresh it is a BARE key consulted ONLY
	// inside the overlay (onAgentsKey intercepts before any idle open key), so it
	// never collides with the blurred textarea nor any global control binding.
	NextTab key.Binding

	// CancelChild (x) cancels the selected/focused subagent lane inside the f6
	// agents overlay (non-terminal lanes only). Like Tasks/Findings it is a BARE
	// key consulted ONLY inside the overlay, so it never collides with the blurred
	// textarea nor any global control binding. Confirm-less single keypress —
	// recoverable: the child is persisted and resumable.
	CancelChild key.Binding

	// ExpandTools is the general "show details" toggle: full vs line-capped
	// tool-result bodies + Edit/Write diffs, and collapsed vs expanded reasoning
	// summaries. Control-modified so it never collides with textarea input.
	ExpandTools key.Binding

	// Help opens the "?" keys-&-features overlay. Unlike the control-modified
	// open keys, "?" is a PRINTABLE rune, so onIdleKey opens help only when the
	// prompt input is EMPTY (otherwise "?" types into the textarea); inside the
	// overlay, "?" (or esc) closes it. An open MCP/agents overlay intercepts keys
	// before this binding is ever consulted, so "?" never opens help over another
	// overlay.
	Help key.Binding

	// Effort (f7) opens the /effort reasoning-effort picker (ADR 0055) — the
	// same surface the /effort command opens. Control-modified so it never collides
	// with textarea input. Like the /effort command it is idle-only and gated on
	// caps.ModelSelection: openEffort returns the model unchanged (the key falls
	// through inert) when model selection is unavailable, so f7 never opens an
	// empty picker. esc dismisses it via the shared onEffortKey overlay route.
	Effort key.Binding

	// SetGlobalDefault (ctrl+g) sets the /models picker's CURSOR row as the client
	// global default (the model new/unseen workspaces inherit). It is CONTROL-modified
	// deliberately: a bare 'g' is ScrollTop/JumpTop (key.Matches), and the picker's
	// filter input is focused, so a bare 'g' must stay typeable in a model name
	// ("gemini"/"gpt"). Consulted ONLY inside the /models picker (onModelsKey), so it
	// never collides with the conversation scrollback or the blurred textarea.
	SetGlobalDefault key.Binding
}

// defaultKeys returns the standard bindings.
func defaultKeys() keyMap {
	return keyMap{
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "send"),
		),
		Newline: key.NewBinding(
			key.WithKeys("shift+enter", "ctrl+j"),
			key.WithHelp("shift+enter", "newline"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel run"),
		),
		ClearPrompt: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("ctrl+u", "clear prompt"),
		),
		EditBack: key.NewBinding(
			key.WithKeys("up"),
			key.WithHelp("↑", "edit queued"),
		),
		Paste: key.NewBinding(
			key.WithKeys("ctrl+v"),
			key.WithHelp("ctrl+v", "paste image"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("ctrl+g"),
			key.WithHelp("ctrl+g", "select all"),
		),
		CopySelection: key.NewBinding(
			// ctrl+shift+c is unreachable in most terminals: classic Ctrl+<letter>
			// control-code encoding has no separate bit for Shift, so many
			// terminals (and tmux) cannot distinguish it from plain ctrl+c
			// (Quit). ctrl+y (yank, the classic terminal/vim copy mnemonic) is
			// a plain single-modifier chord every terminal can send.
			key.WithKeys("ctrl+y"),
			key.WithHelp("ctrl+y", "copy selection"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
		QuitD: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "quit (EOF)"),
		),
		Suspend: key.NewBinding(
			key.WithKeys("ctrl+z"),
			key.WithHelp("ctrl+z", "suspend"),
		),
		Allow: key.NewBinding(
			key.WithKeys("a", "y", "enter"),
			key.WithHelp("a", "allow"),
		),
		AllowAlways: key.NewBinding(
			key.WithKeys("w"),
			key.WithHelp("w", "always allow (session)"),
		),
		Deny: key.NewBinding(
			key.WithKeys("d", "n", "esc"),
			key.WithHelp("d", "deny"),
		),
		ScrollU: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "scroll up"),
		),
		ScrollD: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdn", "scroll down"),
		),
		// home/end ONLY (not g/G — those stay typeable in prose). Distinct from the
		// team-overlay JumpTop/JumpEnd (home/g, end/G), which only move the roster
		// selection while that overlay owns the keyboard.
		ScrollTop: key.NewBinding(
			key.WithKeys("home"),
			key.WithHelp("home", "scroll to top"),
		),
		ScrollBottom: key.NewBinding(
			key.WithKeys("end"),
			key.WithHelp("end", "scroll to bottom"),
		),
		// shift+tab: cycle permission mode. This conventional terminal chord avoids
		// relying on macOS Option being configured as Meta, while remaining distinct
		// from the submit path.
		ModeSwitch: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "switch mode"),
		),
		// ctrl+o / ctrl+r / f8: modified or special keys so they never collide with
		// the textarea's printable input (a bare letter must still type into the
		// prompt). f8 frees the textarea's readline-style ctrl+p previous-line key.
		MCPPanel: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("ctrl+o", "MCP inventory"),
		),
		Resources: key.NewBinding(
			key.WithKeys("ctrl+r"),
			key.WithHelp("ctrl+r", "MCP resources"),
		),
		Prompts: key.NewBinding(
			key.WithKeys("f8"),
			key.WithHelp("f8", "MCP prompts"),
		),
		// f6: the unified agents overlay (Subagents + Parallel + Teams tabs). A
		// function key keeps ctrl+a available for the textarea's line-start action.
		Agents: key.NewBinding(
			key.WithKeys("f6"),
			key.WithHelp("f6", "agents (subagents / parallel / teams)"),
		),
		// f7: open the /effort reasoning-effort picker (ADR 0055). A function key
		// keeps ctrl+e available for the textarea's line-end action; the picker stays
		// idle-only and caps-gated inside openEffort, mirroring the /effort command.
		Effort: key.NewBinding(
			key.WithKeys("f7"),
			key.WithHelp("f7", "reasoning-effort picker"),
		),
		// tab: switch tabs inside the agents overlay. Consulted only while the overlay
		// owns the keyboard.
		NextTab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch tab"),
		),
		// x: cancel the selected/focused subagent lane inside the agents overlay.
		CancelChild: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "cancel subagent"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Choose: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "select"),
		),
		Close: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Tasks: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "tasks"),
		),
		Findings: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "findings"),
		),
		// r: pretty↔raw toggle inside the full-screen ask-args view. Consulted only
		// while that view owns the keyboard (the permission modal is open).
		RawArgs: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "raw args"),
		),
		JumpTop: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("home/g", "first"),
		),
		JumpEnd: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("end/G", "last"),
		),
		ExpandTools: key.NewBinding(
			key.WithKeys("ctrl+t"),
			key.WithHelp("ctrl+t", "expand/collapse details"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		// ctrl+g: set the picker cursor row as the global default. ctrl-modified so a
		// bare 'g' stays typeable in the picker's filter (it is also ScrollTop/JumpTop).
		SetGlobalDefault: key.NewBinding(
			key.WithKeys("ctrl+g"),
			key.WithHelp("ctrl+g", "set global default"),
		),
	}
}

// applyKeyOverrides returns a copy of km with overridden key sets and rebuilt help short keys.
// Unknown action names in ov are ignored — composition-side validator rejects them at load.
func applyKeyOverrides(km keyMap, ov map[string][]string) keyMap {
	if len(ov) == 0 {
		return km
	}

	join := func(list []string) string { return strings.Join(list, ",") }

	// bindingSetter is a closure that replaces one field of km with a new binding.
	type bindingSetter func(chords []string)
	setters := map[string]bindingSetter{
		"Submit": func(chords []string) {
			km.Submit = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Submit.Help().Desc))
		},
		"Newline": func(chords []string) {
			km.Newline = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Newline.Help().Desc))
		},
		"Cancel": func(chords []string) {
			km.Cancel = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Cancel.Help().Desc))
		},
		"ClearPrompt": func(chords []string) {
			km.ClearPrompt = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ClearPrompt.Help().Desc))
		},
		"EditBack": func(chords []string) {
			km.EditBack = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.EditBack.Help().Desc))
		},
		"Paste": func(chords []string) {
			km.Paste = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Paste.Help().Desc))
		},
		"SelectAll": func(chords []string) {
			km.SelectAll = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.SelectAll.Help().Desc))
		},
		"CopySelection": func(chords []string) {
			km.CopySelection = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.CopySelection.Help().Desc))
		},
		"Quit": func(chords []string) {
			km.Quit = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Quit.Help().Desc))
		},
		"QuitD": func(chords []string) {
			km.QuitD = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.QuitD.Help().Desc))
		},
		"Suspend": func(chords []string) {
			km.Suspend = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Suspend.Help().Desc))
		},
		"Allow": func(chords []string) {
			km.Allow = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Allow.Help().Desc))
		},
		"AllowAlways": func(chords []string) {
			km.AllowAlways = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.AllowAlways.Help().Desc))
		},
		"Deny": func(chords []string) {
			km.Deny = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Deny.Help().Desc))
		},
		"ScrollU": func(chords []string) {
			km.ScrollU = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ScrollU.Help().Desc))
		},
		"ScrollD": func(chords []string) {
			km.ScrollD = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ScrollD.Help().Desc))
		},
		"ScrollTop": func(chords []string) {
			km.ScrollTop = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ScrollTop.Help().Desc))
		},
		"ScrollBottom": func(chords []string) {
			km.ScrollBottom = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ScrollBottom.Help().Desc))
		},
		"ModeSwitch": func(chords []string) {
			km.ModeSwitch = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ModeSwitch.Help().Desc))
		},
		"MCPPanel": func(chords []string) {
			km.MCPPanel = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.MCPPanel.Help().Desc))
		},
		"Resources": func(chords []string) {
			km.Resources = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Resources.Help().Desc))
		},
		"Prompts": func(chords []string) {
			km.Prompts = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Prompts.Help().Desc))
		},
		"Agents": func(chords []string) {
			km.Agents = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Agents.Help().Desc))
		},
		"NextTab": func(chords []string) {
			km.NextTab = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.NextTab.Help().Desc))
		},
		"CancelChild": func(chords []string) {
			km.CancelChild = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.CancelChild.Help().Desc))
		},
		"ExpandTools": func(chords []string) {
			km.ExpandTools = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.ExpandTools.Help().Desc))
		},
		"Help": func(chords []string) {
			km.Help = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Help.Help().Desc))
		},
		"Effort": func(chords []string) {
			km.Effort = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Effort.Help().Desc))
		},
		"SetGlobalDefault": func(chords []string) {
			km.SetGlobalDefault = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.SetGlobalDefault.Help().Desc))
		},
		"Up": func(chords []string) {
			km.Up = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Up.Help().Desc))
		},
		"Down": func(chords []string) {
			km.Down = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Down.Help().Desc))
		},
		"Choose": func(chords []string) {
			km.Choose = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Choose.Help().Desc))
		},
		"Close": func(chords []string) {
			km.Close = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Close.Help().Desc))
		},
		"Refresh": func(chords []string) {
			km.Refresh = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Refresh.Help().Desc))
		},
		"Tasks": func(chords []string) {
			km.Tasks = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Tasks.Help().Desc))
		},
		"Findings": func(chords []string) {
			km.Findings = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.Findings.Help().Desc))
		},
		"RawArgs": func(chords []string) {
			km.RawArgs = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.RawArgs.Help().Desc))
		},
		"JumpTop": func(chords []string) {
			km.JumpTop = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.JumpTop.Help().Desc))
		},
		"JumpEnd": func(chords []string) {
			km.JumpEnd = key.NewBinding(key.WithKeys(chords...), key.WithHelp(join(chords), km.JumpEnd.Help().Desc))
		},
	}
	for name, set := range setters {
		if chords, ok := ov[name]; ok {
			set(chords)
		}
	}
	return km
}

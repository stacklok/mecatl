package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// builtin is one client-side slash command: a name (without the leading "/"), a
// short description for the palette, and a run func that acts purely on the
// Bubble Tea Model. Built-ins live in the ui package (not client) because they
// are a Model concern — they mutate model state (clear the conversation, open an
// overlay) rather than talking to the server. They coexist with server-side
// workspace commands in the palette; see mergeCommands for precedence.
//
// Note: /compact is intentionally NOT a built-in. Compaction is a server
// operation with no client-reachable RPC today, so a "/compact" built-in would
// have nothing to call. It is a follow-up that needs a proto message + server
// RPC before the ui can offer it.
type builtin struct {
	name string
	desc string
	run  func(Model) (tea.Model, tea.Cmd)
}

// wiredCollaborators is the set of "is this ui collaborator wired" booleans the
// built-in registry consults. It collapses the long bool parameter list (which
// had reached 7 and was growing per feature) into one named struct so call sites
// read clearly and a new collaborator is one field, not an 8th positional bool.
type wiredCollaborators struct {
	MCP       bool
	Agents    bool
	Skills    bool
	Soul      bool
	UserModel bool
	Models    bool // mirrors client.Capabilities.ModelSelection
	Worktrees bool
}

// builtinCommands returns the caps-filtered built-in set for the connected
// server. /clear and /help are ALWAYS present — they act purely on the Model and
// need no server feature. /mcp is present only when the server advertises MCP
// AND a Commander-independent MCP collaborator is wired (w.MCP); /agents (the
// definition inventory) only when the server advertises Agents AND an agents
// collaborator is wired (w.Agents); /team (the live-team overlay) only when the
// server advertises Teams; /skills only when the server advertises Skills
// AND a skills collaborator is wired (w.Skills); /soul only when the server
// advertises Soul AND a soul collaborator is wired (w.Soul); /usermodel only
// when the server advertises UserModel AND a user-model collaborator is wired
// (w.UserModel); /models (the model picker) only when the server advertises
// model_selection AND a model lister is wired (w.Models); /worktrees (the
// worktree switch overlay, issue #102) only when the server advertises
// worktrees AND a worktree lister is wired (w.Worktrees). The order is fixed
// (clear, help, mcp, agents, team, skills, soul, usermodel, models, worktrees)
// and locked by a test so the palette ordering is stable.
func builtinCommands(caps client.Capabilities, w wiredCollaborators) []builtin {
	out := []builtin{
		{
			name: "clear",
			desc: "clear the conversation and scrollback",
			run:  Model.runClear,
		},
		{
			name: "help",
			desc: "show keys & features",
			run:  Model.runHelp,
		},
	}
	if caps.MCP && w.MCP {
		out = append(out, builtin{
			name: "mcp",
			desc: "browse MCP inventory",
			run:  Model.runMCP,
		})
	}
	if caps.Agents && w.Agents {
		out = append(out, builtin{
			name: "agents",
			desc: "browse agent definitions",
			run:  Model.runAgentsInv,
		})
	}
	if caps.Teams {
		out = append(out, builtin{
			name: "team",
			desc: "live agent-team overlay",
			run:  Model.runTeam,
		})
	}
	if caps.Skills && w.Skills {
		out = append(out, builtin{
			name: "skills",
			desc: "browse skills inventory",
			run:  Model.runSkills,
		})
	}
	if caps.Soul && w.Soul {
		out = append(out, builtin{
			name: "soul",
			desc: "inspect the persona (read-only)",
			run:  Model.runSoul,
		})
	}
	if caps.UserModel && w.UserModel {
		out = append(out, builtin{
			name: "usermodel",
			desc: "inspect the user model (read-only)",
			run:  Model.runUserModel,
		})
	}
	if caps.ModelSelection && w.Models {
		out = append(out, builtin{
			name: "models",
			desc: "pick the model for the next session",
			run:  Model.runModels,
		})
	}
	if caps.Worktrees && w.Worktrees {
		out = append(out, builtin{
			name: "worktrees",
			desc: "switch to a sibling git worktree",
			run:  Model.runWorktrees,
		})
	}
	// /posture prints the server-wide operator posture tier + a line per defense.
	// Gated on a non-empty caps.Posture (an older server omits the field), so it never
	// appears against a server that cannot report it. Chrome only — it changes nothing.
	if caps.Posture != "" {
		out = append(out, builtin{
			name: "posture",
			desc: "show the server's operator posture",
			run:  Model.runPosture,
		})
	}
	return out
}

// builtinByName looks up a built-in by name within the caps-filtered set, for
// dispatch. ok is false when no built-in by that name is currently registered
// (either unknown, or gated off on this server).
func builtinByName(caps client.Capabilities, w wiredCollaborators, name string) (builtin, bool) {
	for _, b := range builtinCommands(caps, w) {
		if b.name == name {
			return b, true
		}
	}
	return builtin{}, false
}

// runClear resets the conversation and all derived session state so the
// zero-state welcome card reappears (conv.isEmpty() becomes true). It is
// idle-only: while a run streams it is a no-op with an explanatory status, so a
// /clear mid-turn can't tear out the live stream's backing state.
func (m Model) runClear() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot clear while running")
		return m, nil
	}
	// resetSession (model.go) owns the full set of session-derived fields, so a
	// future such field can never be silently forgotten by /clear.
	m = m.resetSession()
	m.statusMsg = m.deps.Theme.Style("success").Render("cleared")
	m.refreshView()
	return m, nil
}

// runHelp opens the "?" keys-&-features overlay — the same state the "?" key
// sets (see onIdleKey). It blurs the textarea so the overlay owns the keyboard.
func (m Model) runHelp() (tea.Model, tea.Cmd) {
	m.showHelp = true
	m.ta.Blur()
	return m, nil
}

// runMCP opens the MCP inventory panel — the same surface ctrl+o opens. Only
// registered when caps.MCP && the MCP collaborator is wired, so openMCP's own
// nil/idle guards are belt-and-braces here.
func (m Model) runMCP() (tea.Model, tea.Cmd) {
	return m.openMCP(mcpPanel)
}

// runAgentsInv opens the agent-definition inventory panel. Only registered when
// caps.Agents && the agents collaborator is wired, so openAgentsInv's own
// nil/idle guards are belt-and-braces here.
func (m Model) runAgentsInv() (tea.Model, tea.Cmd) {
	return m.openAgentsInv()
}

// runTeam opens the live agent-team overlay — the same surface ctrl+a opens.
// Only registered when caps.Teams.
func (m Model) runTeam() (tea.Model, tea.Cmd) {
	return m.openTeam()
}

// runSkills opens the skills inventory panel. Only registered when caps.Skills
// && the skills collaborator is wired, so openSkills's own nil/idle guards are
// belt-and-braces here.
func (m Model) runSkills() (tea.Model, tea.Cmd) {
	return m.openSkills()
}

// runSoul opens the read-only soul (persona) inspection panel. Only registered
// when caps.Soul && the soul collaborator is wired, so openSoul's own nil/idle
// guards are belt-and-braces here.
func (m Model) runSoul() (tea.Model, tea.Cmd) {
	return m.openSoul()
}

// runUserModel opens the read-only user-model inspection panel. Only registered
// when caps.UserModel && the user-model collaborator is wired, so openUserModel's
// own nil/idle guards are belt-and-braces here.
func (m Model) runUserModel() (tea.Model, tea.Cmd) {
	return m.openUserModel()
}

// runModels opens the /models picker. Only registered when caps.ModelSelection &&
// the model lister is wired, so openModels's own nil/idle guards are
// belt-and-braces here.
func (m Model) runModels() (tea.Model, tea.Cmd) {
	return m.openModels()
}

// runWorktrees opens the /worktrees overlay (issue #102). Only registered when
// caps.Worktrees && the worktree lister is wired, so openWorktrees's own
// nil/idle guards are belt-and-braces here.
func (m Model) runWorktrees() (tea.Model, tea.Cmd) {
	return m.openWorktrees()
}

// runPosture shows the server-wide operator posture tier and a compact per-defense
// summary in the status line. Chrome only — read-only, mutates nothing on the server
// and acts purely on the Model (like /clear). Only registered when caps.Posture is
// non-empty. The summary names the four defenses the posture controls so an operator
// can confirm, e.g., that the child prompt-injection defense is OFF under yolo.
func (m Model) runPosture() (tea.Model, tea.Cmd) {
	m.statusMsg = m.deps.Theme.Style("muted").Render(postureSummary(m.caps.Posture))
	return m, nil
}

// postureSummary renders the one-line /posture summary for a posture token. It is
// pure (no Model) so it is directly testable. allow-all + main-substitution are on at
// auto+yolo; the CHILD substitution (prompt-injection defense OFF) is yolo-only;
// project-trust is on at trusted+. An empty/unknown token degrades to a bare label.
func postureSummary(p string) string {
	allowAll := p == postureAuto || p == postureYolo
	childDefenseOff := p == postureYolo
	trust := p == postureTrusted || p == postureAuto || p == postureYolo
	onoff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	label := p
	if label == "" {
		label = "unknown"
	}
	return "posture " + label +
		" — allow-all " + onoff(allowAll) +
		"; main $()/heredoc auto-run " + onoff(allowAll) +
		"; child $()/heredoc auto-run (injection-defense off) " + onoff(childDefenseOff) +
		"; project-trust " + onoff(trust) +
		" (Deny & configured Ask always apply)"
}

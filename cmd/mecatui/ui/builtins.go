package ui

import (
	"encoding/json"
	"fmt"
	"strings"

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
	MCP         bool
	Agents      bool
	Skills      bool
	Soul        bool
	UserModel   bool
	Reflections bool
	Models      bool // mirrors client.Capabilities.ModelSelection
	Worktrees   bool
	Scheduling  bool
	Sessions    bool // /sessions picker — gated on inventory + authoritative transcript
	Learning    bool // /learning operator-settings enum
	DebugAsk    bool // /debug-ask — env-gated (MECATUI_DEBUG_ASK=1) fake-ask injector
}

// wiredCollaborators builds the struct from m.deps — the SINGLE construction
// site for it, called by every builtinCommands/builtinByName call site
// (palette.go's builtinRows, update.go's runSelectedBuiltin and submitPrompt) so
// the three can never drift out of sync again (issue: the palette's hand-rolled
// copy once omitted Sessions, so /sessions never appeared in autocomplete even
// though the actual dispatch path built it correctly).
func (m Model) wiredCollaborators() wiredCollaborators {
	return wiredCollaborators{
		MCP: m.deps.MCP != nil, Agents: m.deps.Agents != nil, Skills: m.deps.Skills != nil,
		Soul: m.deps.Soul != nil, UserModel: m.deps.UserModel != nil, Models: m.deps.Models != nil,
		Reflections: m.deps.Reflections != nil,
		Worktrees:   m.deps.Worktrees != nil, Scheduling: m.deps.Sched != nil,
		Sessions: m.deps.Sessions != nil && m.deps.Transcript != nil,
		Learning: m.deps.Learning != nil,
		DebugAsk: m.deps.DebugAsk,
	}
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
// worktrees AND a worktree lister is wired (w.Worktrees). /schedule (the
// scheduled-tasks overlay, issue #234) only when the server advertises
// scheduling AND a schedule lister is wired (w.Scheduling). /effort (the
// reasoning-effort picker, ADR 0055) is gated identically to /models and sits
// directly after it. The order is fixed (clear, help, mcp, agents, team, skills,
// soul, usermodel, models, effort, worktrees, schedule) and locked by a test so
// the palette ordering is stable.
//
//nolint:gocyclo // capability-gated built-ins remain explicit and ordered
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
		{
			name: "session",
			desc: "show active session details and copy its exact ID",
			run:  Model.runSessionDetails,
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
	if caps.LearningProposals && w.Reflections {
		out = append(out, builtin{name: "reflections", desc: "review staged learning proposals", run: Model.runReflections})
	}
	if caps.Reflection && w.Reflections {
		out = append(out, builtin{name: "reflect", desc: "reflect the current completed session", run: Model.runReflect})
	}
	if caps.ModelSelection && w.Models {
		out = append(out, builtin{
			name: "models",
			desc: "pick the model for the next session",
			run:  Model.runModels,
		})
		// /effort picks the reasoning-effort tier (ADR 0055). Gated identically to
		// /models — the effort is a per-session server setting that only matters when
		// model selection is available — and sits right after it (the natural pairing).
		out = append(out, builtin{
			name: "effort",
			desc: "pick the reasoning-effort tier (restarts the session)",
			run:  Model.runEffort,
		})
	}
	if caps.Worktrees && w.Worktrees {
		out = append(out, builtin{
			name: "worktrees",
			desc: "switch to a sibling git worktree",
			run:  Model.runWorktrees,
		})
	}
	if caps.Scheduling && w.Scheduling {
		out = append(out, builtin{
			name: "schedule",
			desc: "browse & manage scheduled tasks",
			run:  Model.runSchedule,
		})
	}
	// /sessions is available when inventory and authoritative transcript clients
	// are wired. Chats can be continued; scheduled and child runs are inspected.
	if w.Sessions {
		out = append(out, builtin{
			name: "sessions",
			desc: "continue chats or inspect scheduled and child runs",
			run:  Model.runSessions,
		})
	}
	out = appendLearningBuiltin(out, w)
	out = appendDebugAskBuiltin(out, w)
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

func appendLearningBuiltin(out []builtin, w wiredCollaborators) []builtin {
	if !w.Learning {
		return out
	}
	return append(out, builtin{
		name: "learning",
		desc: "cycle completed-trajectory learning mode (restart required)",
		run:  Model.runLearning,
	})
}

// appendDebugAskBuiltin registers /debug-ask ONLY under the env-gated Deps.DebugAsk
// (MECATUI_DEBUG_ASK=1) — a hand-testing affordance for the permission modal's
// long-args surfaces (issue #488), never a documented feature.
func appendDebugAskBuiltin(out []builtin, w wiredCollaborators) []builtin {
	if !w.DebugAsk {
		return out
	}
	return append(out, builtin{
		name: "debug-ask",
		desc: "(debug) inject a fake permission ask (long bash)",
		run:  Model.runDebugAsk,
	})
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

func (m Model) runSessionDetails() (tea.Model, tea.Cmd) {
	return m.openSessionDetails()
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

func (m Model) runReflections() (tea.Model, tea.Cmd) {
	return m.openReflections()
}

// runModels opens the /models picker. Only registered when caps.ModelSelection &&
// the model lister is wired, so openModels's own nil/idle guards are
// belt-and-braces here.
func (m Model) runModels() (tea.Model, tea.Cmd) {
	return m.openModels()
}

// runEffort opens the /effort picker (ADR 0055). Only registered when
// caps.ModelSelection && the model lister is wired, so openEffort's own nil/idle
// guards are belt-and-braces here.
func (m Model) runEffort() (tea.Model, tea.Cmd) {
	return m.openEffort()
}

// runWorktrees opens the /worktrees overlay (issue #102). Only registered when
// caps.Worktrees && the worktree lister is wired, so openWorktrees's own
// nil/idle guards are belt-and-braces here.
func (m Model) runWorktrees() (tea.Model, tea.Cmd) {
	return m.openWorktrees()
}

// runSchedule opens the /schedule overlay (issue #234). Only registered when
// caps.Scheduling && the schedule lister is wired, so openSchedule's own
// nil/idle guards are belt-and-braces here.
func (m Model) runSchedule() (tea.Model, tea.Cmd) {
	return m.openSchedule()
}

// runSessions opens the capability-driven session inventory. It is registered
// only when the inventory and authoritative transcript clients are wired.
func (m Model) runSessions() (tea.Model, tea.Cmd) {
	return m.openSessions()
}

func (m Model) runLearning() (tea.Model, tea.Cmd) {
	from, to, restart, err := m.deps.Learning.Advance()
	if err != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("learning settings: " + err.Error())
		return m, nil
	}
	m.statusMsg = m.deps.Theme.Style("success").Render("learning: " + from + " → " + to + "; " + restart)
	return m, nil
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

// debugAskPayloads are the three canned long-args Bash commands /debug-ask
// rotates through (issue #488): (a) one very long single-line pipeline, (b) a
// compound &&/||/; command with pipes and redirections, (c) a heredoc carrying
// real newlines. Each is injected JSON-encoded as {"command": …} so the modal's
// pretty tier decodes it exactly like a wire ask.
var debugAskPayloads = []string{
	"find . -name '*.go' -not -path './vendor/*' -print0 | xargs -0 grep -nH 'func Test' | awk -F: '{print $1}' | sort | uniq -c | sort -rn | head -40 | while read -r count file; do printf '%5d  %s\\n' \"$count\" \"$file\"; done | tee /tmp/test-counts.txt | column -t -s' '",
	"git fetch origin main && git rebase origin/main || git merge --abort; cargo build --release 2>&1 | tee /tmp/build.log | grep -E 'error|warning' > /tmp/build-issues.txt; docker compose up -d --wait && curl -fsS http://localhost:8080/healthz || docker compose logs --tail=200",
	"cat <<'EOF' > /tmp/report.md\n# Nightly report\n\n## Summary\n\n- total: 42\n- failed: 3\n- skipped: 1\n\n## Failures\n\n- pkg/foo: TestBar — timeout after 30s waiting on the fixture server\n- pkg/baz: TestQux — golden mismatch (see .scratch/qux.diff)\n- pkg/quux: TestCorge — nil dereference on empty input\n\n## Environment\n\nRun at $(date -u +%FT%TZ) against the staging workspace (us-east-1).\nRunner: nightly-04 · image sha256:9f86d08…\n\n## Next steps\n\nRe-run the three failing tests with -count=1 -v and attach the artifacts bundle to the tracker issue.\nEOF\nprintf 'wrote %s (%d bytes)\\n' /tmp/report.md \"$(wc -c < /tmp/report.md)\"",
}

// runDebugAsk injects a FAKE permission ask with long Bash args through the SAME
// reducer the wire drives (applyPermissionAsk over a client.PermissionAskMsg), so
// queueing, dedupe, focus, the (1 of N) badge, and the click geometry all
// exercise for real. Registered only under MECATUI_DEBUG_ASK=1. Each invocation
// rotates to the next canned payload (debugAskCycle). At phaseIdle the modal
// opens directly (applyPermissionAsk does not gate on phase) — that is the
// intended debug affordance, and a phaseAwaitingApproval invocation queues FIFO
// behind the open modal exactly like a wire ask.
func (m Model) runDebugAsk() (tea.Model, tea.Cmd) {
	n := m.debugAskCycle
	m.debugAskCycle++
	cmd := debugAskPayloads[n%len(debugAskPayloads)]
	args, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("debug-ask: marshal failed")
		return m, nil
	}
	// The askID embeds the monotonic invocation counter so a re-injection after a
	// resolve is never swallowed by the resolvedAsks dedupe. It is colon-free up
	// to the trailing counter, so isChildAsk classifies it as a MAIN ask (the
	// always button is offered — the modal shows all three buttons).
	return m.applyPermissionAsk(client.PermissionAskMsg{
		AskID:  fmt.Sprintf("sess-debug-ask-%d", n),
		Tool:   "Bash",
		Args:   string(args),
		Reason: "debug ask (MECATUI_DEBUG_ASK) — not from the model",
	})
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

// allBuiltins returns the static set of ALL possible built-in names (clear, help,
// mcp, agents, team, skills, soul, usermodel, models, effort, worktrees, schedule,
// sessions, posture) — regardless of caps or wired-collaborators. It is derived
// from builtinCommands with all gates set true so it stays in sync with the table
// (no second list to drift). Used by submitPrompt to block a /<known-builtin>
// that is currently gated-off rather than sending it to the model as text.
func allBuiltins() map[string]bool {
	allCaps := client.Capabilities{
		MCP: true, Agents: true, Teams: true, Skills: true, Soul: true,
		UserModel: true, ModelSelection: true, Worktrees: true, Scheduling: true,
		Posture: "yes",
	}
	allWired := wiredCollaborators{
		MCP: true, Agents: true, Skills: true, Soul: true, UserModel: true,
		Models: true, Worktrees: true, Scheduling: true, Sessions: true, Learning: true,
		DebugAsk: true,
	}
	set := make(map[string]bool, 14)
	for _, b := range builtinCommands(allCaps, allWired) {
		set[b.name] = true
	}
	return set
}

// knownBuiltinNames is the one-shot evaluation of allBuiltins.
var knownBuiltinNames = allBuiltins()

// isKnownBuiltinName reports whether name is one of the static built-in slash
// commands — regardless of whether it is currently gated off by caps or wired
// collaborators. The caller uses this alongside builtinByName to distinguish
// "gated-off builtin" from "unknown / workspace command".
func isKnownBuiltinName(name string) bool {
	return knownBuiltinNames[name]
}

// interceptSlashCommand checks whether text is a bare slash command (no args, no
// newlines). If it matches a currently-registered builtin, it executes it. If it
// matches a KNOWN builtin name that is gated off, it blocks the send with a
// warning. Otherwise it returns handled=false and the caller falls through to the
// normal send path (workspace/custom command, issue #348).
func (m Model) interceptSlashCommand(text string) (tea.Model, tea.Cmd, bool) {
	name, ok := commandPrefix(text)
	if !ok {
		return m, nil, false
	}
	// Normalize to lower-case: builtinByName does a case-sensitive lookup
	// against all-lowercase names, so "/MODELS" would miss both the dispatch
	// path and the isKnownBuiltinName guard. Lowercasing here aligns the two
	// without changing the downstream palette/completion paths.
	name = strings.ToLower(name)
	if b, found := builtinByName(m.caps, m.wiredCollaborators(), name); found {
		m.ta.Reset()
		mm, cmd := b.run(m)
		return mm, cmd, true
	}
	// The slash name did NOT match any CURRENTLY-REGISTERED built-in.
	// Block it if it IS a known builtin name (gated off by caps or wired
	// collaborators) — sending "/mcp" to the model when MCP is off is
	// never useful. Unknown names fall through to the normal send path.
	if isKnownBuiltinName(name) {
		m.statusMsg = m.deps.Theme.Style("warning").Render(
			"/" + name + " is not available for this session — capabilities may still be loading; try again in a moment",
		)
		return m, nil, true
	}
	return m, nil, false
}

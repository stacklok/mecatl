package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// builtin is one client-side slash command: a name (without the leading "/"), a
// short description for the palette, and a run func that acts purely on the
// Bubble Tea Model. Built-ins live in the ui package (not client) because they
// are a Model concern — they mutate model state (clear the conversation, open an
// overlay) rather than talking to the server. They coexist with server-side
// workspace commands in the palette; see mergeCommands for precedence.
type builtin struct {
	name        string
	desc        string
	acceptsArgs bool
	run         func(Model) (tea.Model, tea.Cmd)
}

// wiredCollaborators is the set of "is this ui collaborator wired" booleans the
// built-in registry consults. It collapses the long bool parameter list (which
// had reached 7 and was growing per feature) into one named struct so call sites
// read clearly and a new collaborator is one field, not an 8th positional bool.
type wiredCollaborators struct {
	MCP          bool
	Agents       bool
	Skills       bool
	Soul         bool
	UserModel    bool
	Reflections  bool
	Dream        bool
	Compactor    bool
	Models       bool // mirrors client.Capabilities.ModelSelection
	Worktrees    bool
	Scheduling   bool
	Sessions     bool // /sessions picker — gated on inventory + authoritative transcript
	Learning     bool // /learning operator-settings enum
	Debug        bool // all debug-only builtins
	DebugAsk     bool // /debug-ask narrow compatibility alias
	Connect      bool // /connect — saved remote target picker
	DebugSession bool // dedicated target-bound debugger: hide binding-breaking actions
}

// wiredCollaborators builds the struct from m.deps — the SINGLE construction
// site for it, called by every builtinCommands/builtinByName call site
// (palette.go's builtinRows, update.go's dispatchSelectedBuiltin and
// dispatchBareBuiltin) so the three can never drift out of sync again (issue: the palette's hand-rolled
// copy once omitted Sessions, so /sessions never appeared in autocomplete even
// though the actual dispatch path built it correctly).
func (m Model) wiredCollaborators() wiredCollaborators {
	return wiredCollaborators{
		MCP: m.deps.MCP != nil, Agents: m.deps.Agents != nil, Skills: m.deps.Skills != nil,
		Soul: m.deps.Soul != nil, UserModel: m.deps.UserModel != nil, Models: m.deps.Models != nil,
		Reflections: m.deps.Reflections != nil,
		Dream:       m.deps.Dream != nil,
		Compactor:   m.deps.Compactor != nil,
		Worktrees:   m.deps.Worktrees != nil, Scheduling: m.deps.Sched != nil,
		Sessions:     m.deps.Sessions != nil && m.deps.Transcript != nil,
		Learning:     m.deps.Learning != nil,
		Debug:        m.deps.Debug,
		DebugAsk:     m.deps.DebugAsk,
		Connect:      m.deps.Connect != nil,
		DebugSession: m.deps.DebugTarget != "",
	}
}

// builtinCommands returns the caps-filtered built-in set for the connected
// server. /clear, /help, and /quit are ALWAYS present — they act purely on the Model and
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
// directly after it. The order is fixed (clear, help, quit, mcp, agents, team, skills,
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
			name: "quit",
			desc: "quit mecatui",
			run:  Model.runQuit,
		},
		{
			name:        "title",
			desc:        "show or rename the active session title",
			acceptsArgs: true,
			run: func(m Model) (tea.Model, tea.Cmd) {
				return m.runTitle(), nil
			},
		},
		{
			name: "session",
			desc: "show active session details and copy its exact ID",
			run:  Model.runSessionDetails,
		},
		{
			name: "retry",
			desc: "retry the last eligible failed model step without resending its prompt",
			run:  Model.runFailedStepRetry,
		},
		{
			name: "diagnostics",
			desc: "send a concise client and server diagnostics report",
			run:  Model.runDiagnostics,
		},
	}
	if caps.ManualCompaction && w.Compactor {
		out = append(out, builtin{
			name: "compact",
			desc: "compact this session's model history",
			run:  Model.runCompact,
		})
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
	if caps.ManualDream != nil && w.Dream {
		out = append(out, builtin{name: "dream", desc: "manually consolidate project memory or the user model", run: Model.runDream})
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
	// are wired. Row capabilities drive continuation, inspection, and management.
	if w.Sessions {
		out = append(out, builtin{
			name: "sessions",
			desc: "continue, inspect, or manage stored sessions",
			run:  Model.runSessions,
		})
	}
	if w.Connect {
		out = append(out, builtin{name: "connect", desc: "sign in and connect to a saved remote target", run: Model.runConnect})
	}
	out = appendLearningBuiltin(out, w)
	out = appendDebugBuiltins(out, w)
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
	if w.DebugSession {
		filtered := out[:0]
		for _, b := range out {
			switch b.name {
			case "clear", "models", "effort", "worktrees", "sessions":
				continue
			default:
				filtered = append(filtered, b)
			}
		}
		return filtered
	}
	return out
}

func appendLearningBuiltin(out []builtin, w wiredCollaborators) []builtin {
	if !w.Learning {
		return out
	}
	return append(out,
		builtin{name: "learning", desc: "cycle completed-trajectory learning mode (restart required)", run: Model.runLearning},
		builtin{name: "learning-sensitivity", desc: "cycle automatic learning sensitivity (restart required)", run: Model.runLearningSensitivity},
	)
}

type debugBuiltin struct {
	builtin
	legacyEnabled func(wiredCollaborators) bool
}

// debugBuiltins is the single declaration of debug-only local commands. Both
// enabled registration and known-but-gated interception derive from this slice.
// Each optional legacy gate remains narrow to that one command.
var debugBuiltins = []debugBuiltin{{
	builtin: builtin{
		name: "debug-ask",
		desc: "(debug) inject a fake permission ask (long bash)",
		run:  Model.runDebugAsk,
	},
	legacyEnabled: func(w wiredCollaborators) bool { return w.DebugAsk },
}}

func appendDebugBuiltins(out []builtin, w wiredCollaborators) []builtin {
	for _, debug := range debugBuiltins {
		if w.Debug || (debug.legacyEnabled != nil && debug.legacyEnabled(w)) {
			out = append(out, debug.builtin)
		}
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

// clearHandoff identifies the one source-bound ClearSession request whose
// response may replace the current binding.
type clearHandoff struct {
	sourceID      string
	token         uint64
	sourcePhase   phase
	sourceSettled bool
	failure       string
}

// runClear starts a create-first handoff to a new empty session from every
// interactive phase. The server owns cancellation and settlement of an active
// run or durable approval; the old binding and transcript remain visible until
// the correlated successor response succeeds.
func (m Model) runClear() (tea.Model, tea.Cmd) {
	if m.clearPending != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("clear is already in progress")
		return m, nil
	}
	if m.deps.Session == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot clear: session creation unavailable")
		return m, nil
	}

	oldID := m.sessionID
	m.clearRequestToken++
	m.clearPending = &clearHandoff{sourceID: oldID, token: m.clearRequestToken, sourcePhase: m.phase}
	m.phase = phaseConnecting
	m.statusMsg = "clearing — cancelling the current run and creating a fresh session…"
	return m, tea.Batch(m.clearSessionCmd(oldID, m.clearRequestToken), m.sp.Tick)
}

// clearSessionCmd asks the server to settle the source and create an
// empty-history successor. Server inheritance carries placement, mode, model,
// effort, limits, and permission posture.
func (m Model) clearSessionCmd(oldID string, token uint64) tea.Cmd {
	deps := m.deps
	return func() tea.Msg {
		id, snapshot, err := deps.Session.ClearSession(deps.Ctx, oldID, nil)
		if err != nil {
			return clearSessionFailedMsg{sourceID: oldID, token: token, err: err}
		}
		return clearSessionReadyMsg{
			ready:     client.SessionReadyMsg{SessionID: id, Capabilities: snapshot.Capabilities, ResolvedModel: snapshot.ResolvedModel, Mode: snapshot.Mode},
			placement: snapshot.Placement,
			oldID:     oldID,
			token:     token,
		}
	}
}

type clearSessionReadyMsg struct {
	ready     client.SessionReadyMsg
	placement client.Placement
	oldID     string
	token     uint64
}

type clearSessionFailedMsg struct {
	sourceID string
	token    uint64
	err      error
}

// closeSessionCmd is deliberately best-effort: the replacement is already bound,
// so failure to close the old persisted session must not affect the new one.
func (m Model) closeSessionCmd(id string) tea.Cmd {
	deps := m.deps
	return func() tea.Msg {
		if id != "" {
			_ = deps.Session.CloseSession(deps.Ctx, id)
		}
		return nil
	}
}

func (m Model) runCompact() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot compact while a run is active")
		return m, nil
	}
	if m.sessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot compact: no active session")
		return m, nil
	}
	if m.compactPending {
		m.statusMsg = m.deps.Theme.Style("warning").Render("session compaction is already in progress")
		return m, nil
	}
	if m.deps.Compactor == nil || !m.caps.ManualCompaction {
		m.statusMsg = m.deps.Theme.Style("warning").Render("/compact is not available on this server")
		return m, nil
	}
	m.compactPending = true
	m.compactRequestToken++
	m.statusMsg = m.deps.Theme.Style("muted").Render("compacting model history…")
	return m, client.CompactSessionCmd(m.deps.Ctx, m.deps.Compactor, m.sessionID, m.compactRequestToken)
}

// runHelp opens the "?" keys-&-features overlay — the same state the "?" key
// sets (see onIdleKey). It blurs the textarea so the overlay owns the keyboard.
func (m Model) runHelp() (tea.Model, tea.Cmd) {
	m.showHelp = true
	m.prompt.Blur()
	return m, nil
}

func (m Model) runQuit() (tea.Model, tea.Cmd) {
	return m.quitNow()
}

func (m Model) runSessionDetails() (tea.Model, tea.Cmd) {
	return m.openSessionDetails()
}

func (m Model) runFailedStepRetry() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot retry while a run is active")
		return m, nil
	}
	if m.sessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no session is available to retry")
		return m, nil
	}
	m.failedStepRetryTried = true
	return m.startFailedStepRetry()
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

// runUserModel opens the read-only user-model inspection panel. Only registered
// when caps.UserModel && the user-model collaborator is wired, so openUserModel's
// own nil/idle guards are belt-and-braces here.
func (m Model) runUserModel() (tea.Model, tea.Cmd) {
	return m.openUserModel()
}

func (m Model) runReflections() (tea.Model, tea.Cmd) {
	return m.openReflections()
}

func (m Model) runDream() (tea.Model, tea.Cmd) {
	return m.openDream()
}

// runModels opens the /models picker. Only registered when caps.ModelSelection &&
// the model lister is wired, so openModels's own nil/idle guards are
// belt-and-braces here.
func (m Model) runModels() (tea.Model, tea.Cmd) {
	return m.openModels()
}

// runConnect opens the saved remote-target picker. It is idle-only: switching a
// connection tears down this TUI before any browser-capable work happens in main.
func (m Model) runConnect() (tea.Model, tea.Cmd) {
	return m.openConnect()
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

func (m Model) runLearningSensitivity() (tea.Model, tea.Cmd) {
	from, to, restart, err := m.deps.Learning.AdvanceSensitivity()
	if err != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("learning sensitivity: " + err.Error())
		return m, nil
	}
	m.statusMsg = m.deps.Theme.Style("success").Render("learning sensitivity: " + from + " → " + to + "; " + restart)
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
// exercise for real. Registered only in client debug mode. Each invocation
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
		Reason: "debug ask (client debug mode) — not from the model",
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

type builtinName struct {
	name        string
	acceptsArgs bool
}

// builtinNameRegistry is the static registry identity and argument policy used
// before a capability-gated builtin can be dispatched.
var builtinNameRegistry = []builtinName{
	{name: "clear", acceptsArgs: false}, {name: "help", acceptsArgs: false}, {name: "quit", acceptsArgs: false}, {name: "title", acceptsArgs: true}, {name: "session", acceptsArgs: false},
	{name: "retry", acceptsArgs: false}, {name: "diagnostics", acceptsArgs: false}, {name: "compact", acceptsArgs: false},
	{name: "mcp", acceptsArgs: false}, {name: "agents", acceptsArgs: false}, {name: "team", acceptsArgs: false},
	{name: "skills", acceptsArgs: false}, {name: "soul", acceptsArgs: false}, {name: "usermodel", acceptsArgs: false},
	{name: "models", acceptsArgs: false}, {name: "effort", acceptsArgs: false}, {name: "worktrees", acceptsArgs: false},
	{name: "schedule", acceptsArgs: false}, {name: "sessions", acceptsArgs: false}, {name: "learning", acceptsArgs: false},
	{name: "learning-sensitivity", acceptsArgs: false}, {name: "posture", acceptsArgs: false},
}

func allBuiltins() map[string]bool {
	set := make(map[string]bool, len(builtinNameRegistry)+len(debugBuiltins))
	for _, builtin := range builtinNameRegistry {
		set[builtin.name] = builtin.acceptsArgs
	}
	for _, debug := range debugBuiltins {
		set[debug.name] = debug.acceptsArgs
	}
	return set
}

var knownBuiltinNames = allBuiltins()

// isKnownBuiltinName reports whether name is one of the static built-in slash
// commands — regardless of whether it is currently gated off by caps or wired
// collaborators.
func isKnownBuiltinName(name string) bool {
	_, ok := knownBuiltinNames[name]
	return ok
}

func canonicalBuiltinName(name string) string {
	if name == "exit" {
		return "quit"
	}
	return name
}

// titleCommand recognizes /title without treating arbitrary model-facing slash
// commands as client commands. bare distinguishes exactly /title from whitespace
// supplied after it, which is rejected rather than silently treated as a read.
func titleCommand(text string) (title string, bare, blank, ok bool) {
	raw := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(strings.ToLower(raw), "/title") {
		return "", false, false, false
	}
	if len(raw) > len("/title") && !unicode.IsSpace(rune(raw[len("/title")])) {
		return "", false, false, false
	}
	if len(raw) == len("/title") {
		return "", true, false, true
	}
	title = strings.TrimSpace(raw[len("/title"):])
	return title, false, title == "", true
}

// runTitle adds a local, nonpersistent title/provenance notice. It deliberately
// does not send prompt content or open a stream.
func (m Model) runTitle() tea.Model {
	title := m.sessionTitle
	if title == "" {
		title = "(untitled)"
	}
	provenance := titleProvenanceLabel(m.sessionTitleProvenance)
	m.conv.addNotice("Session title: " + title + " (" + provenance + ")")
	m.refreshView()
	return m
}

func (m Model) renameTitle(title string) (tea.Model, tea.Cmd) {
	if m.sessionID == "" || m.deps.SessionManagement == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("title rename is unavailable")
		return m, nil
	}
	m.titleRenamePrevious = m.sessionTitle
	m.sessionTitle = title
	m.sessionTitleProvenance = "operator"
	m.prompt.Reset()
	return m, client.RenameSessionCmd(m.deps.Ctx, m.deps.SessionManagement, m.sessionID, title)
}

// dispatchBareBuiltin checks whether raw text is a slash command after trimming
// surrounding Unicode whitespace. A current bare built-in executes locally. A
// recognized built-in that does not accept arguments keeps the input and shows a
// local warning. Unknown slash commands remain model-facing.
func (m Model) dispatchBareBuiltin(text string) (tea.Model, tea.Cmd, bool) {
	if title, bare, blank, ok := titleCommand(text); ok {
		if blank {
			m.statusMsg = m.deps.Theme.Style("warning").Render("title cannot be blank; use /title <text>")
			return m, nil, true
		}
		if bare {
			return m.runTitle(), nil, true
		}
		mm, cmd := m.renameTitle(title)
		return mm, cmd, true
	}
	trimmed := strings.TrimSpace(text)
	fields := strings.Fields(trimmed)
	if len(fields) > 1 {
		name, ok := commandPrefix(fields[0])
		if ok {
			name = canonicalBuiltinName(strings.ToLower(name))
			acceptsArgs, known := knownBuiltinNames[name]
			if b, found := builtinByName(m.caps, m.wiredCollaborators(), name); found {
				acceptsArgs, known = b.acceptsArgs, true
			}
			if known && !acceptsArgs {
				m.statusMsg = m.deps.Theme.Style("warning").Render("/" + name + " does not take arguments; use bare /" + name)
				return m, nil, true
			}
		}
	}
	name, ok := commandPrefix(trimmed)
	if !ok {
		return m, nil, false
	}
	// Normalize to lower-case: builtinByName does a case-sensitive lookup
	// against all-lowercase names, so "/MODELS" would miss both the dispatch
	// path and the isKnownBuiltinName guard. Lowercasing here aligns the two
	// without changing the downstream palette/completion paths.
	name = canonicalBuiltinName(strings.ToLower(name))
	if b, found := builtinByName(m.caps, m.wiredCollaborators(), name); found {
		// A successful bare-command dispatch consumes the command line, so close its
		// derived palette state too. This path serves both idle and running input.
		m.palette.open = false
		m.palette.filtered = nil
		m.palette.cursor = 0
		m.prompt.Reset()
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

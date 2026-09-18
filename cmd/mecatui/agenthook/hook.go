// Package agenthook emits mecatui's session lifecycle as AGENT LIFECYCLE HOOK
// events, in the cross-vendor hook schema that coding-agent host tools consume.
//
// # The schema is a de-facto standard, not one vendor's convention
//
// Anthropic's Claude Code originated it: a host registers a command hook, and
// the agent hands that command one JSON object on STDIN carrying
// `hook_event_name` plus common fields (`session_id`, `transcript_path`, `cwd`),
// with the lifecycle vocabulary SessionStart / UserPromptSubmit / PreToolUse /
// PostToolUse / PermissionRequest / Stop / StopFailure / SessionEnd / SessionEnd
// (see code.claude.com/docs/en/hooks). OpenAI's Codex adopted the SAME shape
// field-for-field — same `hook_event_name` on stdin, same event names, the same
// event → matcher-group → handler `hooks.json` nesting — and even exports
// CLAUDE_PLUGIN_ROOT/CLAUDE_PLUGIN_DATA "for compatibility with existing plugin
// hooks" (learn.chatgpt.com/docs/hooks). Other agents (Gemini CLI, opencode,
// Droid, Kimi, Grok, Mastra) expose the same vocabulary with minor spelling
// variance (camelCase `hookEventName`, Codex's older argv-delivered `notify`
// callback).
//
// So this package speaks a SHARED contract, which is why it is named for the
// mechanism (an agent lifecycle hook) rather than for any single host.
//
// # Why mecatui emits these itself
//
// Host tools drive their notification/busy chrome off these events, and they
// reach a vendor's agent through something the host can write into: a hook
// config file, a wrapper script, or a plugin the agent loads. mecatl has none of
// those — its only hooks are the per-tool-call PreToolUse/PostToolUse permission
// gate (engine/governance), which carries no session-lifecycle signal. mecatui
// already consumes the session.Event stream to render, so it emits the lifecycle
// directly: turn start, a main-session permission ask, and the terminal result.
//
// # Host binding, and why it is inert by default
//
// The SCHEMA above is vendor-neutral; DISCOVERY of the host's hook command is
// necessarily host-specific and lives in its own file (superset_host.go today).
// A host is detected from the environment it injects into the agent's terminal;
// with no supported host present New returns nil, every method is a no-op, and
// there is zero behaviour change for an ordinary mecatui run.
//
// Delivery is best-effort: events are queued to one ordered worker, each
// invocation is separately bounded, failures are swallowed, and nothing here can
// block or fail the agent run.
package agenthook

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// EventType is a `hook_event_name` value in the shared agent-hook schema. These
// are the CANONICAL cross-vendor spellings (identical in Claude Code and Codex),
// deliberately preferred over any host's normalized aliases: a host that
// collapses them (Superset maps UserPromptSubmit→Start server-side, for example)
// understands the canonical name, while a host-specific alias would not travel.
type EventType string

const (
	// EventPromptSubmit marks the agent beginning work on a turn — the busy
	// signal. Canonical per-turn start event in both vendors' schemas.
	EventPromptSubmit EventType = "UserPromptSubmit"
	// EventStop marks the agent finishing a turn cleanly: the completion
	// notification, and back to idle.
	EventStop EventType = "Stop"
	// EventStopFailure marks a turn that ended in an error terminal. Hosts route
	// its preview from the error fields rather than the last assistant message.
	EventStopFailure EventType = "StopFailure"
	// EventPermissionRequest marks the agent blocked waiting on a human
	// approval — the "needs attention" notification.
	EventPermissionRequest EventType = "PermissionRequest"
)

// runnerFunc dispatches one already-built hook invocation. It is the single seam
// tests replace to stay offline (no real hook command, no host service): the
// production runner shells the host's hook command, the test runner records it.
// extraEnv carries whatever host-specific entries the detected host requires
// (see the per-host files); the generic core never names them.
type runnerFunc func(ctx context.Context, script string, extraEnv []string, payload string)

// Notifier emits lifecycle hook events to a host's hook command. A nil
// *Notifier is valid and every method is a no-op — that is the "no supported
// host" state, so callers hold a *Notifier and never branch on it.
type Notifier struct {
	script   string        // absolute path to the host's hook command
	extraEnv []string      // host-specific env entries for each invocation
	run      runnerFunc    // dispatch seam
	timeout  time.Duration // per-call wall-clock bound

	mu      sync.Mutex
	running bool          // dedupe: busy signal once per turn, terminal once per turn
	queue   chan dispatch // ordered single-worker delivery; lazily started
}

// dispatch is one queued hook invocation. Ordering matters (the busy signal must
// reach the host before the terminal), so deliveries flow through one worker
// goroutine rather than a goroutine each.
type dispatch struct {
	ev        EventType
	sessionID string
	message   string
}

// queueDepth bounds the pending-delivery buffer. A full buffer drops the oldest
// pending event rather than blocking the UI reducer — lifecycle notifications
// are best-effort and a stall must never back-pressure the agent loop. In
// practice the depth is tiny (a handful of transitions per run), so the cap is
// only a safety valve against a wedged hook command.
const queueDepth = 64

// agentID identifies mecatl to the host. Hosts that multiplex several agents
// cross-check it against the identity their wrapper exported for the launched
// process and DROP a mismatch (a foreign agent replaying our hook config, or our
// binary invoked from another agent's tool call). Kept as one constant so a
// host-side registration and this emitter cannot drift.
const agentID = "mecatl"

// newNotifier builds a Notifier for a resolved host hook command plus the
// host-specific env entries that command expects. Host detection lives in the
// per-host files (see superset_host.go).
func newNotifier(script string, extraEnv []string) *Notifier {
	return &Notifier{
		script:   script,
		extraEnv: extraEnv,
		run:      defaultRunner,
		timeout:  5 * time.Second,
	}
}

// Start emits the per-turn busy signal (UserPromptSubmit) on an idle→running
// transition. Repeated Starts within one active period (e.g. one per turn) are
// collapsed to a single event: a host wants one busy signal per active period,
// not one per turn. sessionID is the mecatl session id, forwarded on the
// schema's `session_id` field so a host can bind native resume.
//
// The context is accepted for the caller's interface but deliberately UNUSED:
// deliveries are cancel-detached and separately bounded (see enqueue).
func (n *Notifier) Start(_ context.Context, sessionID string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	if n.running {
		n.mu.Unlock()
		return
	}
	n.running = true
	n.mu.Unlock()
	n.enqueue(EventPromptSubmit, sessionID, "")
}

// Stop emits the terminal event exactly once per active period, closing the busy
// period a Start opened. failed selects StopFailure over Stop (an error
// terminal); message is an optional preview (the terminal error or last
// assistant text) forwarded so the host's notification can show it. A Stop with
// no preceding Start is a no-op (no busy period to close — e.g. a terminal
// replayed on reconnect).
//
// The context is accepted for the caller's interface but deliberately UNUSED
// (see Start).
func (n *Notifier) Stop(_ context.Context, sessionID string, failed bool, message string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	if !n.running {
		n.mu.Unlock()
		return
	}
	n.running = false
	n.mu.Unlock()
	ev := EventStop
	if failed {
		ev = EventStopFailure
	}
	n.enqueue(ev, sessionID, message)
}

// PermissionRequest emits a PermissionRequest event. It does NOT touch the
// running flag: the turn is still active (paused on a human), so the eventual
// Stop must still fire. The caller is responsible for filtering child/subagent
// asks — a host drives terminal-level status from the main loop only (the same
// reason the schema carries `agent_id`/`agent_type` on subagent events).
//
// The context is accepted for the caller's interface but deliberately UNUSED
// (see Start).
func (n *Notifier) PermissionRequest(_ context.Context, sessionID, message string) {
	if n == nil {
		return
	}
	n.enqueue(EventPermissionRequest, sessionID, message)
}

// enqueue hands one event to the ordered delivery worker (started lazily on the
// first event). It never blocks the caller: a full buffer drops the OLDEST
// pending event to make room, because a stalled hook command must not
// back-pressure the UI reducer.
//
// It deliberately takes NO context. Each delivery is DETACHED from the caller's
// per-run context and bounded by n.timeout instead (see worker): the terminal
// event fires exactly as the run's context is torn down, so honouring that
// context would abort the hook before it could deliver the completion — the same
// cancel-detached rationale as the server's durable event append. The timeout
// still guarantees a wedged hook command cannot wedge the worker forever.
func (n *Notifier) enqueue(ev EventType, sessionID, message string) {
	n.mu.Lock()
	if n.queue == nil {
		n.queue = make(chan dispatch, queueDepth)
		//nolint:contextcheck // deliberate: see the doc comment above — a
		// lifecycle notification must outlive the run context that triggered it.
		go n.worker(n.queue)
	}
	q := n.queue
	n.mu.Unlock()

	d := dispatch{ev: ev, sessionID: sessionID, message: message}
	for {
		select {
		case q <- d:
			return
		default:
			// Buffer full: drop the oldest, then retry. Best-effort delivery.
			select {
			case <-q:
			default:
			}
		}
	}
}

// worker delivers queued events in arrival order, one at a time.
func (n *Notifier) worker(q chan dispatch) {
	for d := range q {
		payload := buildPayload(d.ev, d.sessionID, d.message)
		ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
		n.run(ctx, n.script, n.extraEnv, payload)
		cancel()
	}
}

// defaultRunner shells the host's hook command with the JSON payload on STDIN —
// the delivery the shared schema specifies for a command hook in both Claude
// Code and Codex. (Codex's older `notify` callback passed the blob as argv
// instead; stdin is the current contract and avoids any argv quoting concern.)
// It inherits the process environment so the host's own terminal markers remain
// visible to the hook, plus whatever host-specific entries the host requires.
// Output is discarded and any error is swallowed — best-effort.
func defaultRunner(ctx context.Context, script string, extraEnv []string, payload string) {
	cmd := exec.CommandContext(ctx, script)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

// notifyPayload is the shared agent-hook input shape: `hook_event_name` plus the
// schema's common fields. The event name is ALWAYS present (a host drops an
// event with no type rather than guessing a terminal); session_id and message
// are omitted when empty. Marshalled with encoding/json so a message with
// quotes/newlines/control bytes cannot break the payload or forge extra fields
// (the injection boundary — never hand-escape here).
type notifyPayload struct {
	Event     EventType `json:"hook_event_name"`
	SessionID string    `json:"session_id,omitempty"`
	Message   string    `json:"message,omitempty"`
}

func buildPayload(ev EventType, sessionID, message string) string {
	p := notifyPayload{
		Event:     ev,
		SessionID: strings.TrimSpace(sessionID),
		Message:   clip(strings.TrimSpace(message), maxMessageBytes),
	}
	b, err := json.Marshal(p)
	if err != nil {
		// Marshalling a struct of strings cannot fail; fall back to the
		// minimal well-formed event so a bug here still yields valid JSON.
		return `{"hook_event_name":"` + string(ev) + `"}`
	}
	return string(b)
}

// maxMessageBytes bounds the preview forwarded to the host (Superset's opencode
// plugin slices to the same 4000). Hosts clamp again; this keeps the payload
// handed to a shell small.
const maxMessageBytes = 4000

// clip trims s to at most limit bytes on a UTF-8 rune boundary so the JSON
// string stays valid.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

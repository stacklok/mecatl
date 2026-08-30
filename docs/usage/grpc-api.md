## 9. The gRPC API

This is the detailed operator and wire reference. For the client-integration
entry point, event lifecycle, and HTTP/SSE comparison, start with [Drive via
gRPC / HTTP](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/grpc-http.md).

Service: `mecatl.v1.HarnessService` (`contracts/proto/mecatl/v1/harness.proto`).

**Sessions & runs:**

| RPC | Kind | Purpose |
| --- | --- | --- |
| `CreateSession(CreateSessionRequest) → CreateSessionResponse` | unary | allocate a server-side session, return its id |
| `GetSession(GetSessionRequest) → GetSessionResponse` | unary | snapshot of an existing session |
| `RenameSession(RenameSessionRequest) → RenameSessionResponse` | unary | replace an owned idle main session's title and mark its provenance operator-authored; ownership, kind, state, liveness, and lease are revalidated at execution |
| `DeleteSession(DeleteSessionRequest) → DeleteSessionResponse` | unary | permanently remove an owned idle main session snapshot and store-managed sidecars; the same execution-time gates apply |
| `CompactSession(CompactSessionRequest) → CompactSessionResponse` | unary | force one configured compaction pass on an owned main chat at an idle or terminal boundary; creates no conversation turn and returns `compacted` to distinguish a rewrite from a successful no-op |
| `CloseSession(CloseSessionRequest) → CloseSessionResponse` | unary | end a session and release its server-side resources (learned rules, per-session engine/workspace); idempotent |
| `ForkSession(ForkSessionRequest) → ForkSessionResponse` | unary | create a new peer session whose conversation history is a snapshot of an existing session's, inheriting the source's mode, workspace, limits, and provider/model/profile labels (same provider and model only; ADR 0065). An optional `reasoning_effort` override changes ONLY the fork's effort tier — provider/model always inherit (ADR 0068). The source must be at a turn boundary (idle/terminal); a running/awaiting source is `FAILED_PRECONDITION`. No streaming — returns the new session id |
| `PreflightSessionAdoption(PreflightSessionAdoptionRequest) → PreflightSessionAdoptionResponse` | unary | authenticate and authorize one legacy `unknown` source, then return stable eligibility/binding reason codes. Workspace/environment and provider/model are mandatory explicit bindings; no omitted binding falls back to the current default |
| `AdoptSession(AdoptSessionRequest) → AdoptSessionResponse` | unary | revalidate the source under run-entry serialization and its mutation lease, then atomically publish one new explicit-main copy. `idempotency_key` is bound to caller+source; retries return the same complete target, while foreign/absent sources are both `NOT_FOUND`. The source is unchanged |
| `Converse(stream ConverseRequest) → stream ConverseResponse` | bidi | drive one agent run; the first frame is either a new `Prompt` or prompt-free `RetryStart` |
| `ApprovePlan(ApprovePlanRequest) → stream Event` | server-stream | atomically resolve a parked **plan-approval** ask (a `PresentPlan` call surfaced in plan mode, issue #206 / [ADR 0069](../adr/0069-plan-approval-gate.md)) and — on an ALLOW verdict — start a FRESH continuation run carrying the proceed message, streaming BOTH runs' events on one stream. `target_mode` selects the verdict: `DEFAULT` → allow-once (flip to default), `ACCEPT_EDITS` → allow-always (flip to accept-edits), `PLAN`/`UNSPECIFIED` → deny (iterate, no flip, no continuation run). A live run is rejected (`FAILED_PRECONDITION` — use the `Converse` `resume_approval` frame for an in-flight run); a session not `awaiting` a `PlanOriginated` ask is `FAILED_PRECONDITION` (`ErrNotAwaitingPlan`); an unknown session is `NOT_FOUND`. |
| `StreamSessionEvents(StreamSessionEventsRequest) → stream Event` | server-stream | replay a session's durable event log (cloud-native Phase 3a read-back); an unknown id yields an empty stream; `UNIMPLEMENTED` when no durable `EventLog` is wired. **Replays the FULL timeline, including the log-only `approval`/`compaction_archive`/`user_prompt` events a live `Converse` skips** — a client opening a past session gets the verdicts and user prompts, which ARE the transcript |
| `ListSessions(ListSessionsRequest) → ListSessionsResponse` | unary | the stored-session inventory — picker metadata (id, timestamps, state, turns, model id; no conversation content), sorted most-recently-active first; an empty list when the store does not implement `PrunableStore` |

**Manual compaction.** Check
`CreateSessionResponse.capabilities.manual_compaction` before offering this action.
Call `CompactSession` with the owned session ID. The server runs the configured
compactor once even when the automatic 0.8 threshold has not been reached. It does
not start `Converse`, add a user message, or create a model turn. A cascade pass may
still invoke its summarization model through the compaction slot.

`compacted=true` means the shorter history was saved. `compacted=false` is a
successful no-op for an empty, identical, or non-reducing candidate; nothing is
saved and no compaction events are appended. Idle, completed, cancelled, and failed
main chats are legal and retain their state. Running or awaiting sessions, scheduled
sessions, delegation children, and an in-process live run return
`FAILED_PRECONDITION`. Another replica holding the mutation lease also returns
`FAILED_PRECONDITION`. An absent or foreign-owned ID returns `NOT_FOUND`, avoiding an
ownership oracle; normal transport authentication still applies. A save failure is
`INTERNAL`. Once save succeeds, later event-log append failure does not change the
successful response or roll back the compacted snapshot.

The capability is additive. New clients reading an old server see false and should
hide the action. Calling an old server's unknown method returns `UNIMPLEMENTED`.

**Inventory & introspection** (read-only; most are snapshots taken at startup):

| RPC | Kind | Purpose |
| --- | --- | --- |
| `ListModels` | unary | the selectable provider/model inventory — public metadata only (powers the `/models` picker; see §3) |
| `ListAgents` | unary | the discovered agent-definition registry (name, description, resolved model, tool scope) |
| `ListCommands` | unary | the available slash commands for a workspace (discovery only — expansion happens on the run path) |
| `ListWorktrees` | unary | the git worktrees of a repo (discovery only — powers the mecatui `/worktrees` switch; nil-safe on a no-FS/cloud server) |
| `ListSkills` | unary | the discovered skills inventory (name + one-line description) |
| `GetSoul` | unary | the resolved soul's build-time snapshot: content, size/hash, provenance, trust + drift state |
| `GetUserModel` | unary | the **live**, bounded user-model index; optional `key` lazily returns exact read-only detail plus up to 16 revisions, including proposal linkage when present. No mutation rides this RPC — Forget remains a permission-gated tool |
| `ReflectSession` | unary | synchronously reflect one caller-owned completed session through its persisted provider/model (the reflection slot may change only the model); remains available with automatic mode off through lazy Build-owned initialization |
| `GenerateDreamPlan` | unary | spend one planner call to generate a bounded-lifetime review for exactly `project_memory` or `user_model`; returns displayed exact-duplicate and synthesized-replacement operations plus an opaque process-local plan id |
| `DecideDreamPlan` | unary | apply or dismiss the authoritative retained whole plan by id; accepts no operation content and returns planned/applied/conflicted/skipped/failed source counts |
| `ListLearningProposals` / `GetLearningProposal` | unary | bounded, cursor-paged proposal metadata in the verified caller partition; optional project partitions remain reviewable, while approve/undo is limited to the trusted launch root; evidence reports digest-verified availability without returning source text |
| `DecideLearningProposal` | unary | CAS approve or reject of a staged proposal; stale versions/transitions return `ABORTED` |
| `UndoLearningPromotion` | unary | CAS compensating revision only while the linked promoted memory revision remains current; stale/newer revisions return `ABORTED` |
| `ListMcpResources` / `ReadMcpResource` | unary | static MCP resource snapshots; read one resource by URI |
| `ListMcpPrompts` / `GetMcpPrompt` | unary | MCP prompt snapshots; expand one prompt to its rendered messages |
| `ListMcpSources` | unary | the resolved MCP source inventory (static / ToolHive) + diagnostics |
| `ListToolHiveGroups` | unary | the distinct ToolHive groups in the resolved inventory (no live ToolHive call) |

**Manual dream review.** Read `CreateSessionResponse.capabilities.manual_dream` to discover
generation and decision availability independently for project memory and the user model. Generation
sends the selected bounded values/descriptions to the configured planner and spends tokens. The
returned plan has a ten-minute process-local lifetime; apply/dismiss is a whole-plan decision and
regeneration is an explicit second provider call. Exact duplicates leave the survivor unchanged;
an approved synthesis atomically rewrites its displayed survivor and tombstones its displayed
sources per operation. Independent operations can produce a partial receipt; there are no per-source
decisions or grouped undo.

Plan IDs are opaque and accepted only by the process that generated them. Expiry, restart, or a
wrong-replica request returns `NOT_FOUND`; the old decision cannot be retried and the client may offer
explicit fresh generation. Same decisions are idempotent. A same-decision request while apply is still
running returns `ABORTED` and may be retried explicitly to retrieve the receipt. An opposite request
against an applying record returns `FAILED_PRECONDITION` and is non-retryable; against a terminal
record it returns `ALREADY_EXISTS`, after which explicit fresh generation is safe. Genuinely
indeterminate transport errors preserve the exact ID and decision for same-decision retry because the
first request may already have applied. No error state enables the opposite decision. Registry pressure
returns `RESOURCE_EXHAUSTED`, and an unavailable target returns `UNIMPLEMENTED`. Manual review is disabled
under ownership enforcement and when the planner or both reviewed atomic store capabilities are
missing. It is separate from `learning.mode` and the off-by-default consolidation interval flags,
and exposes neither recall counters nor provider/model identity.

**Agent teams** (experimental; registered with `--enable-teams`, the default):

| RPC | Kind | Purpose |
| --- | --- | --- |
| `CreateTeam(CreateTeamRequest) → CreateTeamResponse` | unary | allocate a team (optionally enrolling an initial roster); accepts the tighten-only `max_team_tokens` |
| `SpawnTeammate` | unary | enrol a member in an existing team (before `RunTeam`) |
| `SendTeammateMessage` | unary | post a message into a member's inbox, delivered at its next turn boundary |
| `CancelTeammate` | unary | cancel ONE member of a **running** team mid-round: it de-schedules with the `cancelled` stop reason and releases its claimed tasks; the team still delivers its report. Not-running team → `FailedPrecondition`; unknown member → `NotFound` |
| `RunTeam(RunTeamRequest) → stream TeamEvent` | server-stream | drive the team to quiescence; every member's events stream tagged with the member name, and the stream **ends with a single terminal frame carrying `TeamEvent.outcome`** (rounds, stop, `budget_exhausted`, usage, dispositions, findings) |
| `ListTeam` | unary | snapshot of the roster, shared task list, and quiescence |
| `CleanupTeam` | unary | tear down a finished team and release its resources |

### The `Converse` flow

The bidi stream drives exactly one run:

1. The client sends the **mandatory first frame**. Use
   `Prompt{session_id, text, parts}` for a new user message; `parts` optionally
   carries multimodal media (image/audio `Content` parts, capability-gated on the
   session's provider). Use `RetryStart{session_id}` to repeat an eligible failed
   model step without adding another prompt. A first frame of any other kind is
   `InvalidArgument`.
2. The server streams `ConverseResponse{Event}` envelopes in sequence order.
3. On a `permission.ask` event, the client sends a control frame
   `ResumeApproval{ask_id, verdict}` — `ask_id` echoes `Event.ask.ask_id`, and
   `verdict` is the **three-way** resolution: `DENY`, `ALLOW_ONCE`, or
   `ALLOW_ALWAYS` (which also **learns** a per-session allow rule for the same
   tool + exact canonical pattern; it never overrides a deny or plan-mode
   mutation denial). The legacy `allow` bool still works when `verdict` is
   unset: `true` → allow once, `false` → deny.
4. The client may send `Cancel{}` at any time to abort — the run terminates with
   a `result` whose `stop = "cancelled"` — or `CancelChild{child_id}` to cancel
   ONE delegated child (subagent / parallel branch / team member) by its id
   while the run itself keeps streaming.
5. The server emits a terminal `result` event and closes the stream.

`ConverseRequest` is a `oneof`:

| Field | When |
| --- | --- |
| `prompt` (`Prompt{session_id, text, parts}`) | first frame for a normal run; records a new user message |
| `retry` (`RetryStart{session_id}`) | first frame for a prompt-free failed-step retry; records no prompt, reuses conversation/tool state, re-resolves live instruction sources, and delegates eligibility to persisted server state |
| `resume_approval` (`ResumeApproval{ask_id, verdict, allow}`) | resolve a paused ask (three-way `verdict`; the `allow` bool is the legacy fallback) |
| `cancel` (`Cancel{}`) | abort the in-flight run |
| `cancel_child` (`CancelChild{child_id}`) | cancel ONE child run by its id (the `agentId:` / `child_id` handle), leaving the run and sibling children untouched; unknown/finished ids are ignored on the stream |

A second `prompt` or `retry`, or any unknown control frame, is ignored. A single
`Converse` stream drives a single run.

For a failed model stream, inspect the terminal Result's optional
`retry_disposition` and `stream_progress`. New servers set both fields even when the
value is `UNKNOWN`; an absent field identifies an older server and must not be treated
as safely retryable. `RetryStart` is prompt-free: the server persists aggregate-owned
retry intent, blocks ordinary prompts while it is pending, and skips prompt hooks and
the first retry turn's boundary injections. This avoids duplicate prompts and tool
effects. Clients may explicitly retry typed `retryable` failures at `precommit` or
`visible`; automatic retry should be narrower and bounded. Mecatui performs one
automatic retry only for typed `retryable + precommit`.

### Event envelope

Every event is the provider-neutral `Event` message (mirrors the domain
`session.Event` one-for-one — never an OpenAI type):

```protobuf
message Event {
  string type = 1;          // session.init | turn.start | turn.end |
                            // message.delta | reasoning.delta | tool.call |
                            // tool.result | tool.progress | permission.ask |
                            // permission.retract | hook | compaction |
                            // no_progress | result | subagent.* | team.* |
                            // parallel.*
  int64  seq  = 2;          // monotonic per run
  int32  turn = 3;
  string text = 4;          // streamed/final text where applicable
  ToolCall      tool_call   = 5;
  ToolResult    tool_result = 6;
  PermissionAsk ask         = 7;   // ask_id echoed in ResumeApproval
  Result        result      = 8;   // terminal event
  Usage         usage       = 9;   // cumulative on result; per-turn in turn_end
  TurnEnd       turn_end    = 10;  // turn.end: this turn's usage + elapsed time
  Hook          hook        = 11;  // structured hook phase/tool/decision/call_id
  Subagent      subagent    = 12;  // subagent.*: REDACTED bounded-preview child projection
  Team          team        = 13;  // team.*: BOUNDED team projection (start/member/tasks/findings/end)
  Parallel      parallel    = 14;  // parallel.*: REDACTED fork-join projection (incl. winner + fork paths)
}
```

The three delegation families (`subagent.*`, `team.*`, `parallel.*`) project a
child loop's lifecycle without leaking its content: all three carry the same
BOUNDED PREVIEWS (ADR 0079) — ids, tool names/counts, usage, stop, plus capped,
control-byte-scrubbed `text`/`detail` previews of child message text and tool
args/results; the task board, findings ledger, dispositions, mutating cue, and
context meter stay Team-only. Every preview is capped and a child's
permission asks are never forwarded.

`Result.stop` is one of: `end_turn`, `max_turns`, `max_tool_calls`,
`max_consecutive_failures`, `budget` (the `--max-run-tokens` ceiling crossed —
a clean, reopen-able terminal), `no_progress`, `cancelled`, `error`. A child's
stop (on `subagent.end` / in a Subagent result) may additionally be
`structured_output` — a structured-output child that exhausted its
validation retries. A plan-approval allow emits `plan_approved` — the clean
terminal ([ADR 0069](../adr/0069-plan-approval-gate.md)) that flips the session
out of plan mode at the terminal boundary.

### Go client snippet

`mecated` does **not** register gRPC server reflection, so `grpcurl` must be
pointed at the proto (and its `buf.validate` import) explicitly. A generated Go
client is the simplest path:

```go
package main

import (
	"context"
	"io"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func main() {
	conn, err := grpc.NewClient("127.0.0.1:8080",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := mecatlv1.NewHarnessServiceClient(conn)

	// 1. Create a session.
	cs, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		Workspace: "/path/to/workspace",
		Mode:      mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	})
	if err != nil {
		log.Fatal(err)
	}

	// 2. Open the Converse stream and send the mandatory first Prompt.
	stream, err := client.Converse(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{
			Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "List the Go files."},
		},
	}); err != nil {
		log.Fatal(err)
	}

	// 3. Relay events; approve any permission.ask.
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return // terminal result delivered, stream closed
		}
		if err != nil {
			log.Fatal(err)
		}
		ev := resp.GetEvent()
		log.Printf("[%d] %s %s", ev.GetSeq(), ev.GetType(), ev.GetText())

		if ev.GetType() == "permission.ask" {
			_ = stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{
						AskId: ev.GetAsk().GetAskId(),
						Allow: true,
					},
				},
			})
		}
		// To abort instead, send a Cancel{} frame:
		//   stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}}})
	}
}
```

`GetSession` returns a snapshot (`session_id`, `state`, `mode`, `workspace`,
`limits`, `turns`, `tool_calls`, `created_at_unix`).

**Inspecting / reopening a past session:** `ListSessions` returns the stored-
session inventory (picker rows: id, timestamps, state, turn count, model id —
no conversation content), sorted most-recently-active first. `StreamSessionEvents`
then replays a session's FULL durable timeline as a server stream of `Event`
envelopes — including the log-only `approval` / `compaction_archive` /
`user_prompt` events a LIVE `Converse` relay skips on the client wire. A client
opening a past session WANTS the verdicts and user prompts (they ARE the
transcript), so the replay does not apply the live-relay filter; the events are
already metadata-only / redacted by construction. An unknown id yields an empty
stream (absence is data); a server with no durable `EventLog` returns
`UNIMPLEMENTED`. Neither surface has a `ServerCapabilities` bit — the capability
is RPC-discoverable (`UNIMPLEMENTED` / empty-list degrade honestly).

### The terminal UI (`mecatui`)

`mecatui` is an optional, flashy terminal UI that drives a `mecated` over this
same gRPC `Converse` stream. After `task build` it lands at `bin/mecatui`. It
needs no separate server by default — bare `mecatui` **hosts one in-process**
over a UNIX socket (built via the shared composition layer, the same assembly
`mecated` uses), and `mecatui connect ADDRESS` dials an external server:

```sh
OPENAI_API_KEY=sk-... bin/mecatui --workspace "$PWD"        # embedded (default)
bin/mecatui --mock --workspace "$PWD"                       # embedded, offline mock

bin/mecated serve &                                         # …or an external server
bin/mecatui connect 127.0.0.1:8080 --workspace "$PWD"
```

The embedded server enables every **free + local** feature by default — memory
(per-project, under `$XDG_DATA_HOME/mecatui/memory`), server-side slash-command
expansion (`.mecatl/commands` / `.claude/commands`), skills (conventional
discovery), agent definitions, soul, user model, child-session retention GC,
ToolHive MCP discovery, and the MCP resource/prompt meta-tools. Only the
opt-ins that spend tokens or need explicit configuration stay off: static
`--mcp-server` registrations, the `SkillDraft` quarantine, the background
user-model reviewer, and memory consolidation — run a full `mecated serve` and
`connect` to it for those. It also accepts **`--perf`** (off by default) to bring up the same
loopback observability surface `mecated` exposes — `/metrics`, `/debug/pprof/*`,
`/debug/vars`, `/debug/flightrecorder` — on a **fixed** `127.0.0.1:9099` port by
default (predictable, so an MCP-client config can hardcode the `/mcp` URL once;
distinct from `mecated`'s `:9090`). Pass `--perf-addr host:port` to move it, or
`--perf-addr 127.0.0.1:0` for an ephemeral port. On a port clash, startup **fails
with guidance** rather than silently falling back (`--perf-goroutine-warn-threshold`
arms the goroutine alarm). The chosen address is logged at startup (loopback, unauthenticated —
same posture as `mecated`'s admin listener; see the observability note in §3).
With `--perf` it also accepts **`--perf-mcp`** to mount the read-only perf MCP
server at `/mcp` on that admin surface (same fail-closed loopback enforcement: a
non-loopback `--perf-addr` with `--perf-mcp` is refused). This is the in-process
way to profile a freeze in the embedded server itself.
The embedded server also accepts `--yolo` (the
allow-all operator posture — same semantics, root refusal, and `MECATL_SANDBOX`/
`IS_SANDBOX` env as `mecated`; see the allow-all note in §12). It is **rejected in
`connect` mode** — the dialed server owns its own posture. Note the TUI's **built-in slash commands**
(`/clear`, `/help`, and the caps-gated `/mcp`, `/agents`, `/team`, `/skills`,
`/soul`, `/usermodel`, `/reflections`, `/reflect`, `/models`, `/effort`, `/worktrees`) still work
regardless — they act on the TUI itself, not the server, so typing `/` always
opens a useful palette even with workspace slash-command expansion off
(`/agents` browses the agent-definition inventory; `/team`, also `ctrl+a`,
opens the live agent-team overlay; `/skills` the skills inventory; `/soul` and
`/usermodel` the persona/user-model views; `/reflections` lists bounded staged proposals and supports
CAS approve/reject/undo; `/reflect` explicitly reflects the current completed session even when
automatic learning is off; `/models` the model picker;
`/effort` picks the session's reasoning-effort tier (`auto`/`low`/`medium`/`high`/`xhigh`/`max`) and **restarts the session** to apply it (a per-session server setting, like a model switch);
`/worktrees` the sibling-git-worktree switch — it lists the repo's worktrees and,
on select, starts a NEW session rooted at the chosen worktree so all local tools
bind there; gated on the server advertising `worktrees`, so it is honestly absent
against a no-FS/cloud server). See
`docs/tui.md` for all flags.

It streams the conversation (glamour markdown for assistant text, themed cards
for tool I/O), shows a thinking spinner and a usage footer, and pops an inline
modal for permission asks that you approve/deny without leaving the stream. It is
themeable (Aztec default, plus `mono`/`solar`, plus drop-in JSON themes) and
respects the same trust model: it refuses to send `--auth-token` in cleartext to
a non-loopback server (use `--tls`). Full flag, key, and theming reference is in
**`docs/tui.md`**.

---

See also: the [HTTP/SSE API](http-sse-api.md) (the wire sibling), or the
[operator guide index](../usage.md).


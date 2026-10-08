---
title: gRPC API reference
description: Look up Mecatl gRPC services, methods, streams, and wire behavior.
sidebar_position: 2
---

# gRPC API reference

This reference describes gRPC services, event streams, and response behavior.
For client integration and transport selection, see
[Connect with gRPC or HTTP](/building/grpc-http.md).

For generated RPC signatures, streaming directions, messages, enums, fields, and
protobuf comments, see the [gRPC schema reference](/reference/grpc-schema.md).

Service: `mecatl.v1.HarnessService` (`contracts/proto/mecatl/v1/harness.proto`).

## Server identity

`GetServerInfo(GetServerInfoRequest) → GetServerInfoResponse` is a unary,
process-wide identity probe. Its optional `provider_id` selector must be the
caller's already-known active provider; it accepts no session or workspace
selector and does not create or inspect state. An absent or unknown
`provider_id` leaves the sanitized diagnostic display endpoint unavailable. Its
response contains these content-free strings:

|Field|Meaning|
|-|-|
|`build_id`|Linker-stamped build identity (`dev` for an unstamped source build).|
|`server_implementation`|Stable server composition family: `mecated`, `mecak8s`, or embedded `mecatui`; a generic embedding reports `unknown`. It is not an instance ID or a deployment label.|
|`llm_provider_display_endpoint`|Sanitized diagnostic display projection for the supplied `provider_id`, only when that provider is already available in composition; empty means unavailable. It is not connection configuration or a connection instruction. It contains only URL scheme, host, optional port, and escaped clean path.|

The RPC uses the same configured transport authentication as every
`HarnessService` RPC. With `mecated --auth-token` (or `MECATL_AUTH_TOKEN`), send
the configured bearer token; configured TLS or mTLS requirements apply as usual.
Do not treat this low-cost probe as public metadata: an unauthenticated call is
rejected when authentication is enabled.

This response is a privacy boundary. Apart from the sanitized display endpoint,
it contains no connection addresses, topology, configuration, capabilities,
authentication or TLS material, workspace paths, session or durable-store data,
prompts, credentials, or raw errors. The RPC performs a side-effect-free lookup
of the requested provider; it does not infer a default or session selection,
discover providers, re-read configuration, or report alternatives.

The RPC is additive. Clients talking to a server predating it receive
`UNIMPLEMENTED` and should degrade without surfacing the returned error body. A
client that receives an otherwise successful response with an absent or blank
`server_implementation` (for example, from a server before that additive field)
should use `unknown`; it must accept an unrecognized non-empty family label for
forward compatibility. `build_id` is an opaque display/comparison value, not a
semantic-version protocol.

## Sessions and runs

|RPC|Kind|Purpose|
|-|-|-|
|`CreateSession(CreateSessionRequest) → CreateSessionResponse`|unary|allocate a server-side session, return its id|
|`GetSession(GetSessionRequest) → GetSessionResponse`|unary|snapshot of an existing session, including authoritative title/provenance, title-generation lifecycle, and canonical durable token usage when present|
|`RenameSession(RenameSessionRequest) → RenameSessionResponse`|unary|replace an owned idle main session's title and mark its provenance operator-authored; this permanently disables automatic title generation; ownership, kind, state, liveness, and lease are revalidated at execution|
|`DeleteSession(DeleteSessionRequest) → DeleteSessionResponse`|unary|permanently remove an owned idle main session snapshot and store-managed sidecars; the same execution-time gates apply|
|`CompactSession(CompactSessionRequest) → CompactSessionResponse`|unary|force one configured compaction pass on an owned main chat at an idle or terminal boundary; creates no conversation turn and returns `compacted` to distinguish a rewrite from a successful no-op|
|`CloseSession(CloseSessionRequest) → CloseSessionResponse`|unary|end a session and release its server-side resources; idempotent|
|`ClearSession(ClearSessionRequest) → ClearSessionResponse`|unary|create a distinct empty-history successor. With no `worktree_selector`, inherit the source's exact placement and labels; a fresh source-scoped selector may choose one currently eligible worktree. The source is unchanged and failures publish nothing|
|`ForkSession(ForkSessionRequest) → ForkSessionResponse`|unary|create a history-carrying successor. Placement inherits exactly unless a fresh source-scoped `worktree_selector` is supplied; provider/model/reasoning overrides and placement resolve atomically. The source must be owned and at a legal turn boundary; failure creates no partial successor|
|`Converse(stream ConverseRequest) → stream ConverseResponse`|bidi|drive one agent run; the first frame is either a new `Prompt` or prompt-free `RetryStart`|
|`ApprovePlan(ApprovePlanRequest) → stream Event`|server-stream|Resolve a parked plan ask and stream the resumed run plus any approved continuation. See [Parked plan approval](#parked-plan-approval).|
|`StreamSessionEvents(StreamSessionEventsRequest) → stream Event`|server-stream|Replay the complete durable timeline, then end. Unknown IDs yield an empty stream; missing durable storage returns `UNIMPLEMENTED`. See [Inspecting a past session](#inspecting-a-past-session).|
|`WatchSessionEvents(WatchSessionEventsRequest) → stream WatchSessionEventsResponse`|server-stream|Replay durable events, then follow new appends with an opaque cursor. See [Durable replay and follow](#durable-replay-and-follow).|
|`ListSessions(ListSessionsRequest) → ListSessionsResponse`|unary|Stored-session inventory with picker metadata (ID, timestamps, state, turns, model ID; no conversation content), sorted most-recently-active first; an empty list when the store does not implement `PrunableStore`|

### Parked plan approval

`ApprovePlan` atomically resolves a parked plan-approval ask raised by
`PresentPlan` in plan mode. On an allow verdict, it starts a fresh continuation
run carrying the proceed message. One stream carries both runs' events.

|`target_mode`|Verdict and continuation|
|-|-|
|`DEFAULT`|Allow once, switch to default mode, and start the continuation.|
|`ACCEPT_EDITS`|Allow always, switch to accept-edits mode, and start the continuation.|
|`PLAN` or `UNSPECIFIED`|Deny and iterate in plan mode, with no mode switch or continuation.|

A live run returns `FAILED_PRECONDITION`; resolve its ask with the `Converse`
`resume_approval` frame instead. A session that is not `awaiting` a
`PlanOriginated` ask returns `FAILED_PRECONDITION` (`ErrNotAwaitingPlan`). An
unknown session returns `NOT_FOUND`.

### Durable replay and follow

`WatchSessionEvents` replays from an opaque `cursor`, then follows new appends.
An empty cursor starts at the beginning. Each frame contains
`{event, cursor, phase}`. The open-string `phase` values are `replay`, `live`,
and `gap`; clients must tolerate unknown values.

Exactly one `live` frame without an event marks the replay-to-live boundary.
This lets clients show a live view even when an idle session produces no new
event. An event-less `gap` frame identifies a known failed durable append.
Optional `run_id` narrows event delivery to one run; gap frames are delivered
with either filter. Like `StreamSessionEvents`, the watch includes log-only
events.

|Code|gRPC status|Meaning and recovery|
|-|-|-|
|`watch_unsupported`|`UNIMPLEMENTED`|The log does not support cursors.|
|`no_event_log`|`UNIMPLEMENTED`|No durable event log is configured.|
|`cursor_malformed`|`INVALID_ARGUMENT`|The cursor is invalid.|
|`cursor_expired`|`FAILED_PRECONDITION`|Restart from the beginning.|
|`watch_capacity`|`RESOURCE_EXHAUSTED`|Follower admission is full; resume with bounded backoff.|
|`watch_lagging`|`RESOURCE_EXHAUSTED`|The client did not consume the bounded buffer quickly enough; resume from the last processed cursor.|
|`activity_gap`|`DATA_LOSS`|Recorded events are missing; retry cannot recover them.|

Treat cursors as opaque bytes. Persist a cursor only after processing its frame,
then resume from that value with the same `run_id` filter. A filtered watch
advances past records it omitted, so changing or removing the filter can skip
those records silently. Start from the beginning to change filters.

`StreamSessionEvents` replays and ends. `StreamSessionLive` is process-local and
drops events for a slow client. `WatchSessionEvents` provides durable replay and
follow. Its guarantee covers durably appended events: a total backend outage
followed by loss of the watcher process can leave an unreportable gap.

### Server-owned placement

`CreateSessionRequest` has no workspace, cwd, exact EnvironmentRef, placement
ID, or worktree selector. Omitted `profile` binds the trusted deployment
default; `profile:"no-fs"` explicitly attenuates filesystem access. The response
and `GetSession` expose bounded `PlacementMetadata` only. Exact
`EnvironmentRef{Kind, ID, Revision}` remains private in the session snapshot and
trusted driver storage. Run entry reattaches that exact identity rather than
inferring it from a current default.

`ListCommandsRequest` and `ListWorktreesRequest` carry `session_id`, not a root.
The server owner-authorizes and exactly reattaches that source before discovery;
no-FS returns empty without invoking filesystem providers. Each worktree has
display-safe metadata and an opaque caller/source-scoped selector accepted only
by ClearSession or ForkSession. Local selectors expire on restart and must be
relisted; they are neither paths nor durable bearer IDs.

### Dedicated debugger creation

Set `CreateSessionRequest.profile = "no-fs"` and set `debug_target_session_id`
to the exact authorized target. Optional repeated `debug_mcp_servers` names only
already-configured server-global streaming-HTTP MCP servers. The response
advertises `session_debug`/`debug_mcp` and persists the selected names plus
exact tool ceiling. The resulting conversation uses ordinary `Converse`;
`InspectSession` returns evidence bound to the target incarnation. Every
selected MCP call, including a read-only tool, surfaces a fresh ordinary
`PermissionAsk` that the client resolves with `ResumeApproval`. Denies remain
absolute, headless calls deny, and allow-always executes only the current call:
it is not learned and the next call asks again. The target is never entered,
leased, or mutated by evidence reads.

### Client-provided MCP servers

`CreateSessionRequest.mcp_servers` mounts streaming-HTTP MCP servers for the
lifetime of the created session, via a per-session engine, so their tools and
their auth headers never leak into another session. Each entry carries `name`,
`url`, `type` (`"http"`, or empty with a `url`), and optional `headers`.

The field is **listener-scoped** as a separate outbound-network/credential
policy, not as workspace authority. Exactly one topology accepts it: a
`--grpc-unix-socket` listener with `--http-addr ""`. Every other deployment,
loopback TCP included, refuses every non-empty value with `UNIMPLEMENTED` / code
`client_mcp_unsupported`. A UNIX socket is guarded by filesystem permissions on
an owner-only directory; loopback TCP is reachable by other local processes. The
refusal is server-enforced.

Check `mcp_servers_on_create` in `GetCompatibilityInfo.features` before sending
the field; the advertisement and the enforcement read the same value, so an
advertised deployment will accept it and an unadvertised one will not. An empty
list is not a use of the feature and is accepted everywhere.

Mounting is atomic. Every requested server must connect or the create fails with
`UNAVAILABLE` / code `client_mcp_unreachable`, naming the servers that did not
answer; no session is created. Handle the two codes separately:
`client_mcp_unsupported` is permanent and a client should stop asking, while
`client_mcp_unreachable` is transient and the client's own endpoint to fix. The
server reports success only after every requested server connects.

Every deployment supports streaming-HTTP MCP transport only. A `stdio` entry, an
untyped entry carrying a `command`, or an `sse` entry returns
`INVALID_ARGUMENT`. Mecatl does not spawn MCP server processes.

Each `name` must be 1-64 characters of `[A-Za-z0-9._-]`, must not contain `__`,
and must be unique within the request; a violation is `INVALID_ARGUMENT`. The
rules are the tool namespace's, not cosmetic: names become
`mcp__<name>__<tool>`, so `__` inside one would forge another server's
namespace. Duplicate names would collide in the catalog and drop one tool set.
The namespace is flat and shared with operator-configured servers. A client
server named `github` can therefore match an operator permission rule written
for the configured server. This is one reason client servers are restricted to a
private local daemon.

Credentials belong in `headers`, and nowhere else. A URL carrying userinfo
(`https://user:pass@host/mcp`) is `INVALID_ARGUMENT`, because Go promotes it to
a `Basic` header that would bypass every protection `headers` gets. Header
values are secret-shaped: never logged, never carried in an event, never
included in an error. A URL is redacted to `scheme://host/path` wherever it is
logged or echoed, including message text, structured attributes, and underlying
transport errors that contain the full request URL. This redaction keeps
query-string tokens out of the operator's log.

A client endpoint may not redirect: a URL that passes validation is not
permitted to send the daemon onward to a host that never did.
Operator-configured servers are unaffected.

### Manual compaction

Check `CreateSessionResponse.capabilities.manual_compaction` before offering
this action. Call `CompactSession` with the owned session ID. The server runs
the configured compactor once even when the automatic 0.8 threshold has not been
reached. It does not start `Converse`, add a user message, or create a model
turn. A cascade pass may still invoke its summarization model through the
compaction slot.

`compacted=true` means the shorter history was saved. `compacted=false` is a
successful no-op for an empty, identical, or non-reducing candidate; nothing is
saved and no compaction events are appended. Idle, completed, cancelled, and
failed main chats are legal and retain their state. Running or awaiting
sessions, scheduled sessions, delegation children, and an in-process live run
return `FAILED_PRECONDITION`. Another replica holding the mutation lease also
returns `FAILED_PRECONDITION`. An absent or foreign-owned ID returns
`NOT_FOUND`, avoiding an ownership oracle; normal transport authentication still
applies. A save failure is `INTERNAL`. Once save succeeds, later event-log
append failure does not change the successful response or roll back the
compacted snapshot.

The capability is additive. New clients reading an old server see false and
should hide the action. Calling an old server's unknown method returns
`UNIMPLEMENTED`.

## Inventory, introspection, and learning controls

Inventory methods generally return snapshots; learning and refresh methods also
include mutations, as described below.

|RPC|Kind|Purpose|
|-|-|-|
|`ListModels`|unary|Selectable provider/model inventory with public metadata only; used by the `/models` picker.|
|`ListAgents`|unary|the discovered agent-definition registry (name, description, resolved model, tool scope)|
|`ListCommands`|unary|slash commands for an owned source session; owner-authorizes and exactly reattaches first; no-FS returns empty|
|`ListWorktrees`|unary|display-safe eligible worktrees plus opaque caller/source-scoped selectors for ClearSession/ForkSession; no paths or exact refs; relist after restart|
|`ListSkills`|unary|the discovered skills inventory (name + one-line description)|
|`GetSoul`|unary|the resolved soul's build-time snapshot: content, size/hash, provenance, trust + drift state|
|`GetUserModel`|unary|the **live**, bounded user-model index; optional `key` lazily returns exact read-only detail plus up to 16 revisions, including proposal linkage when present. Read-only; Forget remains a permission-gated tool|
|`ReflectSession`|unary|synchronously reflect one caller-owned completed session through its persisted provider/model (the reflection slot may change only the model); remains available with automatic mode off through lazy Build-owned initialization|
|`GetLearningAttempt` / `ListLearningAttempts`|unary|content-free lifecycle metadata from only the verified caller's private attempt partition; list accepts closed `state`, opaque `cursor`, and a bounded `limit` (default 50, maximum 200). Foreign and missing IDs are both `NOT_FOUND`; no attempt watch or EventLog projection is exposed|
|`RetryLearningAttempt` / `AbandonLearningAttempt`|unary|retry a failed attempt or non-compensatingly abandon an unclaimed nonterminal attempt using its opaque `expected_version`. Both mutate only the caller-partitioned attempt repository; stale versions, terminal conflicts, and live claims return closed conflict errors without changing state, and abandon promises no downstream rollback|
|`GenerateDreamPlan`|unary|spend one planner call to generate a bounded-lifetime review for exactly `project_memory` or `user_model`; returns displayed exact-duplicate and synthesized-replacement operations plus an opaque process-local plan id|
|`DecideDreamPlan`|unary|apply or dismiss the authoritative retained whole plan by id; accepts no operation content and returns planned/applied/conflicted/skipped/failed source counts|
|`ListLearningProposals` / `GetLearningProposal`|unary|bounded, cursor-paged proposal metadata in the verified caller partition; optional project partitions remain reviewable, while approve/undo is limited to the trusted launch root; evidence reports digest-verified availability without returning source text|
|`DecideLearningProposal`|unary|CAS approve or reject of a staged proposal; stale versions/transitions return `ABORTED`|
|`UndoLearningPromotion`|unary|CAS compensating revision only while the linked promoted memory revision remains current; stale/newer revisions return `ABORTED`|
|`ListMcpResources` / `ReadMcpResource`|unary|static MCP resource snapshots; read one resource by URI|
|`ListMcpPrompts` / `GetMcpPrompt`|unary|MCP prompt snapshots; expand one prompt to its rendered messages|
|`ListMcpSources`|unary|cached published/pre-shadow MCP sources plus revision, stale, and reconciling status; performs no probe|
|`RefreshMcpSources`|unary|reconcile direct MCP for an eligible owned root and return the request-pinned revision plus request-local changed result|
|`ListToolHiveGroups`|unary|the distinct ToolHive groups in the resolved inventory (no live ToolHive call)|

### Refresh MCP sources

`RefreshMcpSources` accepts only an owned ordinary root in `idle` or `completed`
state with no local active run or broker binding. An unchanged runtime and
already-complete authority union is a no-op with no write; additions are a
stable union. The returned revision belongs to the operation pin and need not be
the latest publication when delivery completes. Cancellation before save
prevents the authority mutation. Once save starts, cancellation or a storage
error can leave the result ambiguous; clients should inspect `ListMcpSources`
and retry the same idempotent refresh. Public load, reconciliation, and
persistence errors are generic and do not contain backend details.

### Manual dream review

Read `CreateSessionResponse.capabilities.manual_dream` to discover generation
and decision availability independently for project memory and the user model.
Generation sends the selected bounded values/descriptions to the configured
planner and spends tokens. The returned plan has a ten-minute process-local
lifetime; apply/dismiss is a whole-plan decision and regeneration is an explicit
second provider call. Exact duplicates leave the survivor unchanged; an approved
synthesis atomically rewrites its displayed survivor and tombstones its
displayed sources per operation. Independent operations can produce a partial
receipt. The API supports neither per-source decisions nor grouped undo.

Plan IDs are opaque and accepted only by the process that generated them.
Expiry, restart, or a wrong-replica request returns `NOT_FOUND`; the old
decision cannot be retried and the client may offer explicit fresh generation.

Same decisions are idempotent. A same-decision request while apply is still
running returns `ABORTED` and may be retried explicitly to retrieve the receipt.
An opposite request against an applying record returns `FAILED_PRECONDITION` and
is non-retryable; against a terminal record it returns `ALREADY_EXISTS`, after
which explicit fresh generation is safe. Genuinely indeterminate transport
errors preserve the exact ID and decision for same-decision retry because the
first request may already have applied. No error state enables the opposite
decision.

Registry pressure returns `RESOURCE_EXHAUSTED`, and an unavailable target
returns `UNIMPLEMENTED`. Manual review is disabled under ownership enforcement
and when the planner or both reviewed atomic store capabilities are missing. It
is separate from `learning.mode` and the off-by-default consolidation interval
flags, and exposes neither recall counters nor provider/model identity.

## Agent teams

Team RPCs are experimental and enabled by default through `--enable-teams`.

|RPC|Kind|Purpose|
|-|-|-|
|`CreateTeam(CreateTeamRequest) → CreateTeamResponse`|unary|allocate a team (optionally enrolling an initial roster); accepts the tighten-only `max_team_tokens`|
|`SpawnTeammate`|unary|Enroll a member in an existing team (before `RunTeam`)|
|`SendTeammateMessage`|unary|post a message into a member's inbox, delivered at its next turn boundary|
|`CancelTeammate`|unary|cancel one member of a running team mid-round: it de-schedules with the `cancelled` stop reason and releases its claimed tasks; the team still delivers its report. Not-running team → `FailedPrecondition`; unknown member → `NotFound`|
|`RunTeam(RunTeamRequest) → stream TeamEvent`|server-stream|Run the team to quiescence. Member events carry the member name; one final `TeamEvent.outcome` frame contains rounds, stop, `budget_exhausted`, usage, dispositions, and findings.|
|`ListTeam`|unary|snapshot of the roster, shared task list, and quiescence|
|`CleanupTeam`|unary|tear down a finished team and release its resources|

`CreateTeam` and pre-run `SpawnTeammate` validate and record roster
declarations; they do not create member engines, workspaces, or durable member
sessions. `RunTeam` constructs those resources under its operation-pinned
runtime, which is not necessarily the latest revision when the stream completes,
and persists members as they run. A configured member store that supports atomic
member creation must also support session deletion so partial startup can be
rolled back; otherwise `RunTeam` fails before member enrollment. Factory or
workspace-fork failures surface from `RunTeam`. On startup failure, the server
closes constructed member resources, then attempts to remove each abandoned
snapshot while its mutation lease remains held, and only then releases the
lease. Snapshot removal is best-effort: a lost lease or deletion failure can
leave the snapshot, and the server reports
`abandoned team member snapshot could not be deleted; left for retention`. The
supported recovery is the configured child-session retention or an authorized
storage cleanup after verifying the session is not live; see
[Session storage operations](/operating/session-storage-operations.md). The
server retains team declarations and queued messages so a caller can retry
`RunTeam`, but that retry does not guarantee that a leftover snapshot has
already been removed.

## The `Converse` flow

The bidi stream drives exactly one run:

1. The client sends the first frame. Use `Prompt{session_id, text, parts}` for a
   new user message; `parts` optionally carries multimodal media (image/audio
   `Content` parts, capability-gated on the session's provider). Use
   `RetryStart{session_id}` to repeat an eligible failed model step without
   adding another prompt. A first frame of any other kind is `InvalidArgument`.
2. The server streams `ConverseResponse{Event}` envelopes in sequence order.
3. On a `permission.ask` event, the client sends a control frame
   `ResumeApproval{ask_id, verdict}`. `ask_id` echoes `Event.ask.ask_id`, and
   `verdict` is the three-way resolution: `DENY`, `ALLOW_ONCE`, or
   `ALLOW_ALWAYS` (which also learns a per-session allow rule for the same
   tool + exact canonical pattern; it never overrides a deny or plan-mode
   mutation denial). The legacy `allow` bool still works when `verdict` is
   unset: `true` → allow once, `false` → deny.
4. The client may send `Cancel{}` to abort the run, which terminates with a
   `result` whose `stop` is `"cancelled"`. `CancelChild{child_id}` cancels one
   delegated child (subagent, parallel branch, or team member) while the parent
   run keeps streaming.
5. The server emits a terminal `result` event and closes the stream.

`ConverseRequest` is a `oneof`:

|Field|When|
|-|-|
|`prompt` (`Prompt{session_id, text, parts}`)|first frame for a normal run; records a new user message|
|`retry` (`RetryStart{session_id}`)|first frame for a prompt-free failed-step retry; records no prompt, reuses conversation/tool state, re-resolves live instruction sources, and delegates eligibility to persisted server state|
|`resume_approval` (`ResumeApproval{ask_id, verdict, allow}`)|resolve a paused ask (three-way `verdict`; the `allow` bool is the legacy fallback)|
|`cancel` (`Cancel{}`)|abort the in-flight run|
|`cancel_child` (`CancelChild{child_id}`)|cancel one child run by its id (the `agentId:` / `child_id` handle), leaving the run and sibling children untouched; unknown/finished ids are ignored on the stream|
|`steer` (`Steer{...}`)|Queue an operator instruction for the next turn boundary; receive a `steer.outcome` acknowledgement.|
|`steer_cancel` (`SteerCancel{...}`)|Retract the pending steer bundle; receive a `steer.outcome` acknowledgement.|

A received second `prompt` or `retry` is `InvalidArgument`; unknown control
frames are ignored. A single `Converse` stream drives a single run. Because the
server closes the stream on the terminal result, a control frame still in
transit at that boundary may instead observe normal EOF.

For a failed model stream, inspect the terminal Result's optional
`retry_disposition` and `stream_progress`. New servers set both fields even when
the value is `UNKNOWN`; an absent field identifies an older server and must not
be treated as safely retryable. `RetryStart` is prompt-free: the server persists
aggregate-owned retry intent, blocks ordinary prompts while it is pending, and
skips prompt hooks and the first retry turn's boundary injections. This avoids
duplicate prompts and tool effects. Clients may explicitly retry typed
`retryable` failures at `precommit` or `visible`. The server owns automatic
precommit provider recovery inside the active run; clients such as `mecatui`
do not start another run automatically after a terminal error. An explicit
`/retry` in `mecatui` uses `RetryStart` as a new action.

## Event envelope

Every event uses the provider-neutral `Event` message, which mirrors the domain
`session.Event`:

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
  Team          team        = 13;  // team.*: bounded team projection (start/member/tasks/findings/end)
  Parallel      parallel    = 14;  // parallel.*: REDACTED fork-join projection (incl. winner + fork paths)
}
```

The three delegation families (`subagent.*`, `team.*`, `parallel.*`) project a
child loop's lifecycle without leaking its content: all three carry the same
bounded previews: IDs, tool names/counts, usage, stop, and capped,
control-byte-scrubbed `text`/`detail` previews of child message text and tool
args/results; the task board, findings ledger, dispositions, mutating cue, and
context meter stay Team-only. Every preview is capped and a child's permission
asks are never forwarded.

`Result.stop` is one of: `end_turn`, `max_turns`, `max_tool_calls`,
`max_consecutive_failures`, `budget`, `no_progress`, `cancelled`, or `error`.
`budget` means the `--max-run-tokens` ceiling was crossed and leaves a terminal
session that can be reopened. A child's stop on `subagent.end` or in a Subagent
result can also be `structured_output` when it exhausts structured-output
validation retries. A plan-approval allow emits `plan_approved`, which flips
the session out of plan mode at the terminal boundary.

`error` includes an upstream provider content filter blocking a response. Some
routes, including Azure OpenAI upstreams, can moderate benign security or
credentials wording. Mecatl treats this as a terminal stop and does not retry
the same input. Retype or resend the triggering message. Repeated blocks on
legitimate input require a provider-side moderation change.

## Go client snippet

`mecated` does not register gRPC server reflection. `grpcurl` requires the proto
files and their `buf.validate` import. The following generated Go client example
connects to an unauthenticated loopback server and approves every ordinary
permission ask. Adapt the approval policy before using it with real workloads:

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
        Mode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
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

`GetSession` returns a snapshot (`session_id`, `state`, `mode`, bounded
`placement` metadata, `limits`, `turns`, `tool_calls`, `created_at_unix`). It
never returns the exact private EnvironmentRef or a filesystem path.

## Inspecting a past session

`ListSessions` returns stored-session inventory sorted by most recent activity.
Rows contain ID, timestamps, state, turn count, and model ID, without
conversation content. `StreamSessionEvents` replays the complete durable
timeline, including log-only `approval`, `compaction_archive`, and `user_prompt`
events omitted from live `Converse` delivery. Scheduled delivery retains its
live-event exception.

`UserPrompt.synthetic=true` identifies a server-authored continuation. An absent
or false value means a genuine prompt or an event whose origin is unknown.
Clients must use this metadata rather than infer origin from text. Replay events
are already metadata-only or redacted.

An unknown ID yields an empty replay stream. A server without durable `EventLog`
returns `UNIMPLEMENTED`; inventory is empty when the store does not implement
`PrunableStore`. Neither method has a `ServerCapabilities` bit, so clients
discover support through these results.

## The terminal UI (`mecatui`)

`mecatui` uses the same gRPC `Converse` stream as other clients. It builds to
`bin/mecatui` with `task build`. Bare `mecatui` hosts an in-process server over
a private UNIX socket using the shared composition layer; `mecatui connect`
dials an external server.

```sh
export OPENAI_API_KEY='<OPENAI_API_KEY>'
bin/mecatui                             # embedded server
bin/mecatui --mock                      # embedded offline provider

bin/mecated serve &                     # external server owns its configured root
bin/mecatui connect 127.0.0.1:8080       # no workspace or cwd crosses the API
```

### Embedded capabilities

The embedded server enables local features by default: project memory under
`$XDG_DATA_HOME/mecatui/memory`, server-side command expansion from
`.mecatl/commands` and `.claude/commands`, conventional skill discovery, agent
definitions, soul, user model, child-session retention, ToolHive MCP discovery,
and MCP resource/prompt meta-tools.

Static `--mcp-server` registrations, the `SkillDraft` quarantine, the background
user-model reviewer, and memory consolidation require explicit configuration.
Use a configured `mecated serve` and connect to it for these integrations.

### Embedded diagnostics

`--perf` is off by default. Enabling it exposes `/metrics`, `/debug/pprof/*`,
`/debug/vars`, and `/debug/flightrecorder`. With no `--perf-addr`, it uses an
owner-private `admin.sock` beside the instance's private gRPC socket, avoiding
collisions among simultaneous clients.

An explicit `--perf-addr host:port` selects TCP and must be loopback;
`127.0.0.1:0` requests an ephemeral port. Startup logs report the resolved
network and address. `--perf-goroutine-warn-threshold` enables the goroutine
alarm.

With `--perf-mcp`, an empty address selects ephemeral loopback TCP because the
MCP transport requires a streaming-HTTP URL. Startup logs report the resolved
`/mcp` URL. Mecatl uses no stdio MCP transport and does not inject raw admin
data into the model or dedicated session debugger.

### Client controls and posture

The embedded server accepts `--yolo` with the same allow-all semantics,
root-user refusal, and `MECATL_SANDBOX`/`IS_SANDBOX` environment checks as
`mecated`. Connect mode rejects `--yolo` because the external server owns its
posture. See
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md).

Client built-in commands remain available when workspace command expansion is
off. Typing `/` opens the palette. `/clear` and `/help` are built in; other
commands depend on the server's advertised capabilities:

|Command|Client behavior|
|-|-|
|`/mcp`|Browse MCP integration.|
|`/agents`|Browse agent definitions.|
|`/team`|Open the Teams tab in the Agents overlay.|
|`/skills`|Browse skills.|
|`/soul`|Show the persona.|
|`/memory`|Show saved memory.|
|`/reflections`|List bounded staged proposals and support compare-and-swap approve, reject, and undo.|
|`/reflect`|Reflect the current completed session even when automatic learning is off.|
|`/models`|Choose a model.|
|`/effort`|Choose `auto`, `low`, `medium`, `high`, `xhigh`, or `max`; restart the session to apply the per-session setting.|
|`/worktrees`|List eligible sibling worktrees and start a new session at the selected worktree. Requires the `worktrees` capability and is absent on no-filesystem/cloud servers.|

The client renders Markdown responses and tool cards, shows a thinking indicator
and usage footer, and presents inline approval dialogs. It provides the `aztec`,
`mono`, and `solar` themes plus custom JSON themes. It refuses to send
`--auth-token` in plaintext to a non-loopback server; use `--tls`.

For workflows, see [Work in the TUI](/mecatui/using-the-tui.md),
[Connect to a server](/mecatui/remote-servers.md),
[Keybindings](/mecatui/keybindings.md), and [Themes](/mecatui/themes.md).

---

## Related information

- [HTTP/SSE API reference](./http-sse-api.md)
- [Connect with gRPC or HTTP](/building/grpc-http.md)
- [Work in the TUI](/mecatui/using-the-tui.md)

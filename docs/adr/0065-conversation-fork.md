# ADR 0065 — Conversation fork: peer session from a history snapshot

- Status: Accepted
- Date: 2026-07-14
- Scope: a new `ForkSession` wire RPC + HTTP route, a `Service.ForkSession` method in `internal/adapter/server`, reuse of existing domain primitives (`session.ForkSnapshot`/`SeedHistory`/`New`); no `engine/`, `port`, or domain-package change
- Supersedes: none
- Superseded by: [ADR 0071](./0071-seamless-model-switch.md) (in part — the same-provider constraint for the *carryover* path; the `ForkSession` RPC itself remains same-provider/model-locked); [ADR 0343](./0343-session-configuration-generations.md) only where carryover generation identity was out of scope

## Context

mecatl sessions are conversational: a `Session` aggregate holds an immutable-element `Conversation` (`[]Message`), persisted as a `sessnap.Snapshot` checkpoint plus an append-only `EventLog` timeline (ADR 0038). A session is single-threaded per id (the run-entry funnel `loadAndReopen` recovers a terminal state before a new prompt, and the optional `port.SessionLease` enforces single-writer across processes — ADR 0027 Phase 4). There is today **no way to branch a conversation**: once two divergent intentions appear ("continue the feature" vs "fix the bug first"), the operator must either interleave them in one session's history (confusing the model and polluting the context) or start a fresh session and manually re-establish context.

The subagent `fork:true` feature (issue #34, ADR 0033) already solves the mechanical core of this — `session.ForkSnapshot` (`engine/session/conversation.go`) clones the conversation with a fresh backing array and strips trailing unanswered tool calls so the snapshot is always tool-pairing-valid, then `session.SeedHistory` loads it into a fresh `StateIdle` aggregate. But that fork targets a **read-only, less-privileged sandboxed child** running in a throwaway git worktree: a subagent, not a peer session. The trust model (trust-neutral, verbatim history, byte-stable cache prefix) and the workspace handling (isolated worktree, discarded after the run) are shaped by that sandboxing need, not by a fork need.

The forces:

1. **The domain primitives are exactly right and already tested.** `ForkSnapshot` + `CloneMessages` + `SeedHistory` + `New` are general-purpose; the subagent-specific concerns (worktree forker, read-only catalog, child sandbox, same-provider-only-via-parent-engine) live in `engine/agent`, not in the domain. Reusing them for a peer fork adds zero domain code.
2. **A peer fork is a full, equally-trusted session**, not a sandboxed child. It gets its own engine (rehydrated to the source's provider/model/profile — Cloud-Native Phase 1, ADR 0027), a full catalog, full permissions, and its own `Counters`/`Usage`/lifecycle. The trust posture is "the operator trusts both sessions equally" — the same posture as two sessions the operator created independently.
3. **Same provider and model only.** The snapshot carries provider-private replay blobs (`Message.Reasoning`, `ProviderPhase`, `ToolCall.ItemID`) that a different provider cannot consume and a different model would mis-replay. This is the same constraint the subagent fork enforces (`validateFork` rejects `fork`+`model`), and it is honest: a cross-provider fork would need to strip/migrate those fields, trading byte-stable cache for portability — out of scope here.
4. **No workspace fork.** Unlike the subagent's throwaway worktree, a peer fork inherits the source session's `Workspace` path string verbatim. The session does not lock its workspace directory; two sessions pointing at the same path is already fine (no different from two editors on the same repo). The `WorkspaceForker`/worktree machinery stays in the subagent path.
5. **Fork at a turn boundary.** The natural UX is: receive a response, decide to branch, fork, send different prompts to each. Forking mid-stream would snapshot a conversation whose in-flight assistant message isn't committed yet (`RecordAssistant` fires only after streaming completes) — a stale prefix. `ForkSnapshot`'s trailing-orphan strip already handles a `running`/`awaiting` source safely (whole-turn strip), but the supported path is a `completed`/`idle` source.

## Decision

Add a **`ForkSession`** operation: create a new peer session whose conversation history is a snapshot of an existing session's, inheriting the source's mode, workspace, limits, and provider/model/profile labels. Same provider and model only.

### The operation

`Service.ForkSession(ctx, srcID) (newID, error)` in `internal/adapter/server/service.go`:

1. **Load the source** via the existing run-entry funnel `loadAndReopen(ctx, srcID)`, so a terminal source is recovered to `idle` first (`completed→Reopen`, `cancelled→Interrupt`, `failed→Recover`) and approval replay runs. A `running`/`awaiting` source is rejected with `ErrFailedPrecondition` ("fork requires a session at a turn boundary; the source is running/awaiting") — the operator waits for the turn to finish or cancels first.
2. **Snapshot** via `session.ForkSnapshot(src.Conversation)` — fresh backing array, trailing orphans stripped, tool-pairing-valid.
3. **Mint** a new aggregate: `session.New(s.cfg.NewID(), src.Mode, src.Workspace, src.Limits, s.cfg.Now())`.
4. **Seed** the copied history: `new.SeedHistory(snapshot)`.
5. **Inherit labels** — copy `src.Profile`, `src.ProviderID`, `src.ModelID`, `src.ReasoningEffort`, `src.Title` onto the new session (the same labels `rehydrateSession` reads to rebuild the matching engine). The new session starts `idle` with zeroed `Counters` and zero `Usage`.
6. **Persist** the new session via `s.cfg.Store.Save`.
7. **Rehydrate the engine** via the existing `rehydrateSession` path (so the fork runs on the source's provider/model/profile, not the default floor) and register it.
8. Return the new session id.

No `engine/`, `port`, or domain package changes. The only production-code additions are the `Service.ForkSession` method (composition) and the wire handlers.

### Wire surface

A new gRPC RPC and HTTP route, additive to the `Mecatl` service:

- `rpc ForkSession(ForkSessionRequest) returns (ForkSessionResponse);` — `ForkSessionRequest{ string source_session_id = 1; string title = 2; }`, `ForkSessionResponse{ string session_id = 1; }`. An empty `title` inherits the source's title verbatim; a non-empty `title` overrides it so the operator can distinguish "implement feature X" from "fix the bug first" in `ListSessions` without a separate rename RPC.
- HTTP `POST /v1/sessions/{source}/fork` with an optional `{"title": "..."}` body → `{ "session_id": "..." }`. An empty/absent body inherits the source's title.

No streaming — the fork is synchronous (load + snapshot + save + rehydrate). No new event type; the new session's own `EvSessionInit`/event log records its lifecycle from the fork point forward. The source session's conversation is never modified — `loadAndReopen` may re-persist a terminal source (Reopen/Interrupt/Recover to idle) as a side effect, but its history is untouched.

### Trust model

Peer sessions, equal trust. The forked history is carried **verbatim, not re-fenced** — for the same reason as the subagent fork: the main loop records tool results raw, the fork introduces no new untrusted ingress (the history came from a session the operator already trusted), and re-fencing would bust the byte-stable prompt-cache prefix the fork relies on. Both sessions now own independent copies of the conversation (fresh backing array guarantees no aliasing) and diverge from the fork point. The operator manages the shared workspace the same way they manage two editors on the same repo.

### Same provider/model only

The fork inherits the source's `ProviderID`/`ModelID` labels and rehydrates the matching engine. There is no `model`/`provider` override parameter on `ForkSessionRequest`. A fork-then-switch-model flow is explicitly out of scope (it would require stripping `Reasoning`/`ProviderPhase`/`ItemID` from the copied messages — a v2 concern).

## Consequences

**Positive.** The operator can branch a conversation at a decision point without losing context or polluting a single session's history — the headline capability. The implementation reuses tested domain primitives and adds no domain code; the new surface is one service method + two wire handlers. The forked session is a first-class session: it appears in `ListSessions`, can be resumed, cancelled, and forked again (forking a fork is fine — `ForkSnapshot` is idempotent over a clean history). The shared workspace is a feature, not a bug, for the "I'm mid-feature and want to fix a bug first" use case.

**Costs.** A new wire RPC + HTTP route to maintain (additive, `task generate` regenerates the proto bindings). A new `Service.ForkSession` method (~30 lines). The forked session inherits the source's `Limits` verbatim — an operator who forked a long-running session gets two sessions each with the original turn/tool budget (likely desirable, but worth noting). Two sessions on the same workspace can produce conflicting file edits if run concurrently; this is the operator's responsibility (same as any two agents on one repo) and is NOT mitigated by the session lease (the lease is per-session-id, not per-workspace-path). The same-provider constraint is honest but limiting — an operator wanting to fork-and-experiment-on-a-cheaper-model must wait for v2.

**Not done here (deliberately).** No cross-provider/model fork (would require replay-blob stripping — v2). No workspace fork/branch (the peer fork shares the workspace; a workspace-branching fork is a different feature). No fork-from-event-log-at-arbitrary-point (the event log supports this via `eventsource.Fold`, but the supported path is fork-from-latest-snapshot; a "fork from turn N" UI is a future step that would replay the log up to a cut-off). No fork lineage tracking (the new session does not record its source; if needed later, a `forked_from` label is additive).

## See also

- [ADR 0033 — Dirty-aware read-only fork](./0033-dirty-aware-readonly-fork.md) — the subagent worktree fork whose `ForkSnapshot`/`SeedHistory` primitives this reuses.
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the `rehydrateSession` Phase-1 path the forked session's engine is rebuilt through.
- [ADR 0038 — Event-sourced rehydration](./0038-event-sourced-rehydration.md) — the event-log `Fold` that makes arbitrary-point reconstruction possible in a future v2.
- [ADR 0077 — Direct-write subagent](./0077-direct-write-subagent.md) — the trust-model precedent for a non-sandboxed child (peer-parity).
- `CreateSession.source_session_id` (issue #20) reuses the same `ForkSnapshot`/`SeedHistory` primitives for same-provider conversation carryover on model switch — a client-side session-create variant under the same same-provider constraint.
- [`docs/architecture.md`](../architecture.md) — the sessions/storage section (fork paragraph added when implementation ships).
- [`docs/design/PRODUCTION-READINESS.md`](../design/PRODUCTION-READINESS.md) — the status tracker (conversation-fork row added when implementation ships).
- [`docs/usage.md`](../usage.md) — the operator guide (ForkSession route added when implementation ships).

# ADR 0005 — Driver seams: ports, the gRPC driver protocol, and conformance

- Status: Accepted
- Date: 2026
- Scope: every externalizable store/source in the harness — session store, memory, skills, soul, agent defs, slash commands, event log
- Superseded by: [ADR 0108](./0108-on-demand-logical-skill-assets.md) — ONLY the skill-asset materialization/read-root decision; all other decisions remain authoritative

## Context

The harness needed its stores and content sources to be swappable across process boundaries without coupling the engine to gRPC or proto types. The risk was either over-coupling (proto types leaking into engine ports) or under-specifying the contract (divergence between in-process and remote implementations). Six seams needed to cross process boundaries reliably, each with different snapshot-vs-live semantics and different failure postures.

## Decision

Each externalizable store/source is a minimal Go interface in the engine. A single versioned gRPC driver protocol (`contracts/proto/mecatl/driver/v1/`) is the ONE remote path — never multiple transports. In-process reference adapters are both the defaults and the conformance baselines; the conformance suites are the contract. Every driver client (and matching server wrapper) lives in one adapter family. Non-local cleartext is refused for all driver dials. Trust is enforced at source construction in composition, never re-checked downstream.

## Consequences

A driver author in any language proves conformance by running the exported Go suites against their endpoint. Snapshot format evolution uses additive JSON fields under the existing `sessnap-json/1` tag; a format-tag bump is reserved for encoding replacement only. The workspace/FS driver is deliberately left as a sketch (the one unimplemented seam) because it breaks the 64 MiB unary rule and the Bash-needs-real-exec constraint. Server-wrapper promotion to a public API is deferred until an external Go consumer exists. Current behaviour is in `docs/architecture.md`; shipped and deferred items are in `docs/design/PRODUCTION-READINESS.md`.

---

This doc records the pattern, the seam inventory, the wire rules, and the
deliberate deferrals. The operator-facing flags live in `docs/usage.md`
("Remote store drivers" / "Remote content-source drivers"); the mechanics and
the full conformance run matrix live in `docs/architecture.md` ("Remote store +
source drivers"); per-phase implementation detail is in
`docs/design/IMPLEMENTATION-NOTES.md` (the "Store drivers" / "Source drivers"
subsections). Don't duplicate those — this is the rationale layer.

## The pattern

Every externalizable store/source the harness consumes sits behind a small Go
interface in the engine, implementable two ways: the in-process reference
adapter (the default), and ONE uniform remote path — a versioned gRPC **driver
protocol** — so a driver can be implemented in any language/process without
linking into mecatl. Five rules, in dependency order:

1. **Ports are Go interfaces in the engine.** `port.SessionStore`
   (`engine/port/store.go`), `tool.MemoryStore`/`tool.SkillSource`/
   `tool.AgentDefSource` (`engine/tool/`), `prompt.SoulSource`/
   `prompt.CommandSource` (`engine/prompt/` — consumer-local, the house
   pattern). They are minimal and consumer-driven: no method exists without a
   consumer (the interface lost `Remember` when its only caller was a
   wrapper). When a NEW capability gains a consumer it lands as a SEPARATE
   OPTIONAL interface, never by widening the base port: the child-session
   retention GC (issue #38) consumes `port.PrunableStore` (List/Delete +
   `StoredSession`), which a store MAY also implement — discovered by type
   assertion, so a Save/Load-only store (in-process or remote) is simply
   never swept. List is an unfiltered inventory and Delete is idempotent;
   retention POLICY (child-id prefixes, age, per-family caps, liveness)
   stays in composition (`internal/app/childgc.go`), never in a store.
2. **gRPC is the DRIVER PROTOCOL, never the port.** Engine layering bans
   gRPC/proto from core and that stays — no proto type appears in any port
   signature, and the DAG test + depguard would reject one. The protocol is
   `contracts/proto/mecatl/driver/v1/*` (one service per seam, one shared
   `mecatl.driver.v1` package; buf STANDARD lint; regenerate via
   `task generate`, never hand-edit `contracts/gen`).
3. **One adapter family translates at its boundary.**
   `internal/adapter/grpcdriver/` holds every driver client (and the matching
   server wrappers, see Deferred §1); each client implements the Go port over
   the corresponding service and confines all proto/status translation to
   itself. Read `internal/adapter/grpcdriver/doc.go` first — it is the
   package-level statement of the trust model, wire format, and resilience
   posture.
4. **In-process reference adapters are the defaults AND the conformance
   baselines.** `memstore`/`jsonlstore` (sessions), the flock-file
   `memory.Store`, `skills.FSSource`, `soul.Store`, `agents.FSSource`. All
   driver selection is composition-only (`internal/app/build.go` +
   `cmd/mecated` flags); all-empty config is byte-identical to the local
   adapters. Auth/TLS to drivers is a construction-time option on the dial
   (`grpcdriver.Dial`), never per-port API surface.
5. **The conformance suites ARE the contract.** `engine/adapter/
   storeconformance` (SessionStore via `Run`, plus the optional
   PrunableStore retention seam via `RunPrunable` — unfiltered List with
   sane ModifiedAt, Delete idempotent on unknown ids, deleted ids vanish
   from List and Load), `memconformance` (MemoryStore), and
   `sourceconformance` (RunSkillSource / RunSoulSource / RunAgentSource /
   RunCommandSource, with exported canonical fixtures). Every reference
   adapter passes its suite, and the grpcdriver clients pass the SAME suite
   over a bufconn wire (client → server wrapper → reference backend), so a
   remote driver cannot drift from in-process semantics. The full run matrix
   is the table in `docs/architecture.md`.

**How a driver author proves conformance.** Each suite is
`Run(t, newImpl func(t *testing.T) <Port>)` — hand it a constructor that
returns YOUR implementation of the Go port. For a remote endpoint that means
the grpcdriver client dialed at your service
(`storeconformance.Run(t, func(t) port.SessionStore { return
grpcdriver.NewSessionStore(conn) })` — exactly what
`internal/adapter/grpcdriver/conformance_test.go` does over bufconn). The
suites live under `engine/adapter/` and are importable; the grpcdriver client
needed to point them at a live endpoint is still `internal/` — running against
a real remote endpoint is therefore an in-repo test today, and exporting the
client/wrappers for external Go consumption is the recorded Deferred §1.

## Seam inventory

| Seam | Port (home) | Lifecycle | Reference adapter | Driver service | Conformance | Failure posture |
|---|---|---|---|---|---|---|
| Sessions | `port.SessionStore` (`engine/port/store.go`) + optional `port.PrunableStore` (retention List/Delete) | live per Save/Load | `memstore` (default), `jsonlstore` | `SessionStoreService` | `storeconformance.Run` + `RunPrunable` | no build probe (lazy dial); a runtime fault is the same unit failure a disk error would be; a driver that cannot enumerate answers List/Delete with UNIMPLEMENTED, mapped client-side to `port.ErrPruneUnsupported`; the sweeper logs one INFO and stickily disables itself — never swept, never re-WARNed |
| Memory | `tool.MemoryStore` (`engine/tool/tool.go`) | live per tool call | flock-file `memory.Store` | `MemoryStoreService` | `memconformance.Run` | dial fail at build = FATAL (explicit URL = loud-misconfig); runtime fault surfaces as the tool's error |
| Skills | `tool.SkillSource` (`engine/tool/skillsource.go`) | **snapshot** at build | `skills.FSSource` | `SkillSourceService` | `sourceconformance.RunSkillSource` | build snapshot fail = FATAL; activation-time materialization fault = model-addressable tool error, never a partial bundle |
| Soul | `prompt.SoulSource` (`engine/prompt/soul.go`) | load per turn 0 | `soul.Store` | `SoulSourceService` | `sourceconformance.RunSoulSource` | build `Probe` fail = FATAL; runtime fault = `("", nil)` + WARN (the port's fail-soft contract) |
| Agent defs | `tool.AgentDefSource` (`engine/tool/agentsource.go`) | **snapshot** at build | `agents.FSSource` | `AgentSourceService` | `sourceconformance.RunAgentSource` | build snapshot (ONE `ListAgentDefs`) fail = FATAL; no runtime RPCs exist |
| Slash commands | `prompt.CommandSource` (`engine/prompt/commandsource.go`) | **live** per expand/list | none — `DirCommandExpander` is the workspace-tier surface, deliberately NOT a port impl | `CommandSourceService` | `sourceconformance.RunCommandSource` | build `Probe` fail = FATAL; runtime fault FAILS SOFT (WARN + pass-through), never latches a command "missing" |
| Event log | `port.EventLog` (`engine/port/eventlog.go`) — Append + server-streaming Read | live per relayed event (Append) / per replay (Read) | `jsonlstore` (the Store doubles as its own log), `memstore.EventLog` (offline sibling) | `EventLogService` | `eventlogconformance.Run` | no build probe (lazy dial, INDEPENDENT of the session store via `--event-log-url`); an Append fault is a relay WARN that NEVER aborts the run (a broken log must not break the live stream); a Read miss is an EMPTY stream, never NOT_FOUND |

The snapshot-vs-live split is principled, not incidental:

- **Skills and agent defs are SNAPSHOT** because the build-once
  trust-gate-completeness invariant depends on it: per-skill read roots are
  opened at workspace construction, per-def child engines are baked once, and
  the trust gate runs at source CONSTRUCTION (below). A watch seam would
  reopen all of that mid-run — see Deferred §3.
- **Commands are LIVE** because the file expander is live today (the palette
  promises the CURRENT command set), and nothing trust-completeness-shaped
  hangs off a command: no per-def engine, no read roots, no always-in-context
  surface — the body lands in the USER channel at invocation. A live driver
  therefore must fail SOFT at runtime (`MultiExpander.List` aborts the whole
  palette walk on a child error, so a transient blip must not propagate) and
  deliberately does NO latching — a fault must never latch a command missing,
  and invalid names are re-dropped statelessly per List.
- **Soul is load-per-call** (per-session turn 0) with the pre-existing
  fail-soft contract: persona is best-effort context, never correctness.

## Wire rules

**Opaque sessnap blob, format-tagged.** Session snapshots cross as
`bytes payload` + `string format` where the payload is exactly
`engine/adapter/sessnap` output and `format` is
`grpcdriver.SnapshotFormat = "sessnap-json/1"`. The driver stores/returns the
envelope VERBATIM and never decodes it; `session_id` is duplicated top-level
on Save so a driver can key/index without decoding (and it is the
AUTHORITATIVE key — the server wrapper rejects a Save whose payload disagrees,
the client rejects a mis-keyed Load, both as infra errors, never not-found).
Schema evolution is split: sessnap owns it (additive JSON fields), the
envelope owns format identification — the tag changes ONLY if the encoding
itself is replaced, and a future bump MUST be read-set-accept / write-newest
or every stored session bricks. The harness rejects an unknown format on Load
with an infrastructure error, never `ErrSessionNotFound`.

The **event log crosses the same way**: each event is a `bytes payload` +
`string format` where the payload is `json.Marshal` of a `session.Event` and
`format` is `grpcdriver.EventLogFormat = "eventlog-json/1"` — the SAME tag the
local `jsonlstore` writes inside its on-disk record, so the wire and the file
version the one event encoding. The driver round-trips the envelope VERBATIM
and never decodes a typed event field (no typed `session.Event` fields ever
ride the wire — gauntlet #7 inherited: the event stream is already
metadata-redacted at the relay). The server wrapper re-decodes the payload into
a value `session.Event` only to feed its value-typed port backend (exactly the
SessionStore wrapper's snapshot decode), and re-encodes on Read; the wrapped
backend never sees the wire bytes. The same read-set-accept / write-newest
discipline applies to a future event-format bump.

**Capacity: 64 MiB unary, no streaming.** `grpcdriver.MaxSnapshotBytes`
(64 MiB, `internal/adapter/grpcdriver/sessionstore.go`) is the protocol's
required minimum message capacity: media-carrying snapshots far exceed gRPC's
4 MiB default, so `Dial` raises the client send/recv limits and a conforming
driver mounts `grpc.MaxRecvMsgSize(MaxSnapshotBytes)` (pinned by the
storeconformance large-snapshot subtest, which FAILS over default limits).
Every store/source RPC is unary because every payload fits under that
ceiling — skill assets ride the same 64 MiB bound that `osfs` enforces on
Read (`maxReadBytes`), so streaming/chunking would buy nothing and would
complicate every driver implementation in every language. The lone exception
is `EventLogService.Read` (`contracts/proto/mecatl/driver/v1/event_log.proto`),
which is SERVER-STREAMING: a run's event log grows unbounded, so a unary Read
would eventually hit the 64 MiB cap, and a stream maps 1:1 onto
`port.EventLog`'s lazy `iter.Seq2` Read. Its events are individually small
(well under the 4 MiB default), so EventLog needs no raised message cap — the
stream frames each event as its own message. The first seam that genuinely
needs unary chunking is the workspace driver — which is exactly why it is a
sketch, not a service (below).

**Error mapping conventions** (the per-seam tables are in
IMPLEMENTATION-NOTES; these are the rules they instantiate):

- **Absence-as-sentinel:** a Load/body/asset miss is `NOT_FOUND` on the wire
  and the client wraps the port sentinel so `errors.Is` holds harness-side
  (`port.ErrSessionNotFound`, `tool.ErrSkillNotFound`,
  `tool.ErrSkillAssetNotFound`), with the id/name in the message.
- **Absence-as-data:** where the Go port models a miss as data, the wire does
  too — a memory Recall miss is `found=false`, NEVER `NOT_FOUND`; an unknown
  command is `NOT_FOUND` on the wire but the client maps it to the port's
  normal `("", false, nil)`. Forget of a missing key is OK (idempotent).
- **`INVALID_ARGUMENT` is pre-validated server-side:** blank keys/names and
  invalid logical asset names are rejected by the server wrapper BEFORE the
  backend sees them (the in-process store's own rejection stays
  conformance-tested independently); an invalid name never yields content.
- **ctx rewrap:** after a failed RPC, if the caller's ctx is done the client
  returns a wrap of `ctx.Err()` so
  `errors.Is(_, context.Canceled/DeadlineExceeded)` holds harness-side.
- **No transient/permanent classification.** Everything else is
  `"grpcdriver: <op>: %w"`. Deadline passthrough only — no retries, no
  default deadline, lazy `grpc.NewClient` (fail-fast, no WaitForReady). If
  drivers ever need retries/breakers, the house answer is a
  `driverresilience` DECORATOR over the clients (the `llmresilience`
  precedent), never knobs on the clients or fields on the ports.

## Trust & security posture

**Trust is enforced at source CONSTRUCTION, in composition.** Origin tiers
(`tool.SkillOrigin`/`tool.AgentOrigin`: `explicit|project|user|driver`) are
CLOSED label sets for observability — a tier label, NEVER a location; no
implementation may put a path, directory, or URL in one. The actual gate runs
where sources are constructed: an untrusted workspace's project tier is never
built (`ResolveSources` never constructs the project `DirSource`s), so nothing
downstream needs to re-check. A driver sits at the **operator-infrastructure
tier**: the same trust class as the on-disk store directory / an explicit
`--skills-dir`. Clients stamp `Origin*Driver` UNCONDITIONALLY — the wire's
claimed origin is driver-side observability only.

**Non-local cleartext is refused for ALL driver dials**, token or not
(`internal/adapter/grpcdriver/dial.go`): pre-dial, any non-loopback/non-unix
target without TLS is rejected, because a driver delivers session payloads,
memories, model-steering skill bodies, and 0o755-materialized executables —
an on-path attacker over cleartext would gain driver-equivalent capability
regardless of auth. Bearer auth (`RequireTransportSecurity()=!loopback`), CA
pinning, and mTLS are dial options; equal URLs share ONE lazy `ClientConn`
(the build-scoped `driverConns` cache in `internal/app`).

**Secret-shaped fields cross the wire but never leak downstream.**
`AgentMCPServer.Headers` (e.g. `Authorization`) crosses the port and the wire
— dropping it would regress vs file defs, which carry plaintext auth headers
on disk today — but it is never logged or projected anywhere
(`defMCPTools` logs names/urls/counts only; `AgentInfo` carries no
mcpServers; guarded by `TestDefMCPHeadersNeverLogged`). It rides the wire
only because of the cleartext refusal above.

**The hooks warning.** A def's `hooks:` map executes through `hookexec` as
UNGATED shell on the harness host — strictly stronger capability than the
skill driver, whose payloads still ride the permission-gated Bash path. **A
compromised agent-source driver executes arbitrary shell on the harness host
via def hooks; treat it as harness-equivalent infrastructure.** The build
narrates every driver def carrying hooks (names only, never hook values).

**Clients normalize defensively; a driver is never trusted to sanitize.**
Every list-shaped client drops blank/grammar-invalid names, de-dups
first-wins, sorts, single-line-normalizes and re-caps descriptions/bodies to
the exported canonical caps, re-normalizes hooks/headers via the same helpers
the frontmatter parser uses, and bounds counts (1024 defs; per-def 32 hooks,
256 tools/disallowed/skills, 64 MCP servers — an over-cap def is dropped with
a WARN naming it, fail-soft per def). The soul client re-runs the FULL local
body discipline (`soul.ValidateBody`: byte cap, injection scan, data-fence
integrity). Model-written memory values are sanitized in the harness's memory
tools, never delegated to the store.

**Logical asset names have ONE grammar and ONE validator.** A skill payload
is addressed by a slash-separated, RELATIVE logical name in the skill's own
namespace (`references/api.md`, `scripts/run.sh` <!-- lint:not-a-citation: skill logical asset names, not repo files --> — the exact namespace
SKILL.md bodies already reference): non-empty, no empty/`.`/`..` segments, no
backslash, no NUL. `tool.ValidSkillAssetName` is the single shared validator
— every implementation and consumer (FS source, driver client, server
wrapper, materializer) calls it; nobody re-derives the grammar.

**Materializer caps and latch semantics**
(`internal/adapter/skills/assetcache.go`). <!-- lint:not-a-citation: frozen ADR historical path removed by ADR 0108 --> Driver skill payloads materialize
lazily into a build-scoped temp cache on FIRST activation (a never-activated
skill transfers zero bytes): per-asset 16 MiB, per-bundle 64 MiB, enforced on
the ACTUAL bytes; name validation + post-Clean containment; executable →
0o755 else 0o644. SUCCESS latches permanently, and a DETERMINISTIC rejection
(caps, invalid names, containment — retrying the identical bundle cannot
succeed) latches too; a transport/ctx fault stays UNLATCHED so the next
activation retries. A failed bundle fails the activation with a
model-addressable tool error — never a partial bundle on disk.

## Deliberately deferred (with the recorded rationale)

1. **Server-wrapper promotion.** `grpcdriver`'s `NewSessionStoreServer` /
   `NewMemoryStoreServer` / `New*SourceServer` wrap an in-process backend as
   the generated server interfaces — a Go driver process is the backend plus
   these wrappers plus a `grpc.Server`. They exist `internal/` because the
   bufconn conformance fixtures need them; **promoting them (and the
   clients) to an importable package for external Go driver authors is a
   public-API commitment** — every exported symbol becomes a compatibility
   surface — and is made deliberately when an external Go consumer exists,
   not implied by the current placement (`internal/adapter/grpcdriver/server.go`
   says the same in code). Non-Go drivers are unaffected: the proto is the contract.
2. **User-model store driver flag.** `--memory-store-url` drives the
   project-memory store only; the user-model store stays on the local flock
   adapter. Same interface (`tool.MemoryStore`), so the flag is mechanical
   when wanted — deferred until a consumer asks, per the no-speculative-
   widening rule.
3. **Live-reload/watch seams.** ListSkills/ListAgentDefs are
   snapshot-stable for the source's life, deliberately: the build-once
   trust-gate-completeness invariant (read roots opened at workspace
   construction, per-def engines baked at build) depends on resolve-once. A
   watch seam is not "add an RPC" — it reopens trust gating, catalog
   identity, and engine lifecycle, so it gets designed on purpose or not at
   all.
4. **memfs/virtual-overlay for skill assets — REJECTED** on a hard fact:
   Bash executes real OS processes, and a virtual file cannot be executed.
   Driver assets must materialize to real disk; FS skills serve in place
   (zero copy). The rejected alternatives (a dedicated asset tool; a pure
   overlay) would have orphaned every SKILL.md's relative-Read and script
   instructions — a model-facing regression for zero interface gain.
5. **Harness downgrade through a shared driver sheds additive fields.** If
   an OLDER harness Loads a snapshot Saved by a newer one (same
   `sessnap-json/1` tag), unknown additive JSON fields are silently dropped
   on decode — identical to sharing a `--store-dir` across versions. Accepted
   as-is; the format tag protects against encoding replacement, not field
   additions.
6. **The wrappers enforce no auth themselves.** They are pure translation; a
   driver process is responsible for its own server-side auth (interceptors,
   mTLS) just as `mecated` composes its own. The harness-side posture
   (bearer + TLS + cleartext refusal) is entirely client-side.
7. **Cross-process GC liveness.** The child-session retention sweep's
   liveness predicate (`Service.IsLive`) sees only THIS process's in-flight
   runs. Several harnesses sharing one remote session-store driver could in
   principle sweep a sibling's live child — in practice protected by the
   age/oldest-first ordering (a live child's snapshot is freshly saved) and
   by every delete being idempotent and best-effort (the child's next save
   recreates it). True cross-process exclusion is a LEASING concern, the
   recorded Tier-2 follow-up, not a List/Delete protocol widening.

## The workspace/FS driver — sketch only (the one unimplemented seam)

`tool.Workspace`/`tool.FileSystem` (`engine/tool/tool.go`) are already ports,
so the seam exists; a driver service does not, deliberately. It is heavier
than every shipped seam in three ways:

- **It breaks the 64 MiB unary rule.** Workspace files, Glob results over big
  trees, and Grep matches have no protocol-level ceiling the way snapshots
  and skill bundles do — a faithful remote workspace needs server-streamed,
  chunked reads (and capped/paged Glob/Grep), the exact complexity every
  shipped service avoided.
- **Bash needs real exec on real files.** The Bash tool runs OS processes
  against the workspace root (`tool.CommandRunner`); a remote FS cannot host
  that. So a remote workspace driver can serve the READ side only
  (Read/Stat/Glob/Grep — the `tool.WorkspaceReader` subset, plus search), or
  must pair with a materialization step that syncs a working set to local
  disk before mutate/exec — the skill asset cache is the small-scale
  precedent (caps, containment, latching).
- **Part of the Workspace surface is harness state, not storage.** The Edit
  read-ledger (`RecordRead`/`WasReadUnchanged`) is per-session, in-harness
  bookkeeping; only the content `version` fingerprint would come from the
  driver (a content hash — mtime is not trustworthy cross-process).

The one-page shape, if/when triggered (same package, same conventions —
unary except the chunked read, `NOT_FOUND` → sentinel, server-side caps):

```proto
// mecatl.driver.v1 — SKETCH, not shipped.
service WorkspaceService {
  // Read side (tool.WorkspaceReader + search). Paths are workspace-relative;
  // the server rejects escapes — same contract osfs enforces locally.
  rpc Stat(StatRequest) returns (StatResponse);            // size, mode, content_hash
  rpc ReadFile(ReadFileRequest) returns (stream FileChunk); // chunked; offset+limit resume
  rpc Glob(GlobRequest) returns (GlobResponse);            // capped + page token
  rpc Grep(GrepRequest) returns (GrepResponse);            // capped/shaped server-side

  // Mutate side — only meaningful with local materialization for exec;
  // a v1 could omit it and serve read-only remote context.
  rpc WriteFile(stream FileChunk) returns (WriteFileResponse);
}
```

**Trigger condition:** a real consumer that separates the harness process
from where the code lives (remote dev box, ephemeral runner) AND is willing
to take the read-only-or-materialize split for Bash. Until then the sketch is
the deliverable — implementing it speculatively would mean designing
streaming, paging, and a sync protocol with no consumer to validate against.


---

*Part of the [design docs](../design/README.md). Related: [mecatl — Architecture](0004-v1-architecture.md), [Implementation Notes](../design/IMPLEMENTATION-NOTES.md).*

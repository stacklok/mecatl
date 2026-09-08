# TypeScript SDK local daemon and callback tools (M3) — acceptance plan

**Phase:** capability — `@stacklok/mecatl-sdk` M3: the spawned local daemon, `query()`, and callback tools
**Status:** landed, 2026-09-04. Delivery PRs: [#1078](https://github.com/stacklok/mecatl/pull/1078), [#1081](https://github.com/stacklok/mecatl/pull/1081), [#1084](https://github.com/stacklok/mecatl/pull/1084), [#1089](https://github.com/stacklok/mecatl/pull/1089), [#1091](https://github.com/stacklok/mecatl/pull/1091), [#1093](https://github.com/stacklok/mecatl/pull/1093), [#1095](https://github.com/stacklok/mecatl/pull/1095), [#1097](https://github.com/stacklok/mecatl/pull/1097), [#1099](https://github.com/stacklok/mecatl/pull/1099), [#1122](https://github.com/stacklok/mecatl/pull/1122).
**Issue:** [stacklok/mecatl#821](https://github.com/stacklok/mecatl/issues/821) (parent: [#761](https://github.com/stacklok/mecatl/issues/761)).
**ADR:** [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) — binary resolution, the SDK-owned argv and its one tool-capable topology, the ready-file barrier, the lifetime pipe, disposal ownership, `query()`'s plan refusal, the immutable tool set and its two collision layers, the hand-written loopback MCP host, the bounded execution contract, the diagnostics sink, and the four new local error codes.
**Delivery shape:** a **linear stack**, one PR per scenario — `sdk/31-spawn` is the stack root off `main`; subsequent layers are `sdk/32-hosting`, `sdk/33-startup-failure`, `sdk/34-disposal`, `sdk/35-query`, `sdk/36-tool`, `sdk/37-tool-host`, `sdk/38-tool-refusal`, `sdk/39-parity`, `sdk/40-e2e`. This is **not** an accumulator: each PR targets its predecessor and is reviewed and merged on its own, exactly as M2's `sdk/21`…`sdk/30` stack was.

The smallest set of work that lets a TypeScript program start its own mecatl
daemon, run one prompt against it, expose its own functions to the model as
tools, and tear the whole thing down without leaking a process, a socket, a
port, or a credential. **The server half already shipped**: `--grpc-unix-socket`,
`--ready-file`, `--lifetime-pipe-fd`, the HTTP-disable semantics, and
`CreateSessionRequest.mcp_servers` with its listener-scoped
`mcp_servers_on_create` feature are all on `main`, recorded in
[`sdk-server-enablers.md`](sdk-server-enablers.md) Scenarios 8 and 9. **This
plan is client-side only.**

The doc is organized scenario-first because acceptance is about what the
running harness (here: the SDK against the running harness) can demonstrate,
not which modules exist on disk.

## Why these scope cuts

- **Nothing edits `contracts/proto/` or production `internal/adapter/server/` /
  `cmd/mecated/`.** The only Go code this plan adds is six parity *tests* in
  the root module beside the existing
  `internal/adapter/server/sdk_typescript_*_test.go` guards, which read
  TypeScript sources and compare them to the server's own constants. Reading
  server sources is fine; the no-touch constraint is about edits. The audit
  behind [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md)'s
  Context found **no server gap** for `spawn()`: `readyDoc` already carries the
  pid, transport, socket path, API major and feature set the client needs, and
  the one topology `spawn()` uses is the one topology `mcp_servers` is
  permitted on.
- **The argv is decided once, in the ADR, not per worker.**
  `clientMCPOnCreateForListeners` in
  [`cmd/mecated/main.go`](../../cmd/mecated/main.go) permits `mcp_servers` only
  for `grpcUnixSocket != "" && httpAddr == ""`. A worker who added an HTTP
  listener "for debuggability" would silently remove `tool()` from the product,
  and the failure would surface three scenarios later as an unexplained
  `client_mcp_unsupported`.
  [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 2
  fixes the argv, the refusal of caller overrides, and the rule that feature
  support is read from the ready file rather than inferred from the flags we
  passed.
- **Namespace collision is checked in two places because only two are
  checkable.** [`mcp.Register`](../../internal/adapter/mcp/mcp.go) is
  skip-and-continue and first-wins, `verifyClientMCPMounted` checks *server*
  names rather than tool names, `CreateSessionResponse` reports no tool
  inventory, and there is no `ListTools` RPC — so a client server named like an
  operator's server-global one mounts cleanly and has its tools silently
  shadowed. Scenario 6 therefore validates locally **and** pre-flights
  `ListMcpSources`. Without the pre-flight, #821's "fail loudly on namespace
  collision" would be a promise the client cannot keep.
- **The MCP host is hand-written, and that is a footprint decision with a
  named cost.** The Go client sends `Accept: application/json,
  text/event-stream`, accepts a plain `application/json` POST response, and
  treats `405` on the standalone GET as a clean no-op, so six methods over
  `node:http` suffice — `server/discover` included, since the pinned client
  probes it before falling back to `initialize`. Importing `@modelcontextprotocol/sdk` would pull a
  server-side dependency tree into `./node` for that, against
  [ADR-0279](../adr/0279-typescript-sdk-architecture.md)'s stated discipline.
  The cost — hand-maintained protocol compatibility — is paid down by Scenario
  10, which proves it against the real Go client on the real wire rather than
  against a fake.
- **`query()` refuses plan mode rather than shipping a callback that cannot
  fire.** #821 asks for `onPlanApproval` as a precondition, but `ApprovePlan`
  streams a resumed run *and* a continuation run with a different id, which is
  why #821 itself scopes `session.resolvePlan()` / `PlanResolution` to M4 and
  why [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) listed it out
  of scope. This is the one place the plan knowingly diverges from the issue's
  wording; see *Deferred decisions*.
- **Handler failure is deliberately asymmetric.** A thrown exception is generic
  to the model and full only to diagnostics; an explicit `isError` result passes
  through verbatim. Collapsing the two would either leak a user's stack trace
  into a context a prompt-injected page can influence, or remove the only way to
  tell the model something useful went wrong.
- **Verify names follow M1/M2's convention.** Go proofs are
  `TestSDKTypescriptLocal_ScenarioN_*`; TypeScript proofs use the strict
  `vitest:<path>#<base64url-title>` resolver form throughout, so a renamed or
  deleted test title fails `task ac-trace-strict` instead of degrading to a
  file-level proof.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Windows local `spawn()` / named pipes | post-v0.1 | [#821](https://github.com/stacklok/mecatl/issues/821) non-goals — v0.1 Windows is remote `connect()` only |
| `mecated` binary auto-download | never (stated non-goal) | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 1 |
| Remote tool hosting / reverse callback channel over `connect()` | post-v0.1 | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 14 |
| Dynamic tool add/remove after session creation | post-v0.1 | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 9 — the server mounts a per-session engine once |
| Any stdio MCP transport | never | [`AGENTS.md`](../../AGENTS.md) — "No stdio MCP, ever" |
| `session.resolvePlan()` / `PlanResolution` / `onPlanApproval` | M4 plan | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 8 |
| A server-side mounted-tool inventory that would make collision detection total | a later server plan | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Consequences |
| Full HarnessService/ScheduleService RPC coverage; descriptor-to-transport parity gate | M4 plan | [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 1 |
| Browser/Playwright matrix, TS 5.7 declaration matrix, macOS CI smoke | M4 plan | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4 |
| npm publish workflow, `sdk/typescript/vX.Y.Z` tags, SDK `user-docs/` pages | M4 plan | [#821](https://github.com/stacklok/mecatl/issues/821) M4 item 4 |
| Attached `approve`/`resolveAsk`/`steer` | M2 residuals → server work + [#873](https://github.com/stacklok/mecatl/issues/873) | [ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) Decision 6 |
| Downstream-app transport-agnostic e2e mocker | [#872](https://github.com/stacklok/mecatl/issues/872) | [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 3 |
| More than one spawned daemon per `Client` | post-v0.1 | [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 14 |

## In scope — 10 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable;
later scenarios assume earlier ones but do not change their acceptance
criteria. Within each, ACs progress happy path → richer happy path → edges →
cross-cutting. TypeScript proofs are cited with the strict `vitest:` resolver
token followed by the human-readable `<file> :: "<title>"`; titles are the
contract.

---

### Scenario 1 — Binary resolution, the spawn argv, and the ready-file barrier

`spawn()` turns "start me a daemon" into a resolved executable, a fixed
argument vector, a private runtime directory, and a `Client` that is only
returned once the daemon has published a ready file the SDK understands
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decisions 1–4).
Resolution is ordered and never fetches anything; the argv is SDK-owned because
the topology it encodes is what makes Scenario 6 possible at all.

**Work:** a `./node`-only `spawn()` over an **internal launcher seam** (the shape
M2 used for its reconnect scheduler: an injectable function the unit suite
substitutes, never public API) so argv, resolution order and readiness
classification are assertable without a real daemon; a ready-file poller; the
private `0700` runtime directory with its socket-path length bound.

**Acceptance:**
- AC1.1: `spawn({ binaryPath })` launches exactly that executable, and neither
  `MECATED_BIN` nor `PATH` is consulted once an explicit path is supplied.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#YW4gZXhwbGljaXQgYmluYXJ5UGF0aCB3aW5zIG92ZXIgTUVDQVRFRF9CSU4gYW5kIFBBVEg — `sdk/typescript/test/spawn.test.ts :: "an explicit binaryPath wins over MECATED_BIN and PATH"`
- AC1.2: With no `binaryPath`, `MECATED_BIN` is used when set, and a `PATH`
  lookup for `mecated` is used otherwise; resolution is performed by the SDK
  against `process.env.PATH` with no shell involved, so a `PATH` entry
  containing shell metacharacters is treated as a directory name and nothing
  is interpreted.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#cmVzb2x1dGlvbiBmYWxscyBiYWNrIE1FQ0FURURfQklOIHRoZW4gUEFUSCB3aXRob3V0IGludm9raW5nIGEgc2hlbGw — `sdk/typescript/test/spawn.test.ts :: "resolution falls back MECATED_BIN then PATH without invoking a shell"`
- AC1.3: A candidate that does not exist, is not a file, or is not executable
  fails with a typed `spawn_failed` naming which of the three sources supplied
  it, and no child process is created.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#YW4gdW5yZXNvbHZhYmxlIGJpbmFyeSBmYWlscyB0eXBlZCBiZWZvcmUgYW55IHByb2Nlc3MgaXMgY3JlYXRlZA — `sdk/typescript/test/spawn.test.ts :: "an unresolvable binary fails typed before any process is created"`
- AC1.4: The argv always contains `serve`, `--grpc-unix-socket`, `--http-addr`
  with an empty value, `--ready-file`, and `--lifetime-pipe-fd`, with the socket
  and ready paths inside the client's own runtime directory.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#dGhlIGRlZmF1bHQgYXJndiBpcyB0aGUgdG9vbC1jYXBhYmxlIFVEUyB0b3BvbG9neQ — `sdk/typescript/test/spawn.test.ts :: "the default argv is the tool-capable UDS topology"`
- AC1.5: A caller-supplied extra argument that names a flag the SDK already set
  is refused with a typed error naming the flag, rather than appended and left
  to last-flag-wins.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#YW4gZXh0cmEgYXJndW1lbnQgY29sbGlkaW5nIHdpdGggYW4gU0RLLW93bmVkIGZsYWcgaXMgcmVmdXNlZA — `sdk/typescript/test/spawn.test.ts :: "an extra argument colliding with an SDK-owned flag is refused"`
- AC1.6: `spawn()` resolves only after a ready file whose `schema` is
  `mecated-ready/1` is read in full, and the returned client's transport dials
  the `socket_path` from that document rather than a path the SDK assumed.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#c3Bhd24gcmVzb2x2ZXMgb24gdGhlIHJlYWR5IGRvY3VtZW50IGFuZCBkaWFscyBpdHMgc29ja2V0IHBhdGg — `sdk/typescript/test/spawn.test.ts :: "spawn resolves on the ready document and dials its socket path"`
- AC1.7: A ready file whose `schema` is any other value fails typed instead of
  being read best-effort, so a future document version cannot be
  half-interpreted.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#YW4gdW5rbm93biByZWFkeS1maWxlIHNjaGVtYSBmYWlscyByYXRoZXIgdGhhbiBkZWdyYWRpbmc — `sdk/typescript/test/spawn.test.ts :: "an unknown ready-file schema fails rather than degrading"`
- AC1.8: The runtime directory is created by `fs.mkdtemp` — atomically, with an
  unguessable suffix, never at a predictable path and never with
  `recursive: true`, so a pre-existing directory or symlink at that path is a
  typed failure rather than something adopted — is mode `0700`, contains the
  socket and ready file, and is removed on disposal without following symlinks.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#dGhlIHJ1bnRpbWUgZGlyZWN0b3J5IGlzIHByaXZhdGUgYW5kIHJlbW92ZWQgb24gZGlzcG9zYWw — `sdk/typescript/test/spawn.test.ts :: "the runtime directory is private and removed on disposal"`
- AC1.9: A socket path whose UTF-8 byte length would reach the Darwin
  `sun_path` bound is rejected or relocated to a shorter base **before** the
  daemon is launched, so the failure is never an opaque `bind(2)` `EINVAL`; the
  shorter base is created under the same `mkdtemp` rule as AC1.8, so it is
  uniquely named and two concurrent `spawn()`s cannot collide on one socket path
  and reach `reclaimStaleSocket`'s documented probe-then-unlink window.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#YW4gb3Zlci1sb25nIHNvY2tldCBwYXRoIGlzIGJvdW5kZWQgYmVmb3JlIGxhdW5jaA — `sdk/typescript/test/spawn.test.ts :: "an over-long socket path is bounded before launch"`
- AC1.10: On a platform reporting `win32`, `spawn()` fails with a typed
  `unsupported_platform` and performs no resolution, no directory creation and
  no launch.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#d2luMzIgaXMgYSB0eXBlZCB1bnN1cHBvcnRlZCBwbGF0Zm9ybSBiZWZvcmUgYW55IHdvcms — `sdk/typescript/test/spawn.test.ts :: "win32 is a typed unsupported platform before any work"`
- AC1.11: The SDK-owned argv contains no posture, trust, or permission flag —
  no `--posture`, `--yolo`, or `--trust-project` — so `spawn()` never alters
  the daemon's default permission posture on the operator's behalf.
  - verify: vitest:sdk/typescript/test/spawn.test.ts#dGhlIHNkay1vd25lZCBhcmd2IG5ldmVyIGxvb3NlbnMgdGhlIGRhZW1vbiBwb3N0dXJl — `sdk/typescript/test/spawn.test.ts :: "the sdk-owned argv never loosens the daemon posture"`
- AC1.12: The default `spawn()` argv starts a **real** same-checkout `mecated`
  to readiness. This lands with Scenario 1 rather than Scenario 10 because
  every flag in that argv is a claim about a daemon nine later PRs build on —
  `--lifetime-pipe-fd` in particular, whose descriptor-type contract no
  seam-level test can check.
  - verify: vitest:sdk/typescript/e2e/spawn-smoke.e2e.test.ts#dGhlIGRlZmF1bHQgc3Bhd24gYXJndiBzdGFydHMgYSByZWFsIG1lY2F0ZWQgdG8gcmVhZGluZXNz — `sdk/typescript/e2e/spawn-smoke.e2e.test.ts :: "the default spawn argv starts a real mecated to readiness"`

---

### Scenario 2 — The spawned daemon's shape: UDS-only, HTTP and metrics off, the lifetime pipe

The daemon `spawn()` starts must open no TCP port, must advertise
`mcp_servers_on_create`, and must die when its parent does
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decisions 2
and 5; [`sdk-server-enablers.md`](sdk-server-enablers.md) Scenario 8).

**Work:** the `http: true` option and its honest consequence; the fourth
`stdio` entry and the parent-held pipe end; a `daemon` accessor exposing the
ready document's non-secret facts (pid, transport, socket path, api major,
features) to the caller.

**Acceptance:**
- AC2.1: A default `spawn()` produces a ready document with `transport: "unix"`,
  a non-empty `socket_path`, and **no** `http_address` key at all — absence,
  not an empty string.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#dGhlIGRlZmF1bHQgZGFlbW9uIGlzIHVuaXggdHJhbnNwb3J0IHdpdGggbm8gaHR0cCBhZGRyZXNz — `sdk/typescript/test/spawn-hosting.test.ts :: "the default daemon is unix transport with no http address"`
- AC2.2: The default daemon's advertised `features` include
  `mcp_servers_on_create`, and the SDK reports tool support from that array
  rather than from the flags it passed.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#dG9vbCBjYXBhYmlsaXR5IGlzIHJlYWQgZnJvbSB0aGUgcmVhZHktZmlsZSBmZWF0dXJlcw — `sdk/typescript/test/spawn-hosting.test.ts :: "tool capability is read from the ready-file features"`
- AC2.3: `spawn({ http: true })` produces a ready document carrying a loopback
  `http_address`, and the client reports tool support as unavailable because
  the daemon no longer advertises the feature.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#ZW5hYmxpbmcgaHR0cCBjb3N0cyB0aGUgbWNwX3NlcnZlcnNfb25fY3JlYXRlIGZlYXR1cmU — `sdk/typescript/test/spawn-hosting.test.ts :: "enabling http costs the mcp_servers_on_create feature"`
- AC2.4: When the lifetime pipe is enabled, the descriptor the child inherits at
  fd 3 is the **connected local stream socket** that Node's `stdio: [..., "pipe"]`
  fourth entry yields. The server-enabler pre-PR widened `checkLifetimePipeFD` to
  admit that shape alongside a FIFO and to reject every other descriptor type, so
  no `mkfifo(1)` dance is needed. The parent holds its end, never writes to it, and
  `--lifetime-pipe-fd 3` is present in the argv.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#dGhlIGxpZmV0aW1lIHBpcGUgaXMgaW5oZXJpdGVkIGF0IGZkIDMgYW5kIG5ldmVyIHdyaXR0ZW4 — `sdk/typescript/test/spawn-hosting.test.ts :: "the lifetime pipe is inherited at fd 3 and never written"`
- AC2.5: The child watches its inherited endpoint only for reads, so it is not
  itself a writer holding the channel open, and when the parent's end is
  released the child observes EOF — the signal the daemon turns into a graceful
  stop.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#cmVsZWFzaW5nIHRoZSBwYXJlbnQgZW5kIGRlbGl2ZXJzIEVPRiB0byB0aGUgY2hpbGQ — `sdk/typescript/test/spawn-hosting.test.ts :: "releasing the parent end delivers EOF to the child"`
- AC2.6: `spawn({ lifetimePipe: false })` omits `--lifetime-pipe-fd` and the
  fourth `stdio` entry entirely; the client still stops the daemon on `close()`.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#b3B0aW5nIG91dCBvZiB0aGUgbGlmZXRpbWUgcGlwZSBzdGlsbCBzdG9wcyB0aGUgZGFlbW9uIG9uIGNsb3Nl — `sdk/typescript/test/spawn-hosting.test.ts :: "opting out of the lifetime pipe still stops the daemon on close"`
- AC2.7: Neither the ready document the SDK exposes nor any value the SDK
  derives from it contains a credential-shaped field; the exposed surface is
  exactly the non-secret allowlist the daemon publishes.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#dGhlIGV4cG9zZWQgZGFlbW9uIGZhY3RzIGFyZSB0aGUgbm9uLXNlY3JldCBhbGxvd2xpc3Q — `sdk/typescript/test/spawn-hosting.test.ts :: "the exposed daemon facts are the non-secret allowlist"`
- AC2.8: The child's environment is the parent's environment merged with the
  caller's `env` overrides, and the SDK never enumerates that environment into
  a diagnostic, an error, or the exposed daemon facts.
  - verify: vitest:sdk/typescript/test/spawn-hosting.test.ts#ZW52aXJvbm1lbnQgaXMgaW5oZXJpdGVkIGFuZCBuZXZlciBlbnVtZXJhdGVkIG91dHdhcmQ — `sdk/typescript/test/spawn-hosting.test.ts :: "environment is inherited and never enumerated outward"`

---

### Scenario 3 — Startup failure: classification, the redacted stderr tail, and the diagnostics sink

A daemon that fails to start must say why, without becoming a channel for
whatever secret its stderr happened to mention
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decisions 3
and 12).

**Work:** the bounded stderr capture and its redaction; the `spawn_failed` /
`readiness_timeout` split; the client-level `diagnostics` sink, its record
shape, and its first emitter.

**Acceptance:**
- AC3.1: A child that exits before publishing a ready file fails with typed
  `spawn_failed` carrying the exit code and signal.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#YSBjaGlsZCBleGl0aW5nIGJlZm9yZSByZWFkaW5lc3MgaXMgc3Bhd25fZmFpbGVkIHdpdGggaXRzIGV4aXQgc3RhdHVz — `sdk/typescript/test/spawn-failure.test.ts :: "a child exiting before readiness is spawn_failed with its exit status"`
- AC3.2: A child that starts but never publishes a ready file fails with typed
  `readiness_timeout` after the configured deadline, and the child is stopped
  rather than left running.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#YSByZWFkaW5lc3MgdGltZW91dCBpcyB0eXBlZCBhbmQgbGVhdmVzIG5vIG9ycGhhbiBjaGlsZA — `sdk/typescript/test/spawn-failure.test.ts :: "a readiness timeout is typed and leaves no orphan child"`
- AC3.3: Both failures carry a stderr tail bounded to a fixed byte budget, taken
  from the end of the stream regardless of how much the child wrote, and the
  reported tail begins at the first **complete line boundary** — a leading
  partial line is dropped rather than reported, so no line ever reaches the
  caller truncated mid-value.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#dGhlIHN0ZGVyciB0YWlsIGlzIGJvdW5kZWQgZnJvbSB0aGUgZW5kIG9mIHRoZSBzdHJlYW0 — `sdk/typescript/test/spawn-failure.test.ts :: "the stderr tail is bounded from the end of the stream"`
- AC3.4: A stderr line matching the SDK's secret-shaped pattern is replaced as a
  **whole line** in the reported tail, never partially masked, and the raw value
  appears in neither the error message nor any diagnostic record. Redaction runs
  over the tail **actually reported**, after truncation, and the pattern matches
  both `NAME=value` shapes and known credential prefixes (`sk-`, `ghp_`,
  `xox[abps]-`, a `eyJ` JWT head).
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#YSBzZWNyZXQtc2hhcGVkIHN0ZGVyciBsaW5lIGlzIHJlZGFjdGVkIHdob2xlc2FsZQ — `sdk/typescript/test/spawn-failure.test.ts :: "a secret-shaped stderr line is redacted wholesale"`
- AC3.5: After any startup failure — including one that lands **after** a valid
  ready file, such as an unknown schema, a failed first dial, or the Scenario 6
  pre-flight refusal — the runtime directory is removed and no socket file is
  left behind.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#YSBmYWlsZWQgc3Bhd24gbGVhdmVzIG5vIHJ1bnRpbWUgZGlyZWN0b3J5IG9yIHNvY2tldA — `sdk/typescript/test/spawn-failure.test.ts :: "a failed spawn leaves no runtime directory or socket"`
- AC3.6: A failed `spawn()` opens no TCP listener at any point — asserted over
  the argv and the ready document rather than by scanning the host.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#YSBmYWlsZWQgc3Bhd24gbmV2ZXIgcmVxdWVzdHMgYSB0Y3AgbGlzdGVuZXI — `sdk/typescript/test/spawn-failure.test.ts :: "a failed spawn never requests a tcp listener"`
- AC3.7: With no `diagnostics` sink installed, a startup failure writes nothing
  to `console`; the error alone carries the report.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#bm8gc2luayBtZWFucyBub3RoaW5nIGlzIHdyaXR0ZW4gdG8gY29uc29sZQ — `sdk/typescript/test/spawn-failure.test.ts :: "no sink means nothing is written to console"`
- AC3.8: With a sink installed, the failure emits one structured record with a
  stable `code`, a level, a message and typed fields, and that record is not a
  `session.Event` of any kind.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#dGhlIGRpYWdub3N0aWNzIHNpbmsgcmVjZWl2ZXMgb25lIHN0cnVjdHVyZWQgc3RhcnR1cCByZWNvcmQ — `sdk/typescript/test/spawn-failure.test.ts :: "the diagnostics sink receives one structured startup record"`
- AC3.9: Every terminal failure that lands **after** the child was launched
  stops that child — `SIGTERM`, a bounded grace, then `SIGKILL` — **and then**
  removes the runtime directory, in that order, so no daemon holding the
  operator's real credentials survives on a live socket and no socket is
  unlinked out from under a still-serving process.
  - verify: vitest:sdk/typescript/test/spawn-failure.test.ts#YSBwb3N0LWxhdW5jaCBmYWlsdXJlIHN0b3BzIHRoZSBjaGlsZCBiZWZvcmUgcmVtb3ZpbmcgaXRzIGRpcmVjdG9yeQ — `sdk/typescript/test/spawn-failure.test.ts :: "a post-launch failure stops the child before removing its directory"`

---

### Scenario 4 — Ownership and disposal: spawned versus connected, graceful then escalated

`close()` must be total for the things this client created and inert for the
things it did not
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 6),
extending the detach-never-cancels rule
[ADR-0288](../adr/0288-typescript-sdk-durable-attachment.md) established for
attachments.

**Work:** the ordered teardown, the `SIGTERM` → grace → `SIGKILL` escalation,
idempotence, and the failure-tolerant reporting path.

**Acceptance:**
- AC4.1: `close()` on a spawned client cancels the runs that client owns,
  releases its attachments and activity streams, stops the status monitor,
  closes the transport, and signals the daemon — in that order, with the tool
  host torn down before the transport.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#Y2xvc2UgdGVhcnMgZG93biBvd25lZCByZXNvdXJjZXMgaW4gYSBmaXhlZCBvcmRlcg — `sdk/typescript/test/client-disposal.test.ts :: "close tears down owned resources in a fixed order"`
- AC4.2: `close()` on a `connect()`ed client sends no signal to any process and
  removes no directory; only client-side resources are released.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#Y2xvc2luZyBhIGNvbm5lY3RlZCBjbGllbnQgc2lnbmFscyBubyBwcm9jZXNz — `sdk/typescript/test/client-disposal.test.ts :: "closing a connected client signals no process"`
- AC4.3: A spawned daemon that exits on `SIGTERM` within the grace window is
  never sent `SIGKILL`.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#YSBkYWVtb24gdGhhdCBzdG9wcyBvbiBTSUdURVJNIGlzIG5ldmVyIGVzY2FsYXRlZA — `sdk/typescript/test/client-disposal.test.ts :: "a daemon that stops on SIGTERM is never escalated"`
- AC4.4: A spawned daemon still running after the grace window is sent
  `SIGKILL`, and `close()` resolves rather than hanging.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#YW4gdW5yZXNwb25zaXZlIGRhZW1vbiBpcyBlc2NhbGF0ZWQgdG8gU0lHS0lMTCBhbmQgY2xvc2UgcmVzb2x2ZXM — `sdk/typescript/test/client-disposal.test.ts :: "an unresponsive daemon is escalated to SIGKILL and close resolves"`
- AC4.5: `await using` / `Symbol.asyncDispose` performs exactly the same
  teardown as `close()`, and calling both performs it once.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#YXN5bmNEaXNwb3NlIGFuZCBjbG9zZSBhcmUgdGhlIHNhbWUgaWRlbXBvdGVudCB0ZWFyZG93bg — `sdk/typescript/test/client-disposal.test.ts :: "asyncDispose and close are the same idempotent teardown"`
- AC4.6: A failure in one teardown step does not abort the rest and does not
  throw out of `close()`; each failure reaches the diagnostics sink instead.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#YSB0ZWFyZG93biBmYWlsdXJlIGlzIHJlcG9ydGVkIGFuZCBuZXZlciBhYm9ydHMgZGlzcG9zYWw — `sdk/typescript/test/client-disposal.test.ts :: "a teardown failure is reported and never aborts disposal"`
- AC4.7: After `close()`, every operation on the client and its sessions fails
  with a typed `invalid_state` rather than a transport error from a dead socket.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#YSBjbG9zZWQgY2xpZW50IHJlZnVzZXMgb3BlcmF0aW9ucyB3aXRoIGludmFsaWRfc3RhdGU — `sdk/typescript/test/client-disposal.test.ts :: "a closed client refuses operations with invalid_state"`
- AC4.8: Disposal aborts in-flight tool handlers through the `AbortSignal` they
  were given, and waits for the host to stop accepting before the transport
  closes.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#ZGlzcG9zYWwgYWJvcnRzIGluLWZsaWdodCBoYW5kbGVycyBiZWZvcmUgY2xvc2luZyB0aGUgdHJhbnNwb3J0 — `sdk/typescript/test/client-disposal.test.ts :: "disposal aborts in-flight handlers before closing the transport"`
- AC4.9: A spawned daemon that exits **without** a `close()` puts the client
  into a typed terminal state, emits one diagnostic record, and makes every
  subsequent operation fail typed rather than as a raw transport error from a
  dead socket.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#YSBkYWVtb24gdGhhdCBkaWVzIG1pZC1zZXNzaW9uIGZhaWxzIHRoZSBjbGllbnQgdHlwZWQ — `sdk/typescript/test/client-disposal.test.ts :: "a daemon that dies mid-session fails the client typed"`
- AC4.10: Disposal signals the launched child **handle**, never a pid read back
  from the ready file, and a daemon already observed to have exited is not
  signalled at all — so a reused pid can never be signalled by mistake.
  - verify: vitest:sdk/typescript/test/client-disposal.test.ts#ZGlzcG9zYWwgc2lnbmFscyB0aGUgY2hpbGQgaGFuZGxlIGFuZCBuZXZlciBhIHBpZCBmcm9tIHRoZSByZWFkeSBmaWxl — `sdk/typescript/test/client-disposal.test.ts :: "disposal signals the child handle and never a pid from the ready file"`

---

### Scenario 5 — `query()`: one-shot lifecycle, `retainSession`, the plan refusal, the ask-deny diagnostic

`query()` is the one-call entry point: spawn, create, run, clean up
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 8,
and [#821](https://github.com/stacklok/mecatl/issues/821)'s "Public API shape"
and "Permissions" sections).

**Work:** `query()` over the existing `Client`/`Session`/`Run` choreography; the
created-versus-supplied resource ledger; the plan-mode refusal; the responder-less
ask policy.

**Acceptance:**
- AC5.1: `query(prompt)` with no existing client spawns a daemon, creates a
  session, runs the prompt, and yields the same `Event` union a `Run` yields.
  - verify: vitest:sdk/typescript/test/query.test.ts#cXVlcnkgc3Bhd25zIHJ1bnMgYW5kIHlpZWxkcyB0aGUgb3JkaW5hcnkgZXZlbnQgdW5pb24 — `sdk/typescript/test/query.test.ts :: "query spawns runs and yields the ordinary event union"`
- AC5.2: On completion `query()` deletes its transient session and stops the
  daemon it spawned.
  - verify: vitest:sdk/typescript/test/query.test.ts#cXVlcnkgZGVsZXRlcyBpdHMgdHJhbnNpZW50IHNlc3Npb24gYW5kIHN0b3BzIGl0cyBkYWVtb24 — `sdk/typescript/test/query.test.ts :: "query deletes its transient session and stops its daemon"`
- AC5.3: `query(prompt, { retainSession: true })` does not delete its session and
  reports the session id; the default deletes it. Retention is scoped honestly to
  what the SDK-owned argv can deliver — `mecated`'s `--store-dir` defaults to
  empty, meaning an **in-memory** store, and `spawn()` does not pass that flag —
  so a retained session outlives the `query()` call, not the daemon, unless the
  caller supplied a durable store.
  - verify: vitest:sdk/typescript/test/query.test.ts#cmV0YWluU2Vzc2lvbiBrZWVwcyB0aGUgc2Vzc2lvbiBmb3IgdGhlIGRhZW1vbidzIGxpZmV0aW1lIGFuZCByZXBvcnRzIGl0cyBpZA — `sdk/typescript/test/query.test.ts :: "retainSession keeps the session for the daemon's lifetime and reports its id"`
- AC5.4: `query()` given an existing `Client` closes no client, stops no daemon,
  and still deletes its own transient session.
  - verify: vitest:sdk/typescript/test/query.test.ts#cXVlcnkgY2xvc2VzIG9ubHkgdGhlIHJlc291cmNlcyBpdCBjcmVhdGVk — `sdk/typescript/test/query.test.ts :: "query closes only the resources it created"`
- AC5.5: A permission ask with no `onPermissionAsk` responder is **denied**, the
  run continues to its terminal, and the model is never silently allowed.
  - verify: vitest:sdk/typescript/test/query.test.ts#YW4gYXNrIHdpdGhvdXQgYSByZXNwb25kZXIgaXMgZGVuaWVkIGFuZCB0aGUgcnVuIGNvbnRpbnVlcw — `sdk/typescript/test/query.test.ts :: "an ask without a responder is denied and the run continues"`
- AC5.6: That denial emits exactly one diagnostic record naming the tool and the
  ask, and emits no `session.Event` of its own.
  - verify: vitest:sdk/typescript/test/query.test.ts#dGhlIHJlc3BvbmRlci1sZXNzIGRlbmlhbCBlbWl0cyBvbmUgZGlhZ25vc3RpYyBhbmQgbm8gZXZlbnQ — `sdk/typescript/test/query.test.ts :: "the responder-less denial emits one diagnostic and no event"`
- AC5.7: `query()` in plan mode fails with a typed `unsupported_feature` naming
  `session.resolvePlan()`, **before** any daemon is spawned.
  - verify: vitest:sdk/typescript/test/query.test.ts#cGxhbiBtb2RlIGlzIHJlZnVzZWQgYmVmb3JlIHNwYXduaW5n — `sdk/typescript/test/query.test.ts :: "plan mode is refused before spawning"`
- AC5.8: A `query()` aborted through its signal, or abandoned by `break`ing out
  of its iteration, still performs the full cleanup its successful path performs.
  - verify: vitest:sdk/typescript/test/query.test.ts#YW4gYWJvcnRlZCBvciBhYmFuZG9uZWQgcXVlcnkgc3RpbGwgY2xlYW5zIHVw — `sdk/typescript/test/query.test.ts :: "an aborted or abandoned query still cleans up"`
- AC5.9: A failure during `query()`'s own setup — spawn, create, or run — cleans
  up whatever it had already created before the typed error propagates.
  - verify: vitest:sdk/typescript/test/query.test.ts#YSBtaWQtc2V0dXAgZmFpbHVyZSB1bndpbmRzIHdoYXQgcXVlcnkgYWxyZWFkeSBjcmVhdGVk — `sdk/typescript/test/query.test.ts :: "a mid-setup failure unwinds what query already created"`
- AC5.10: The responder-less denial is `query()`'s policy, not the `Run`'s: a
  `Run` obtained outside `query()` with no `onPermissionAsk` still leaves the
  ask **pending**, and `resolveAsk()` still resolves it — the M1/M2 manual path
  is unchanged.
  - verify: vitest:sdk/typescript/test/query.test.ts#dGhlIHJlc3BvbmRlci1sZXNzIGRlbmlhbCBpcyBxdWVyeSBwb2xpY3kgYW5kIG5ldmVyIHRoZSBydW4ncw — `sdk/typescript/test/query.test.ts :: "the responder-less denial is query policy and never the run's"`

---

### Scenario 6 — `tool()`: registration, schema validation, naming, collisions, annotations

A TypeScript function becomes a model-callable tool with a validated schema and
a name the harness will not silently shadow
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decision 9).

**Work:** `tool(name, schema, handler)` on the `./node` entry point; local JSON
Schema 2020-12 validation; the server-name grammar mirror; the `ListMcpSources`
pre-flight; the read-only annotation.

**Acceptance:**
- AC6.1: A registered tool reaches the session as one `McpServerSpec` entry
  whose `type` is `http`, whose `url` is an `http` URL naming the host's
  literal loopback address and port, and whose `headers` carry the bearer — one
  spec for the whole tool set, not one per tool.
  - verify: vitest:sdk/typescript/test/tool.test.ts#dGhlIHRvb2wgc2V0IHRyYXZlbHMgYXMgYSBzaW5nbGUgbG9vcGJhY2sgaHR0cCBzZXJ2ZXIgc3BlYw — `sdk/typescript/test/tool.test.ts :: "the tool set travels as a single loopback http server spec"`
- AC6.2: A tool named `lookup` on the default server name is offered to the
  model as `mcp__sdk__lookup`.
  - verify: vitest:sdk/typescript/test/tool.test.ts#dGhlIGRlZmF1bHQgc2VydmVyIG5hbWUgeWllbGRzIG1jcF9fc2RrX18gcHJlZml4ZWQgdG9vbHM — `sdk/typescript/test/tool.test.ts :: "the default server name yields mcp__sdk__ prefixed tools"`
- AC6.3: A configured server name is honoured, and one violating the harness's
  grammar — empty, over-long, containing `__`, or outside `[A-Za-z0-9._-]` — is
  refused locally with a typed `tool_registration` rather than discovered as a
  server rejection.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YW4gaW52YWxpZCBzZXJ2ZXIgbmFtZSBpcyByZWZ1c2VkIGxvY2FsbHkgYWdhaW5zdCB0aGUgaGFybmVzcyBncmFtbWFy — `sdk/typescript/test/tool.test.ts :: "an invalid server name is refused locally against the harness grammar"`
- AC6.4: Two tools registered under the same name on one client fail with a
  typed `tool_registration` naming the duplicate.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YSBkdXBsaWNhdGUgdG9vbCBuYW1lIGZhaWxzIHJlZ2lzdHJhdGlvbg — `sdk/typescript/test/tool.test.ts :: "a duplicate tool name fails registration"`
- AC6.5: A tool name that would produce a malformed namespaced name — empty, or
  containing `__` — is refused at registration.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YSB0b29sIG5hbWUgdGhhdCB3b3VsZCBmb3JnZSBhIG5hbWVzcGFjZSBpcyByZWZ1c2Vk — `sdk/typescript/test/tool.test.ts :: "a tool name that would forge a namespace is refused"`
- AC6.6: Creating a tool-bearing session pre-flights `ListMcpSources` and fails
  with a typed `tool_registration` when the chosen server name matches a
  resolved server-global server name, instead of creating a session whose tools
  are silently shadowed.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YSBzZXJ2ZXItZ2xvYmFsIG5hbWUgY29sbGlzaW9uIGlzIHJlZnVzZWQgYnkgdGhlIHByZS1mbGlnaHQ — `sdk/typescript/test/tool.test.ts :: "a server-global name collision is refused by the pre-flight"`
- AC6.7: A schema that is not valid JSON Schema 2020-12 fails at registration
  with a typed `tool_registration`, before any session exists.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YW4gaW52YWxpZCBzY2hlbWEgZmFpbHMgYXQgcmVnaXN0cmF0aW9uIHRpbWU — `sdk/typescript/test/tool.test.ts :: "an invalid schema fails at registration time"`
- AC6.8: Arguments are validated against the schema **before** the handler runs;
  a violating call never reaches the handler and returns a model-visible
  validation error. Validation is **non-mutating** — no type coercion, no
  default injection — so the handler receives the model's arguments unchanged
  rather than a payload the validator rewrote.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YXJndW1lbnRzIGFyZSB2YWxpZGF0ZWQgYmVmb3JlIHRoZSBoYW5kbGVyIGlzIGludm9rZWQ — `sdk/typescript/test/tool.test.ts :: "arguments are validated before the handler is invoked"`
- AC6.9: Registration requires no Zod, TypeBox, or any other schema library: a
  plain JSON Schema 2020-12 object is sufficient.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YSBwbGFpbiBKU09OIFNjaGVtYSBvYmplY3QgaXMgc3VmZmljaWVudCB0byByZWdpc3RlciBhIHRvb2w — `sdk/typescript/test/tool.test.ts :: "a plain JSON Schema object is sufficient to register a tool"`
- AC6.10: A tool is advertised as mutating by default and as read-only only when
  explicitly annotated, and the annotation is carried as MCP's `readOnlyHint`.
  - verify: vitest:sdk/typescript/test/tool.test.ts#dG9vbHMgZGVmYXVsdCB0byBtdXRhdGluZyBhbmQgcmVhZC1vbmx5IGlzIGV4cGxpY2l0 — `sdk/typescript/test/tool.test.ts :: "tools default to mutating and read-only is explicit"`
- AC6.11: The tool set is fixed when the session is created: a registration
  attempted afterwards fails with a typed `invalid_state`, and the session's
  advertised tools do not change.
  - verify: vitest:sdk/typescript/test/tool.test.ts#dGhlIHRvb2wgc2V0IGlzIGltbXV0YWJsZSBvbmNlIGEgc2Vzc2lvbiBpcyBjcmVhdGVk — `sdk/typescript/test/tool.test.ts :: "the tool set is immutable once a session is created"`
- AC6.12: Validated arguments are materialised on a null-prototype object: a
  payload carrying `__proto__`, `constructor`, or `prototype` neither pollutes
  `Object.prototype` nor reaches the handler as an inherited property.
  - verify: vitest:sdk/typescript/test/tool.test.ts#YXJndW1lbnQgcGF5bG9hZHMgY2Fubm90IHBvbGx1dGUgcHJvdG90eXBlcyBvciBpbmhlcml0IGludG8gdGhlIGhhbmRsZXI — `sdk/typescript/test/tool.test.ts :: "argument payloads cannot pollute prototypes or inherit into the handler"`
- AC6.13: The public type and its documentation state that `readOnly` is a
  caller **assertion** the SDK does not verify, because the harness derives its
  dispatch class from that hint — a mis-annotated tool joins the concurrent read
  batch — and plan mode's hard-deny set is a fixed tool-name list no `mcp__`
  name is in.
  - verify: vitest:sdk/typescript/test/tool.test.ts#cmVhZE9ubHkgaXMgZG9jdW1lbnRlZCBhcyBhbiB1bnZlcmlmaWVkIGNhbGxlciBhc3NlcnRpb24 — `sdk/typescript/test/tool.test.ts :: "readOnly is documented as an unverified caller assertion"`

---

### Scenario 7 — The loopback MCP host: bearer capability, concurrency, cancellation, results, and the error split

The host is the thing the daemon actually talks to
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decisions 10
and 11). Its protocol surface is five methods; its security surface is loopback
plus a bearer; its execution surface is a bounded queue.

**Work:** the stateless streaming-HTTP server on `node:http`; the bearer mint
and constant-time compare; the semaphore and queue; result normalisation; the
two error paths.

**Acceptance:**
- AC7.1: The host answers the **six** JSON-RPC methods the pinned client
  actually sends — `server/discover`, `initialize`, `notifications/initialized`,
  `tools/list`, `tools/call`, `ping` — over POST. `server/discover` is answered
  with a JSON-RPC method-not-found error so the client falls back to the legacy
  `initialize` handshake, a call is answered `application/json`, and the
  `notifications/initialized` **notification** is answered `202` with no body.
  GET and DELETE get `405` plus `Allow: POST` — the shape the Go client treats
  as a clean no-op.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGhvc3Qgc2VydmVzIHRoZSBmaXZlIG1ldGhvZHMgb3ZlciBqc29uIGFuZCA0MDVzIHRoZSBzdHJlYW0 — `sdk/typescript/test/tool-host.test.ts :: "the host serves the five methods over json and 405s the stream"`
- AC7.2: The host's bound address is literally `127.0.0.1` on an ephemeral
  port — never `0.0.0.0`, never `::`, never a hostname — and the URL handed to
  the daemon is built from that literal address rather than the name
  `localhost`, which `isLoopbackHost` accepts and the daemon would then resolve
  through its own OS resolver.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGhvc3QgaXMgbG9vcGJhY2stb25seSBpbmRlcGVuZGVudCBvZiB0aGUgYmVhcmVy — `sdk/typescript/test/tool-host.test.ts :: "the host is loopback-only independent of the bearer"`
- AC7.3: Missing, wrong, and truncated bearers are all refused by one
  indistinguishable response taking the same path, and the comparison is a
  constant-time equality over fixed-length digests of the presented and expected
  values — so there is no length branch to leak through and no throw on a short
  input.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#YSBiYWQgYmVhcmVyIGlzIHJlZnVzZWQgYnkgYSBjb25zdGFudC10aW1lIGNvbXBhcmlzb24 — `sdk/typescript/test/tool-host.test.ts :: "a bad bearer is refused by a constant-time comparison"`
- AC7.4: The bearer is minted from a cryptographic source — `node:crypto`'s
  `randomBytes(32)` or `webcrypto.getRandomValues`, never `Math.random()` — at
  full length with no truncation, is distinct per client, and appears in no log
  line, no error message, no diagnostic record, and no exposed daemon fact.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGJlYXJlciBpcyBwZXItY2xpZW50IGFuZCBuZXZlciBzdXJmYWNlcyBvdXR3YXJk — `sdk/typescript/test/tool-host.test.ts :: "the bearer is per-client and never surfaces outward"`
- AC7.5: At most eight handlers run concurrently by default; a ninth call is
  queued and runs when a slot frees, rather than being rejected.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#Y29uY3VycmVuY3kgaXMgYm91bmRlZCBhdCBlaWdodCBhbmQgZXhjZXNzIGlzIHF1ZXVlZA — `sdk/typescript/test/tool-host.test.ts :: "concurrency is bounded at eight and excess is queued"`
- AC7.6: A per-tool concurrency limit may only tighten the client bound, never
  raise it.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#YSBwZXItdG9vbCBsaW1pdCB0aWdodGVucyBidXQgbmV2ZXIgcmFpc2VzIHRoZSBjbGllbnQgYm91bmQ — `sdk/typescript/test/tool-host.test.ts :: "a per-tool limit tightens but never raises the client bound"`
- AC7.7: Every handler receives an `AbortSignal`, and cancelling the call
  aborts it; a handler that ignores the signal cannot wedge the host.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#aGFuZGxlcnMgcmVjZWl2ZSBhbiBhYm9ydCBzaWduYWwgdGhhdCBjYW5jZWxsYXRpb24gZmlyZXM — `sdk/typescript/test/tool-host.test.ts :: "handlers receive an abort signal that cancellation fires"`
- AC7.8: A handler returning a string yields one text content block; a handler
  returning any other JSON value yields structured content plus its text mirror;
  a handler returning an explicit `CallToolResult` is passed through verbatim,
  `isError` included.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#cmVzdWx0cyBub3JtYWxpc2UgdGhyZWUgd2F5cyBhbmQgYW4gZXhwbGljaXQgcmVzdWx0IHBhc3NlcyB0aHJvdWdo — `sdk/typescript/test/tool-host.test.ts :: "results normalise three ways and an explicit result passes through"`
- AC7.9: A thrown handler exception yields a model-facing error result carrying
  a generic message and a correlation id — never the exception message, stack,
  or cause — while the full cause reaches the diagnostics sink under the same
  correlation id.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#YSB0aHJvd24gZXhjZXB0aW9uIGlzIGdlbmVyaWMgdG8gdGhlIG1vZGVsIGFuZCBmdWxsIHRvIGRpYWdub3N0aWNz — `sdk/typescript/test/tool-host.test.ts :: "a thrown exception is generic to the model and full to diagnostics"`
- AC7.10: An explicit `isError` result is delivered to the model verbatim, so an
  intentional failure remains distinguishable from a crash.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#YW4gaW50ZW50aW9uYWwgZXJyb3IgcmVzdWx0IGlzIGRlbGl2ZXJlZCB2ZXJiYXRpbQ — `sdk/typescript/test/tool-host.test.ts :: "an intentional error result is delivered verbatim"`
- AC7.11: Closing the client stops the listener, aborts in-flight handlers, and
  drops queued calls; the port is released.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#Y2xvc2luZyB0aGUgY2xpZW50IHN0b3BzIHRoZSBsaXN0ZW5lciBhbmQgZHJhaW5zIHRoZSBxdWV1ZQ — `sdk/typescript/test/tool-host.test.ts :: "closing the client stops the listener and drains the queue"`
- AC7.12: The bearer appears in neither the spawn argv, the child's
  environment, the runtime directory, nor any file on disk; its sole outbound
  channel is `McpServerSpec.headers` over the UDS gRPC connection — so it is
  invisible to `ps` and to every shell the daemon later spawns, whose
  `envscrub` denylist would not know its name.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGJlYXJlciBuZXZlciByZWFjaGVzIGFyZ3YgdGhlIGNoaWxkIGVudmlyb25tZW50IG9yIGRpc2s — `sdk/typescript/test/tool-host.test.ts :: "the bearer never reaches argv the child environment or disk"`
- AC7.13: Every method other than POST — `OPTIONS` included — is answered
  `405` with `Allow: POST` and **no** `Access-Control-Allow-*` header, and a
  request carrying an `Origin` header, or a `Host` other than the bound
  `127.0.0.1:<port>`, is refused before authentication — the DNS-rebinding and
  browser-reachability control the MCP spec requires of a local server.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGhvc3QgZW1pdHMgbm8gY29ycyBoZWFkZXJzIGFuZCByZWZ1c2VzIGZvcmVpZ24gb3JpZ2luIG9yIGhvc3Q — `sdk/typescript/test/tool-host.test.ts :: "the host emits no cors headers and refuses foreign origin or host"`
- AC7.14: An unauthenticated request is refused **before** its body is read,
  request bodies are size-capped, the call queue has a fixed maximum depth
  beyond which a call returns a model-visible error rather than growing memory,
  and every call carries a wall-clock deadline after which its `AbortSignal`
  fires and the host returns a typed error result without waiting for the
  handler.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGhvc3QgYm91bmRzIGJvZGllcyBxdWV1ZSBkZXB0aCBhbmQgY2FsbCBkZWFkbGluZXM — `sdk/typescript/test/tool-host.test.ts :: "the host bounds bodies queue depth and call deadlines"`
- AC7.15: The `protocolVersion` the host echoes in its `initialize` result is a
  member of the Go SDK's `supportedProtocolVersions` set, because `Client.Connect`
  closes the session outright when it is not; the host also declares its
  supported-version set so the client's `negotiateMutuallySupportedVersion` path
  has something to negotiate against rather than mismatching silently.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#dGhlIGluaXRpYWxpemUgcmVzdWx0IGRlY2xhcmVzIGEgbmVnb3RpYWJsZSBwcm90b2NvbCB2ZXJzaW9uIHNldA — `sdk/typescript/test/tool-host.test.ts :: "the initialize result declares a negotiable protocol version set"`
- AC7.16: A handler result whose serialized size would exceed the harness's
  tool-output cap is refused **client-side** with a typed error naming the cap
  and the tool, rather than sent and then mangled: the harness truncates an
  over-cap string and **fail-closes** an over-cap structured result into a
  remediation message that tells the model to paginate or use a jq filter —
  neither of which a user-authored TypeScript callback can do.
  - verify: vitest:sdk/typescript/test/tool-host.test.ts#YW4gb3Zlci1jYXAgaGFuZGxlciByZXN1bHQgaXMgcmVmdXNlZCBjbGllbnQtc2lkZSByYXRoZXIgdGhhbiBtYW5nbGVk — `sdk/typescript/test/tool-host.test.ts :: "an over-cap handler result is refused client-side rather than mangled"`

---

### Scenario 8 — Remote and HTTP-enabled clients refuse `tool()`, typed

Callback tools are local-only in v0.1, and the refusal has to be a typed error
a caller can branch on rather than a runtime surprise
([ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) Decisions 9
and 13; [#821](https://github.com/stacklok/mecatl/issues/821) M3 item 3).

**Acceptance:**
- AC8.1: `tool()` on a `connect()`ed client fails with a typed
  `unsupported_feature`, and no MCP host is started.
  - verify: vitest:sdk/typescript/test/tool-refusal.test.ts#YSBjb25uZWN0ZWQgY2xpZW50IHJlZnVzZXMgdG9vbCB3aXRoIHVuc3VwcG9ydGVkX2ZlYXR1cmU — `sdk/typescript/test/tool-refusal.test.ts :: "a connected client refuses tool with unsupported_feature"`
- AC8.2: The refusal happens on the client, before any RPC — a remote daemon is
  never asked to mount something the SDK will not host.
  - verify: vitest:sdk/typescript/test/tool-refusal.test.ts#dGhlIHJlZnVzYWwgaXMgbG9jYWwgYW5kIGlzc3VlcyBubyBycGM — `sdk/typescript/test/tool-refusal.test.ts :: "the refusal is local and issues no rpc"`
- AC8.3: `tool()` on a client spawned with `http: true` fails with a typed
  `unsupported_feature` whose message names the missing
  `mcp_servers_on_create` feature.
  - verify: vitest:sdk/typescript/test/tool-refusal.test.ts#YW4gaHR0cC1lbmFibGVkIHNwYXduZWQgY2xpZW50IG5hbWVzIHRoZSBtaXNzaW5nIGZlYXR1cmU — `sdk/typescript/test/tool-refusal.test.ts :: "an http-enabled spawned client names the missing feature"`
- AC8.4: A tool-bearing create against a daemon that refuses `mcp_servers`
  surfaces the server's own `client_mcp_unsupported` code unchanged, not a
  reinvented local one.
  - verify: vitest:sdk/typescript/test/tool-refusal.test.ts#YSBzZXJ2ZXIgcmVmdXNhbCBzdXJmYWNlcyBjbGllbnRfbWNwX3Vuc3VwcG9ydGVkIHVuY2hhbmdlZA — `sdk/typescript/test/tool-refusal.test.ts :: "a server refusal surfaces client_mcp_unsupported unchanged"`
- AC8.5: A tool host the daemon cannot reach surfaces the server's
  `client_mcp_unreachable` code, and the SDK does not report the session as
  created.
  - verify: vitest:sdk/typescript/test/tool-refusal.test.ts#YW4gdW5yZWFjaGFibGUgaG9zdCBzdXJmYWNlcyBjbGllbnRfbWNwX3VucmVhY2hhYmxl — `sdk/typescript/test/tool-refusal.test.ts :: "an unreachable host surfaces client_mcp_unreachable"`
- AC8.6: The four local codes this milestone adds — `spawn_failed`,
  `readiness_timeout`, `unsupported_platform`, `tool_registration` — are the
  only additions to `SDKErrorCode`, and no server code is shadowed by a local
  one.
  - verify: vitest:sdk/typescript/test/tool-refusal.test.ts#ZXhhY3RseSBmb3VyIGxvY2FsIGNvZGVzIGFyZSBhZGRlZCBhbmQgbm9uZSBzaGFkb3dzIGEgc2VydmVyIGNvZGU — `sdk/typescript/test/tool-refusal.test.ts :: "exactly four local codes are added and none shadows a server code"`

---

### Scenario 9 — Go parity guards: the ready file, the feature identifier, the spec fields, the flags

Four Go tests in the root module keep the client's hard-coded knowledge of the
server honest, extending the mechanism M1's
`TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited` and M2's three attach
parity guards already use
([`AGENTS.md`](../../AGENTS.md) — the engine tree is self-contained, so these
live beside the existing `internal/adapter/server/sdk_typescript_*_test.go`
files and never under `engine/`). The `cmd/mecated`-facing guards **source-parse**
`cmd/mecated/*.go` with the established `readParitySource` idiom rather than
importing: `readyDoc`, `readyDocSchema` and the `serve` flag set are unexported
identifiers in `package main`, and the Definition of done forbids adding a test
file under `cmd/mecated/`.

**Acceptance:**
- AC9.1: A Go guard fails when the SDK's ready-document type and schema constant
  drift from `readyDoc` and `readyDocSchema` — a field the daemon publishes that
  the SDK does not model, or a schema string that no longer matches.
  - verify: `TestSDKTypescriptLocal_Scenario9_ReadyDocSchemaParity`
- AC9.2: A Go guard fails when the feature identifier the SDK gates `tool()` on
  is not `server.FeatureMCPServersOnCreate`.
  - verify: `TestSDKTypescriptLocal_Scenario9_ClientMCPFeatureIdParity`
- AC9.3: A Go guard fails when the field names the SDK sends for one MCP server
  drift from `McpServerSpec`'s proto field names.
  - verify: `TestSDKTypescriptLocal_Scenario9_McpServerSpecFieldParity`
- AC9.4: A Go guard fails when any flag string in the SDK's spawn argv is not a
  flag `mecated serve` actually defines, so a renamed flag breaks CI rather than
  the first user's spawn.
  - verify: `TestSDKTypescriptLocal_Scenario9_SpawnFlagParity`
- AC9.5: All four guards read the TypeScript sources from `sdk/typescript/` and
  live in the root module; the engine module is untouched.
  - verify: `TestSDKTypescriptLocal_Scenario9_ReadyDocSchemaParity`, `TestSDKTypescriptLocal_Scenario9_SpawnFlagParity`
- AC9.6: A Go guard fails when the server-name grammar, the name-length bound,
  or the client-server count bound the SDK enforces locally drift from
  `clientServerNameRune`, `MaxClientServerNameLen`, and `MaxClientServers` —
  the one hand-copied rule with a security rationale (namespace forgery)
  attached to it in the Go source.
  - verify: `TestSDKTypescriptLocal_Scenario9_ClientServerNameGrammarParity`
- AC9.7: A Go guard fails when the tool-result size cap the SDK enforces
  client-side (AC7.16) drifts from `toolkit.MaxOutputBytes`.
  - verify: `TestSDKTypescriptLocal_Scenario9_ToolOutputCapParity`

---

### Scenario 10 — Offline e2e on Node and Bun against a same-checkout `mecated`

Everything above is proven against seams and fakes. This scenario proves it on
the wire: a real `mecated --mock-script` from the same checkout, a real Go MCP
client calling a real TypeScript handler, on both supported runtimes
([#821](https://github.com/stacklok/mecatl/issues/821) M3 item 2 — "Exercise
Node and Bun clean shutdown, parent-death shutdown, retained sessions, startup
failure, and no-port-open assertions").

**Work:** a spawn-based e2e that uses the product `spawn()` rather than M1's
hand-rolled harness; a `--mock-script` fixture whose turn calls
`mcp__sdk__<tool>`; a Bun invocation of the same suite.

**Acceptance:**
- AC10.1: On Node, `spawn()` starts a same-checkout `mecated`, a scripted turn
  calls `mcp__sdk__lookup`, the TypeScript handler runs, and its result reaches
  the model's next turn — the full round trip on real wire.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#YSBzY3JpcHRlZCB0dXJuIGNhbGxzIGFuIHNkayB0b29sIGFuZCByZWNlaXZlcyBpdHMgcmVzdWx0 — `sdk/typescript/e2e/tool.e2e.test.ts :: "a scripted turn calls an sdk tool and receives its result"`
- AC10.2: One named test exercises the spawn → tool round trip → clean shutdown
  path under Bun. Whether the whole suite runs on Bun in CI is a job-composition
  fact discharged by AC10.12, not by this test.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#dGhlIHRvb2wgcm91bmQgdHJpcCBwYXNzZXMgb24gYnVu — `sdk/typescript/e2e/tool.e2e.test.ts :: "the tool round trip passes on bun"`
- AC10.3: The spawned daemon opens no TCP port: the ready document reports
  `unix` transport with no `http_address`, and a **real** TCP connect to
  `mecated`'s default `--grpc-addr` and default HTTP address, made while the
  daemon is up, is refused with `ECONNREFUSED`.
  - verify: vitest:sdk/typescript/e2e/spawn.e2e.test.ts#dGhlIHNwYXduZWQgZGFlbW9uIG9wZW5zIG5vIHRjcCBwb3J0 — `sdk/typescript/e2e/spawn.e2e.test.ts :: "the spawned daemon opens no tcp port"`
- AC10.4: `close()` stops the daemon and removes the socket and runtime
  directory; the process is gone by the time `close()` resolves.
  - verify: vitest:sdk/typescript/e2e/spawn.e2e.test.ts#Y2xvc2Ugc3RvcHMgdGhlIGRhZW1vbiBhbmQgcmVtb3ZlcyBpdHMgcnVudGltZSBkaXJlY3Rvcnk — `sdk/typescript/e2e/spawn.e2e.test.ts :: "close stops the daemon and removes its runtime directory"`
- AC10.5: Killing the parent process without a clean `close()` stops the daemon
  through the lifetime pipe, on Node and on Bun.
  - verify: vitest:sdk/typescript/e2e/spawn.e2e.test.ts#YSBraWxsZWQgcGFyZW50IHN0b3BzIHRoZSBkYWVtb24gdGhyb3VnaCB0aGUgbGlmZXRpbWUgcGlwZQ — `sdk/typescript/e2e/spawn.e2e.test.ts :: "a killed parent stops the daemon through the lifetime pipe"`
- AC10.6: `query(prompt, { retainSession: true })` on the real wire leaves a
  session a later client can load, and the default `query()` leaves none.
  - verify: vitest:sdk/typescript/e2e/spawn.e2e.test.ts#cmV0YWluU2Vzc2lvbiBzdXJ2aXZlcyBvbiB0aGUgd2lyZSBhbmQgdGhlIGRlZmF1bHQgZG9lcyBub3Q — `sdk/typescript/e2e/spawn.e2e.test.ts :: "retainSession survives on the wire and the default does not"`
- AC10.7: A startup failure on the real wire — an unresolvable binary and a
  binary given an argument `mecated` rejects — produces the typed error and the
  redacted stderr tail, with no orphan process.
  - verify: vitest:sdk/typescript/e2e/spawn.e2e.test.ts#YSByZWFsIHN0YXJ0dXAgZmFpbHVyZSBpcyB0eXBlZCB3aXRoIGEgcmVkYWN0ZWQgdGFpbCBhbmQgbm8gb3JwaGFu — `sdk/typescript/e2e/spawn.e2e.test.ts :: "a real startup failure is typed with a redacted tail and no orphan"`
- AC10.8: A handler that throws on the real wire yields a generic model-facing
  error, and the run continues to its terminal rather than failing.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#YSB0aHJvd2luZyBoYW5kbGVyIGlzIGdlbmVyaWMgb24gdGhlIHdpcmUgYW5kIHRoZSBydW4gY29udGludWVz — `sdk/typescript/e2e/tool.e2e.test.ts :: "a throwing handler is generic on the wire and the run continues"`
- AC10.9: A `readOnly: true` tool is dispatched in the harness's concurrent read
  batch — two calls in one turn overlap in flight, where a mutating tool is
  dispatched alone. `readOnlyHint` steers dispatch, not permissions, so a
  read-only callback still raises an ask; both halves run the same default
  posture, so the only variable between them is the `readOnly` flag.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#cmVhZC1vbmx5IGFuZCBtdXRhdGluZyB0b29scyBib3RoIHJlYWNoIHRoZSBtb2RlbA — `sdk/typescript/e2e/tool.e2e.test.ts :: "read-only and mutating tools both reach the model"`
- AC10.10: A default (mutating) tool raises a `permission.ask`; an
  `onPermissionAsk` responder allowing it lets the call reach the handler, and a
  responder-less `query()` denies it while the run still reaches its terminal.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#YSBtdXRhdGluZyB0b29sIGFza3MgYW5kIGEgcmVzcG9uZGVyLWxlc3MgcXVlcnkgZGVuaWVzIGl0 — `sdk/typescript/e2e/tool.e2e.test.ts :: "a mutating tool asks and a responder-less query denies it"`
- AC10.11: The MCP handshake is completed by the **real** pinned Go client
  against the hand-written host on the wire — version negotiation included —
  so a protocol revision that changes it fails this suite rather than a user's
  first tool call.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#dGhlIHJlYWwgZ28gbWNwIGNsaWVudCBjb21wbGV0ZXMgdGhlIGhhbmRzaGFrZSBhZ2FpbnN0IHRoZSBzZGsgaG9zdA — `sdk/typescript/e2e/tool.e2e.test.ts :: "the real go mcp client completes the handshake against the sdk host"`
- AC10.12: The `sdk` CI job runs the new unit suites and these e2e suites on
  Node, and the Bun leg either runs or is explicitly recorded as deferred with
  its reason.
  - verify: inspection — a CI job's composition is a workflow fact, not a unit-testable one; review checks the `sdk` job in `.github/workflows/ci.yml`
- AC10.13: A callback tool reached through the **default**
  `--authority-evaluator local` is denied by the capability-set evaluator, with
  the run still reaching its terminal. This pins the known limitation recorded
  under "Deferred decisions and known risks" so the eventual `RootAuthority`
  widening has a failing test to flip rather than a silent behaviour change.
  - verify: vitest:sdk/typescript/e2e/tool.e2e.test.ts#YSBjYWxsYmFjayB0b29sIGlzIGRlbmllZCBieSB0aGUgZGVmYXVsdCBjYXBhYmlsaXR5LXNldCBldmFsdWF0b3I — `sdk/typescript/e2e/tool.e2e.test.ts :: "a callback tool is denied by the default capability-set evaluator"`

---

## Cross-cutting deliverables

- [ADR-0292](../adr/0292-typescript-sdk-local-daemon-and-tools.md) — authored
  with this plan, on the stack root, not a worker task.
- API Extractor reports for `.` and `./node` regenerated intentionally as each
  scenario adds public surface (`spawn`, `SpawnOptions`, `query`,
  `QueryOptions`, `tool`, `ToolDefinition`, the diagnostics sink types, and the
  four new `SDKErrorCode` members). Every M3 addition belongs to `./node`
  except the diagnostics sink and the error codes; `./gen` stays
  codegen-governed per
  [ADR-0279](../adr/0279-typescript-sdk-architecture.md) Decision 4.
- `docs/architecture.md`: extend the SDK section with the local daemon and the
  tool host; `docs/design/IMPLEMENTATION-NOTES.md`: the dense notes — the
  resolution order, the argv contract and its coupling to
  `clientMCPOnCreateForListeners`, the readiness classification, the lifetime
  pipe, the disposal order, and the tool host's protocol subset.
- `user-docs/`: a short Node/Bun `spawn()` + `tool()` note per
  [`AGENTS.md`](../../AGENTS.md)'s user-docs rule, linking out to the full
  reference. The example set and the browser/BFF pages stay M4.
- `task docs` configuration-reference regeneration with every Markdown change.
- Any new runtime dependency (the JSON Schema validator) is added to
  `sdk/typescript/package.json` with its licence recorded, and the `.`
  entrypoint's module graph is asserted free of it by the existing package
  guard test.
- No `engine/` API change is expected. If one appears, `task api:check` /
  `task api:update` plus the `engine/CHANGELOG.md` note per the standing rule.

## Sequencing recommendation

Scenario 1 is strictly first: the launcher seam and the ready-file barrier it
introduces are what every later spawn-shaped scenario is written against.
Scenario 2 follows immediately — it fixes the daemon topology the whole tool
half depends on. Scenario 3 introduces the diagnostics sink, which Scenarios 4,
5 and 7 all emit into, so it must precede them. Scenarios 4 and 5 then
parallelize. Scenarios 6 and 7 are the tightest coupling in the plan — the
registration contract and the host that serves it are two halves of one
mechanism — so a single worker taking both is reasonable; Scenario 8 is the
refusal half of Scenario 6 and should follow it. Scenario 9 depends only on
Scenarios 1, 2 and 6 having fixed their constants and can land any time after
them. Scenario 10 lands last, consuming everything. As in M1 and M2, the
exports barrel and the error hierarchy are merge-conflict hotspots rather than
serialization points.

## Named tests landing in this plan

- `TestSDKTypescriptLocal_Scenario9_ReadyDocSchemaParity`
- `TestSDKTypescriptLocal_Scenario9_ClientMCPFeatureIdParity`
- `TestSDKTypescriptLocal_Scenario9_McpServerSpecFieldParity`
- `TestSDKTypescriptLocal_Scenario9_SpawnFlagParity`
- `TestSDKTypescriptLocal_Scenario9_ClientServerNameGrammarParity`
- `TestSDKTypescriptLocal_Scenario9_ToolOutputCapParity`

All six live in the root module beside the existing
`internal/adapter/server/sdk_typescript_*_test.go` parity guards — they read
TypeScript sources from `sdk/typescript/`, so they can never live under
`engine/` ([`AGENTS.md`](../../AGENTS.md): the engine tree is self-contained and
its module boundary rejects a cross-tree read). All other proofs are vitest
suites under `sdk/typescript/`, cited per AC.

## Definition of done

1. `task lint` and `task test` pass (both Go modules, `-race`), plus
   `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:e2e`, and
   `task sdk:api:check`.
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green;
   `task site:build` green for the `user-docs/` addition.
3. `task generate` reproduces both generated trees byte-identically — this plan
   touches no proto, so it is a no-op.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (once this plan
   is `landed`), including every `vitest:` resolver token.
5. The six named Go parity tests are green and grep-locatable by their
   identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. `contracts/proto/`, production `internal/adapter/server/`, and
   `cmd/mecated/` are byte-unchanged in this plan's diff; the only Go additions
   are the six parity `_test.go` files.
8. No test reaches a live model or an external network; the tool host binds
   loopback only.

## Deferred decisions and known risks

- **RESOLVED — Node and Bun inherit the lifetime socketpair on fd 3.** The
  server-enabler pre-PR widened `checkLifetimePipeFD`
  ([`cmd/mecated/lifetimefd_unix.go`](../../cmd/mecated/lifetimefd_unix.go))
  to admit either a FIFO or a connected local stream socket and still reject
  every other descriptor shape. The SDK therefore uses libuv's fourth
  `child_process` stdio entry without `mkfifo(1)`. Scenario 2 pins the server
  parity and Scenario 10 kills both a Node and a Bun parent, proving the daemon
  observes EOF and exits.
- **`retainSession` retains into an in-memory store on the SDK-owned argv.**
  `--store-dir` defaults to empty ("in-memory store") and `spawn()` does not
  pass it, so a retained session outlives the `query()` call but not the daemon.
  AC5.3 and AC10.6 are worded to that truth. Making retention durable needs a
  `storeDir` spawn option pointing **outside** the runtime directory AC1.8
  deletes on disposal — a public-API addition deliberately not made here.
- **The AC6.6 pre-flight can false-positive.** `ListMcpSources` reports resolved
  server *candidates* whether or not they connected, so a dead operator server
  named `sdk` refuses a create that would have worked. Fail-closed, and cheap to
  work around by configuring a different server name, but it is a real
  false-refusal.
- **The server enablers this plan stands on are recorded in a `draft` plan.**
  [`sdk-server-enablers.md`](sdk-server-enablers.md) Scenarios 8 and 9 describe
  code that is genuinely on `main` and was re-audited for this plan, but that
  document's status means `ac-trace --strict` does not gate those ACs.
- **The "no TCP port" claim is about the daemon, not the system.** AC3.6 and
  AC10.3 assert the spawned *daemon* opens none — and it does not. The SDK's own
  tool host is a loopback TCP listener in the client process, which is forced:
  `ValidateClientURL` accepts only `https` or `http`, so there is no UDS MCP
  transport to use instead. The bearer is therefore the sole authentication on a
  port every local process can reach, which is why AC7.2, AC7.3, AC7.4, AC7.13
  and AC7.14 are as specific as they are.
- **A same-named client server inherits the operator's permission rules even
  when the pre-flight sees nothing.** Permission rules glob on tool names
  (`mcp__github__*`), so a client server named after an operator's server picks
  up that operator's rule whether or not the real server is present in
  `ListMcpSources` — the AC6.6 pre-flight is structurally blind to the absent
  case. This is a different residual from silent shadowing: shadowing costs
  availability, this costs an auto-allow. The default name `sdk` keeps it low,
  but the name is caller-configurable.
- **`readOnly` is an unverified assertion with a dispatch consequence.**
  `remoteTool.ReadOnly()` derives from the hint and drives
  read-parallel/mutate-serial, while plan mode's hard-deny set is the fixed
  `{Edit, Write}` list no `mcp__` name is in. A mis-annotated SDK tool therefore
  joins the concurrent read batch and is not plan-mode denied. AC6.10's
  mutating-by-default and AC6.13's documented-assertion wording are the
  mitigations; closing it properly is an MCP-wide question, not this plan's.
- **`query()` has no plan mode, and that diverges from #821 as written.** The
  issue asks for a `query()` that requires `onPlanApproval`; this plan refuses
  plan mode instead, because `ApprovePlan` streams a resumed run and a
  continuation run with a different id and needs the `PlanResolution` type #821
  itself scopes to M4. The alternative — ship `onPlanApproval` now and wire the
  minimal `ApprovePlan` path — pulls M4 work into M3 and adds an options field
  M4 must reinterpret. This is the plan's largest open decision.
- **RESOLVED — Bun's fourth-stdio inheritance works and runs in CI.** Bun 1.4.1
  executes the same built-package spawn → tool → clean-close helper as Node,
  and AC10.5 kills a Bun parent while its daemon is live. The pinned Bun setup
  in the SDK CI job makes both proofs hard failures. The broader browser,
  TypeScript-declaration, and platform matrix remains M4 work; only the M3
  runtime claim moved forward here.
- **Namespace-collision detection is strong but not total.** The pre-flight
  closes the case that matters (an operator's server-global server sharing the
  SDK's name), but a source resolved after the pre-flight, or a tool-name
  collision inside a same-named source, stays invisible: the server's
  `mcp.Register` is skip-and-continue with only a server-side WARN, and
  `CreateSessionResponse` carries no tool inventory. Closing it properly needs a
  server-side mounted-tool inventory, which is out of scope.
- **BLOCKING FOR REAL USE — a callback tool is denied under the default
  `--authority-evaluator local`.** `mintRootAuthority`
  ([`internal/app/root_authority.go`](../../internal/app/root_authority.go)) mints
  the root capability set from the process-wide `assets.rootCatalog`, and
  `Service.RootAuthority`
  ([`internal/adapter/server/service.go`](../../internal/adapter/server/service.go))
  is a `func(session.SessionKind) session.Authority` that never sees a session's
  `mcp_servers` additions. A client MCP tool therefore mounts into the
  per-session catalog but is absent from the capability set, and
  `localauthority.Evaluator`
  ([`engine/adapter/localauthority/localauthority.go`](../../engine/adapter/localauthority/localauthority.go))
  denies it after the permission ask has already been allowed. Observed on the
  real wire: `tool.result` carries `tool "mcp__sdk__lookup" denied by authority:
  tool is absent from the capability set`, `isError: true`. `spawn()` does not
  pass `--authority-evaluator`, so **every default SDK deployment hits this** —
  the M3 callback-tool feature is wire-complete but not usable end-to-end until
  it is fixed. The fix must widen `RootAuthority` to carry the session's client
  MCP tool names, which is production `internal/adapter/server/` work and
  outside this plan's edit surface; it is tracked as a follow-up rather than
  patched here. Scenario 10's fixtures pass `--authority-evaluator noop` so the
  suite proves the SDK half — host handshake, dispatch, schema validation,
  refusals, ask routing — rather than silently re-proving the denial, and
  AC10.13 pins the default-posture denial so the limitation is test-covered and
  the eventual fix has a failing test to flip.
- **A hand-written MCP host is a standing compatibility liability.** Five
  methods and one response shape today, verified against the real Go client only
  by Scenario 10. A protocol revision that changes the handshake is SDK work,
  and a fake-only test suite would not catch it — which is why AC10.1 is on the
  real wire and not negotiable down to a stub.
- **The JSON Schema validator is a new runtime dependency on a published
  package.** Validating model-supplied arguments before user code runs is not
  something to hand-roll with a fail-open subset (the compromise mecatl's own Go
  `session.ValidateJSON` deliberately accepts elsewhere, for a different
  threat model). The candidate is `ajv` with its 2020-12 dialect; the
  zero-dependency alternative is `@cfworker/json-schema`. Either is
  `./node`-only. This is a dependency decision on a package we will publish, so
  it is called out rather than made silently in a PR.
- **Secret-shaped stderr redaction is a heuristic.** Whole-line replacement on a
  pattern match is strictly better than partial masking, but a credential
  written to stderr in an unrecognised shape survives into the tail. The daemon's
  own `envscrub` denylist is what actually keeps credentials out of agent-facing
  processes; the tail bound and the redaction are defence in depth, not the
  primary control.
- **`spawn()` inherits everything by default, which is the point and the risk.**
  A spawned daemon runs with the operator's real credentials, real config and
  real durable state. That is what makes `spawn()` useful and what makes a
  careless `query()` in a test suite able to touch real state. The mitigations
  are explicit `env` and `workspace` overrides, not a safer default — a
  sandboxed-by-default `spawn()` would be a different product.
- **The disposal order is asserted, not enforced by types.** AC4.1 pins tool
  host before transport before daemon signal, but nothing structurally prevents
  a later contributor from reordering it. If that proves fragile the fix is a
  single ordered teardown list with the test reading it, not more ACs.
- **The bundled authoring check warns "no named tests".** Its regex recognises
  only `TestADR_NNNN_*` and `TestInvariant_*`; this plan's Go proofs use the
  `Test<Plan>_Scenario<N>_*` form for the reason
  [`sdk-typescript-core.md`](sdk-typescript-core.md) records — an ADR renumber
  silently repointed an earlier plan's `TestADR_*` pins, once at a missing ADR
  and once at an unrelated one. The warning is accepted deliberately, not
  overlooked.

## Exit criteria

When every point under *Definition of done* holds across the merged stack, this
plan is satisfied.

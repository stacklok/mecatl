# ADR 0243 — SDK compatibility discovery and the typed error contract

- Status: Proposed
- Date: 2026-08-28
- Scope: `GetServerInfo`, the API-major/feature vocabulary, the stable error-code
  registry, RFC 9457 HTTP errors, gRPC status details, exact-origin CORS.

## Context

[Issue #821](https://github.com/stacklok/mecatl/issues/821) ships `@stacklok/mecatl`, a
TypeScript SDK over the existing gRPC and HTTP/SSE surfaces. Two server-side gaps block
it, and both are about a client being able to ask an honest question and get an honest
answer.

**Gap 1 — a client cannot discover what a server supports.** `ServerCapabilities`
([`contracts/proto/mecatl/v1/harness.proto`](../../contracts/proto/mecatl/v1/harness.proto))
already exists and already answers a question: *what has this operator enabled?* Its own
doc comment is explicit that each field "reflects the BUILT service state (registered
tools / wired seams)". `bash: false` means the operator ran `--no-bash`. That is not the
same question as *what does this build implement?* — and an SDK that conflates them will
either throw a compatibility error at a correctly-configured deployment (reading a
disabled capability as version skew) or call an RPC the server has never heard of
(reading protocol support out of an operator toggle). There is also no way to reach the
capabilities without creating a session: they ride `CreateSessionResponse`, so today
"what does this server support?" costs a probe session.

**Gap 2 — HTTP errors are untyped prose.** [`internal/adapter/server/http.go`](../../internal/adapter/server/http.go)
funnels every failure through one `writeError` that emits `{"error": "<message>"}` with
`Content-Type: application/json`. A client can read the HTTP status and a human-readable
string; it cannot branch on a stable machine identifier. gRPC callers get a `codes.Code`,
which is coarser than the domain (a dozen distinct conditions collapse onto
`FailedPrecondition`). The two transports therefore disagree about what went wrong, which
is a problem for an SDK whose whole premise is that Node and browser clients expose "the
same normalized public operations, errors, events, capabilities" (#821 AC3).

A third, smaller gap rides along: there is no CORS support at all, so a browser cannot
reach `mecated` directly even in local development.

Two options were rejected for the feature/error vocabularies:

- **A closed proto enum.** Generated into Go and TypeScript for free, exhaustive
  `switch` in both. But adding a value becomes a wire-compat event requiring
  `task generate` and a proto review, and an older SDK decodes every new value as
  `UNKNOWN` while its exhaustive switch silently grows a dead branch. A new server
  error code should be a minor SDK release, not a proto change.
- **Semver comparison** between client and server. #821 rules this out directly, and
  correctly: mecatl is deployed from `main` as often as from a tag, and a version string
  does not tell a client whether a specific RPC exists.

## Decision

**1. Add `GetServerInfo` as an authenticated RPC on `HarnessService`, with an HTTP peer.**
It answers the compatibility question without a probe session. #821 sets the floor: a
server that does not implement it (gRPC `UNIMPLEMENTED`) is below the SDK's compatibility
floor and yields `IncompatibleServerError`. The SDK never infers a legacy mode and never
creates a probe session to find out.

**2. `capabilities` and `features` are separate fields, because they answer different
questions.**

| Field | Question | Changes when |
| --- | --- | --- |
| `capabilities` (the existing `ServerCapabilities`, verbatim) | what has this operator enabled? | operator config changes |
| `features` (new, `repeated string`) | what does this build implement? | mecatl is upgraded |

`GetServerInfoResponse` carries `api_major`, `capabilities`, `features`, an optional
`build_version`, and an optional `deployment`.

**3. `features` are open strings, not an enum.** This is the discipline the event
taxonomy already settled on the same axis — [`AGENTS.md`](../../AGENTS.md) records that
`EvNoProgress`/`StopNoProgress` are "STRING passthroughs on the wire (proto `type`/`stop`
are strings, not enums — no `task generate` needed)". Each PR in the #821 server-enabler
stack registers its own identifier as it lands (`run_id`, `expected_run_id`,
`cursor_watch`, `mcp_servers_on_create`, …), so a partially-deployed stack — which will
exist, because these land in `main` incrementally — describes itself honestly.

**4. `api_major` starts at `1` and bumps only on a genuine break.** Every change in the
#821 stack is additive, so it stays `1` throughout. Being able to add a capability
without bumping the major is the entire purpose of `features`.

**5. Media capability stays session-authoritative.** `capabilities.image`/`.audio` on
`GetServerInfo` is a server-wide hint for UI chrome. The per-session
`CreateSessionResponse` echo remains the authority, per the existing
[`AGENTS.md`](../../AGENTS.md) invariant that capability truth is a single
composition-computed intersection that must not be recomputed per sink. An SDK validates
outbound media against the session echo, never against server info.

**6. `deployment` is operator-set, opaque, bounded, and empty by default.** It is never
derived from hostname, pod name, or environment. Infrastructure topology is not something
an authenticated caller is owed, and a deployment label that the operator did not choose
is a leak with no consenting author.

**7. Error identity is a stable open-string code, carried identically on both
transports.** A Go registry is the single source of truth. HTTP returns RFC 9457
`application/problem+json` with the code in `type`; gRPC returns the same string in a
status detail alongside its `codes.Code`. The `error` key is retained inside the problem
body as a compatibility extension during the transition.

**8. A Go↔TypeScript code-parity gate.** The TS surface is a union of string literals
plus an `unknown` arm; a CI gate walks the Go registry against it, so a new server code
fails CI until it is typed. This mirrors the event kind-parity gate #821 already
requires, and the `harnessTokenFields` idiom in `TestToProtoStructuralUTF8Guard` where
adding a line to an exemption list is a deliberate, visible act.

**9. CORS uses exact origin matching.** Credentials are permitted only for an exactly
allowed origin; wildcard-with-credentials is never emitted; preflight and `Vary: Origin`
are correct. This is the local-development path only — the production browser path
remains a same-origin BFF that injects bearer credentials server-side.

## Consequences

**Easier.** A client learns everything it needs about a server in one authenticated
call, before creating anything. Adding a server feature or an error code becomes a minor
SDK release rather than a proto change. Both transports report the same error identity,
so the SDK's normalization layer is a mapping rather than a reconciliation. Browsers can
reach a dev server without a proxy.

**Harder — and these are the honest costs.**

- **`features` is listener-dependent, not purely build-dependent.** Per
  [ADR 0245](./0245-durable-cursors-and-watch.md)'s sibling decision on `mcp_servers`
  (see [ADR 0237](./0237-listener-scoped-workspace-authority.md)), a feature that is only
  reachable on a UDS listener is advertised only on that listener. This muddies the clean
  capabilities/features split above: `features` is really "what this build implements
  *and this listener permits*". The alternative — advertise it always and let the SDK
  infer locality from the transport — was rejected because advertising a feature a caller
  cannot invoke is a lie the SDK would have to work around.
- **Open strings mean no exhaustiveness checking.** Neither Go nor TypeScript can prove a
  switch covers every code. The parity gate recovers most of this, but it is a CI check,
  not a type-system guarantee.
- **The `Content-Type` flip to `application/problem+json` is observable.** No in-repo
  non-test HTTP consumer exists today (`mecatui` is a gRPC client), and the `error` key
  survives inside the body, but any external client pattern-matching on
  `Content-Type: application/json` for errors will see a change. We accept this while the
  only known consumer is one we control; RFC 9457 compliance that lies about its media
  type is not compliance.
- **A hard compatibility floor.** Every `mecated` built before this ADR lands is
  unusable by the SDK, by design. There is no negotiated downgrade, because a downgrade
  path is a second protocol to maintain and test forever.
- **One more authenticated RPC on the heartbeat path.** #821 has the SDK poll
  `GetServerInfo` for connection status while status has subscribers, so this handler is
  on a recurring path and must stay cheap and allocation-light.

## See also

- [Issue #821](https://github.com/stacklok/mecatl/issues/821) — the SDK implementation
  plan this ADR unblocks; [#761](https://github.com/stacklok/mecatl/issues/761) — the
  proposal.
- [`docs/acceptance/sdk-server-enablers.md`](../acceptance/sdk-server-enablers.md) — the
  scenario-first acceptance contract for this decision.
- [ADR 0244](./0244-durable-run-identity.md) — durable run identity; [ADR 0245](./0245-durable-cursors-and-watch.md)
  — durable cursors and the watch transport. The two siblings in the same stack.
- [ADR 0237](./0237-listener-scoped-workspace-authority.md) — listener-scoped authority,
  the precedent for a per-listener feature.
- [ADR 0037](./0037-engine-stability-contract.md) — the engine public-API gate this stack
  is measured against.
- [`AGENTS.md`](../../AGENTS.md) — the capability-intersection and string-passthrough
  invariants cited above.

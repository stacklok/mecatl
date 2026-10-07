# Agent authority: implementation evidence and remaining work

Companion to the [agent-authority design](agent-authority.md). This document holds
branch pins, observed behavior, unresolved mechanisms and implementation work. The
main page states the intended requirements; none of the snapshots below establishes
the complete interactive flow.

## Evidence baseline: separate branches, not one product

These are inspected snapshots, excluding uncommitted changes and later commits.
Re-pin before implementation. Individual branch tests do not prove their combination.

### Current investigation pins

| Label | Source and pin | Observed scope and limits |
|---|---|---|
| **I** | Mecatl `acc/singleton-identity-mcp-integration` — `2ee96819939367e5f7c57fd48ead21fca828bd0a` | Harness-attested user evidence, logical-agent issuance and exact-call Execute admission. Issuer lacks human freshness; Execute checks it separately. |
| **B** | Mecatl `acc/singleton-mcp-broker-review-remediation-squash` — `06d1ecc2c12d6533e060d90809f4425c8d78294f` | External broker, ToolHive OAuth and guarded encrypted credential recovery; recovery does not preserve the original canonical human subject. |
| **B2** | Mecatl `broker-simple-take-2` — `0d9467664c7a496b8dda287183f89dedea3ec786` | Workload-authenticated broker sessions, browser OAuth, account continuity, encrypted custody and recovery. Initial caller-to-account authorization, instance-SVID exchange, and executing-run binding remain integration work. |
| **W** | ToolHive `spiffe-v2-14-docs` — `36f85e4b0f6b9e449fd2099ec164bb1204ded5cc` | SPIFFE WIP; distinct logical-agent actor validation and exchanged-token upstream linkage remain gaps. |
| **S** | Mecatl `broker-simple/04-toolhive-backend` — `e3ce6b374e74847c79f5ab9b69b987693d4540d6` | Newer committed-connection mapping with revision/provider checks; not B's storage implementation. |

**I and B depend on ToolHive v0.50.0, not W.** These pins must not be combined into
one shipped implementation. **P**, below, is the historical richer issuer baseline;
its issuer-admission guarantees must not be attributed to I.

### Historical evidence and branch relationships

| Source and pin | What it supplied at that snapshot | What it did not establish |
|---|---|---|
| `origin/main` — `a711451616edf84f50e793c9bcbfb697ef400347` | Caller/owner checks, carried authority, exact placement, dispatch/event logging and configured in-process MCP broker paths | Completed external user/agent admission or exhaustive shell-effect evidence |
| `handover/agent-mcp-authority-restart` — `ed319f45cd972de3cb95031b39e57b8eec425789` | Issuer signing/verification, logical-agent credentials, adapter-neutral acting-as-user exchange contract | Production ToolHive exchange or provider credential lookup |
| Earlier broker squash — `53eab6b753869b33c317da49a67295575807ac61` | TLS/workload-OIDC remote broker, callback lifecycle, process-local Execute receipts, configured encrypted custody recovery | Logical-agent admission, fresh user proof, HA or durable operation outcomes |
| **P**: identity integration — `db5bcd8845f87f05fa0c0efecb2092fd6c5e497c` | Constrained issuer, live exact-call Cedar admission and ToolHive fixture tests | Independently verified human evidence, scheduled grants or durable broker audit completion |
| `workload-identity` — `b6dc951c091197bcb4c3ec444a80308d43dc9e5a` | Historical broker-owned SPIFFE custody/proxy experiment | Selected production transport or an integration dependency |

At these historical pins, main was an ancestor of the squash, which superseded the
older review-remediation branch. Identity, squash and integration diverged at
`038b5739`; neither squash nor integration contained the other. P adapted the issuer
slice but had no `internal/actingaccess`: its broker-local admission was not simply
the donor exchange contract wired to ToolHive. These ancestry observations are not
an assertion about later I/B/S integration.

## Observed implementation

### Baseline and broker protections to preserve

Pinned main authenticates callers, checks ownership, reattaches exact placement,
applies local permissions/hooks/authority and records effective tool results.
Child [derivation and resume checks][delegation] prevent a broader definition from
restoring authority absent from its parent. These are harness checks, not signed
external grants. Ownerless compatibility exists with enforcement disabled; a shared
static bearer does not establish individual users.

The historical external broker authenticates workload OIDC over TLS, including
Kubernetes OIDC discovery/JWKS—not TokenReview. Workload/session-owned attachments
and receipts protect lifecycle, not the complete user/actor conjunction. Identical
calls within a receipt lease may join or return a recorded result; changed content
is rejected. Receipts do not survive process replacement.

Configured encrypted custody can recover a **fresh attachment** using exact
session/incarnation, workload/owner partitions, profile/provider, recovery-reference
and deadline checks. An owner partition is an isolation guard, not human proof.
Credential recovery does not recover a callback, Execute receipt or effect. Sources
at the historical broker pin: [authentication][broker-auth], [custody RPCs][custody],
[protected storage][storage], [Execute receipts][receipts].

The alternate workload-identity proxy kept downstream workload credentials behind
the broker. Its [recorded spike results][track-b] include missing-subject,
path-containment and alternate-SVID bypass limitations. Those are historical findings,
not current-broker claims. Neither that experiment nor an SA-first checkpoint is a
substitute for verified logical-agent identity in the full design.

### Historical P: live admission and profile findings

P's [identity wrapper][wrapper] turns matching current user authentication into
harness-attested facts. Its workload-authenticated [issuer ingress][ingress] carries
resolved definition, tool authority and exact execution facts. Invocation verifies
workload and agent JWT-SVID, consumes matching presenter/owner/actor/expiry,
attachment/incarnation, call ID, argument digest and occurrence, then applies
registered-target checks and Cedar. Changing PR 42 to PR 43 cannot reuse admission;
a valid SVID cannot skip policy. See [one-use admission][admission] and
[Cedar gate][cedar].

P's [real-ToolHive fixtures][fixtures] include an admitted write and denied
credential/backend counters. Their presence is not a fresh test run or Kind proof.
Its broker-owned confidential OAuth client uses authorization code and refresh,
not an integrated user-subject/logical-agent-actor exchange.

The [integration ADR][integration-adr] records P's trusted-attester relationship.
Earlier design handover sections 10/13 instead requested a distinct fresh broker/AS
user assertion from which the broker derives owner. The main design now explicitly
trusts harness-attested authentication and reuses ToolHive's existing subject token;
it does not select that older extra assertion layer. Audience restriction alone
never supplied independence. The remaining authorized account-association mechanism
is still unselected, and local permission provenance does not prove every
subject-authority/consent ceiling of the [acting-as-user conjunction][exchange-adr].

Two source-review findings at **P**, not fixes or live reproductions:

- Ordinary Kubernetes SA tokens are classified as user grants, while issuance
  requires a client-credentials grant. That blocks the ordinary-SA
  [stage B](agent-authority.md#example-flow-serviceaccount-plus-agent-instance-jwt-svid) flow at
  this pin, between [GrantTypeFromClaims](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/engine/session/principal.go)
  and [IssueAttested](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/identityissuer/host.go).
- The [bundle-format gap](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/identityissuer/bundle.go)
  prevents claiming standard SPIFFE bundle interoperability merely because the
  local issuer/verifier agree. The reported fields are `sequence`/`refresh_hint`
  rather than SPIFFE-prefixed fields, with a missing JWT-SVID JWK usage marker.

Recheck these defects against I before claiming either persistence or resolution.
Stage A does not require the logical-agent issuer; C requires new workload support
as well as qualified issuer compatibility.

P custody source navigation (not I's current issuer contract):
[identity client](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/identityissuer/client.go),
[trust configuration](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/cmd/mecak8s/mcp_identity.go),
[issuer ingress][ingress], [gRPC presentation/verification][admission],
[ToolHive target](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/adapter/mcpbroker/toolhive_process.go)
and [OAuth flow](https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/adapter/mcpbroker/auth.go).
The donor [issuer substrate][issuer-adr] and [logical-agent projection][agent-adr]
record the earlier identity slices, not completion of the intended exchange.

### Current I: freshness is checked separately from issuance

At I, the API creates request-local `AuthenticationEvidence`: owner issuer/subject,
`VerifiedAt` (API bearer-verification time, **not human login time**), `ExpiresAt`
(bearer expiry) and random `RequestID`. Verification requires a non-future age of
at most five minutes, expiry after now and a nonempty request ID; broker Execute
repeats these checks. The ID is **not a replay nonce**. The login bearer is neither
persisted nor forwarded. Workload-authenticated TLS/gRPC protects this trusted
harness attestation; it is not independent human proof against a compromised harness.
Sources: [API evidence creation][current-authn], [evidence contract][current-evidence].

Dispatch derives effective execution facts after local permissions and hooks.
I's issuer receives owner, resolved definition, tools and exact-instance hash, but
**not human freshness**: it cannot cap the SVID lifetime at human-token expiry.
Execute separately checks fresh evidence, owner, attachment, workload, SVID, exact
call and Cedar. Its [detached execution context][current-execute] drops incoming
evidence context; the target uses the attachment credential. There is **no integrated
user-subject/logical-agent-actor exchange**. HTTPS issuance and TLS-protected gRPC
invocation are distinct harness-to-broker paths; issuer and admission share
broker-owned admission state.

Exact receipts and JTI tracking remain process-local. Restart must fail closed,
not imply durable single-use protection. Retrieving a started receipt is not a
new dispatch. The main's [interactive flow](agent-authority.md#5-interactive-execution-flow)
is a normative design, not an execution trace of I or W.

### B, W and S: credential continuity is not subject continuity

B already captures a verified `tsid` and preserves encrypted custody across
recovery. Storage encrypts under session/provider/field; it neither maps users nor
rewrites JWTs. B's recovered token uses `mecatl-recovered:v1:ownerPartition`, not the
original canonical ToolHive user subject. Recovering the right credentials therefore
does not establish the required user-and-agent token meaning.

W's exchange omits the upstream link during session construction; the MCP loader
returns on a missing JWT `tsid` before consulting storage. A storage decorator alone
cannot fix a token that never reaches it. W also restricts the SPIFFE actor to the
presenter: distinct logical-agent support needs validation and an authorized
association, not simply removal of that check. S's committed-connection mapping
with revision/provider checks is separate-branch work, not B's behavior.

The [credential-continuity brief](toolhive-credential-continuity-followup.md) owns
the detailed ToolHive task, code navigation, minimal issuance/storage handoff and
test matrix. It supersedes the [SPIFFE brief](toolhive-spiffe-followup.md)'s earlier
new-user-evidence exploration for subject reuse and linkage; the SPIFFE brief
still covers separate logical-agent actor support. Do not duplicate those adapter
details or introduce a new opaque user-evidence platform here.

### B2: enrollment and account continuity to reuse

B2 is the starting point for extending the broker's connection machinery. The
following findings come from source and test inspection at the pinned commit;
the tests were not run during this review.

Mecatl records the initiating principal and checks Connect ownership when ownership
enforcement is enabled. Broker RPCs authenticate the workload instead. Production
`OWNERLESS` partitions attachments by workload and deployment; it does not mean
that ToolHive has no OAuth user, or that all sessions share one account. Each
broker session can establish its own connection baseline. See the
[host Connect path][b2-connect] and [broker partitioning][b2-partitions].

Enrollment binds OAuth state and the retained PKCE verifier to the broker's
logical session and authorization transaction. The callback rejects stale or
replayed state and unexpected selectors, then exchanges the code. Authenticated
observation performs ToolHive discovery before publishing the connection and
catalogue. The broker records an account digest over the configured providers'
verified user, upstream subject, and client identities. Later reauthorization or
recovery rejects a changed account. Explicit disconnect clears the baseline and
invalidates parked work before another connection is selected. Sources:
[callback][b2-callback], [connection publication][b2-publication], and
[account continuity][b2-account].

This establishes transaction correlation and account continuity, not the initial
caller's permission to use that account. If Alice owns the Mecatl session but Bob
completes its genuine first enrollment URL, Bob's authenticated account can become
the baseline while Alice remains the session owner. That outcome follows from the
inspected flow; no dedicated first-connection Alice/Bob test was found. It can be
legitimate account sharing, but the flow does not establish that Alice authorized
that selection. A later changed-account continuation is rejected. The
[production tests][b2-production-tests] cover that continuation check, not proof
that the first browser user equals the initiating Mecatl user.

Preserve B2's references, catalogue checks, credential custody, and host-side
unknown-outcome fence. Its attempt identifier is not a durable idempotency key:
the [occurrence test][b2-occurrences] deliberately demonstrates repeated execution
of the same attempt. Session and account references also do not establish an
executing-run binding. Production has browser authorization-code and refresh
support, but no integrated logical-agent SVID actor exchange or admitted-run
lifecycle. Those are additions to this foundation, not reasons to replace OAuth
or invent another user-token lifecycle.

## Current gaps and foundation work

Complete the identity/broker foundation first. These are requirements to implement
or decisions to select, not claims already satisfied by the snapshots.

### Reusable identity and run-scoped delegation

The intended lifetime model now separates a reusable, short-lived agent credential,
run-scoped delegated access and per-call admission. This is not I's implemented
profile: I binds the agent SVID to an exact invocation and tracks its JTI as
single-use. Move occurrence/replay enforcement into call admission before allowing
identity-credential reuse; do not merely remove the existing checks.

Extend B2's existing session/invocation machinery with an admitted executing-run
association. The first preference is broker-held run context, addressed by a
reference bound to authenticated requests alongside the attachment. Reuse existing
reference machinery where possible; this does not select new RPCs or a token
schema. Obtain the ToolHive user from the broker-held subject token and the actor
from the instance SVID. Preserve the authorized connection association described
below. Bind each actor's own run, retaining parent/root execution links separately
from the instance's creation ancestry.

Authenticate the initiating human when admitting work. Ordinary expiry of that
login bearer does not end the admitted run; completion, cancellation, and configured
budgets do. Renewal must remain within that run's authority, and explicit policy
withdrawal can restrict subsequent calls. Dependent child access must close with
its parent. An unfinished run can recover only when durable state establishes its
binding; restoring a session or credential alone cannot revive ended work.

Use short-lived delegated access tokens, renewable only within an active admitted
run. One run can reuse suitable tokens or obtain resource-specific replacements.
Define cache partitioning, actor-transition checks, lifecycle propagation, and restart
behavior without confusing token refresh with new authority. Recipients that do
not check live run status can still accept an issued bearer until expiry. Preserve
unknown-outcome handling for dispatched calls rather than claiming immediate
revocation or replay safety from the run reference.

### Caller/account association and exchange on B2

Reuse B2's enrollment and verified account baseline. The missing association is
which authenticated Mecatl caller may use that connection for an admitted run.
The provider account need not represent the same natural person. State/PKCE and
account-continuity checks do not establish that permission on first enrollment.

- Select how authenticated run admission establishes the caller's permission to
  use the discovered ToolHive/provider account. Owner-bound completion,
  authenticated account confirmation, and trusted entitlement mapping are
  alternatives, not selected mechanisms. Equal subject strings or emails across
  issuers are insufficient. Preserve the accepted association through recovery
  without replaying or retaining the human's login bearer.
- Extend the workload-authenticated boundary to carry or resolve the trusted
  admission association. B2's injectable owner-partition resolver is not a shipped
  per-human production mode, and partitioning by owner alone would not authorize
  the first browser account. Keep harness-reported authentication distinct from
  facts independently checked by the broker. Reconcile I's per-call freshness
  gate with admission-time authentication; do not make an already-admitted run
  depend on the continued validity of the original login token.
- Integrate the instance SVID and token exchange into B2's production path. Reuse
  the ToolHive auth-code access token as `subject_token`, never an owner label,
  attachment handle, or `tsid`; use the instance credential as `actor_token` and
  authenticate the broker OAuth client separately. B2's
  [client registration][b2-client] currently enables authorization code and refresh.
  Configure the exchange grant, eligible resources/scopes, actor validation, and
  authorized presenter association. An SVID accepted for broker admission is not
  automatically valid at the ToolHive authorization server.
- Preserve or authoritatively resolve canonical ToolHive subject identity across
  credential recovery. B2's [recovered bearer][b2-recovered] uses an owner-partition
  subject and verified token-session linkage; recovering the connection does not
  prove the required delegated token has the original human `sub`. Do not treat
  the account digest as a substitute for canonical identity.
- Resolve the authorized provider credential link from validated subject and
  connection state. User, tenant, provider, connection/revision, and target must
  agree; never accept a caller-selected storage key or external `tsid`. Reuse the
  [credential-continuity brief](toolhive-credential-continuity-followup.md) for
  ToolHive-specific exchange/storage integration rather than designing a second
  OAuth subsystem here.

Qualify the additions through the real host-to-broker path: admission for the
correct caller/account; first enrollment completed by another account; unauthorized
association and changed-account refusal; cross-instance/run reference substitution;
continued admitted work after login-token expiry; completion/cancellation; and
recovery without account substitution or replay of uncertain effects. An intentional
account-sharing case must be distinguishable from an unauthorized association under
the selected mechanism. Retain B2's existing callback and continuity regressions;
their presence is not a test run of the future combination.

Detailed association and authorization mechanisms remain separate design work.
These integration requirements do not select a new user-evidence credential,
consent UI, or mission/mandate system.

### Session-scoped definitions and uniform identity

PR [#2003](https://github.com/stacklok/mecatl/pull/2003) introduces
`CreateSessionRequest.agent_definition_name`, allowing a main session to use a
named `AgentDef`. PR [#2055](https://github.com/stacklok/mecatl/pull/2055) adds the
mecatui command and picker. The architecture review inspected backend commit
`595be9fd0e139e856a3a18632d25cf7f9985815e` and TUI commit
`cda6eee7374c495f71eda5d9cec1875e5f5dddd6`; these are separate from the
identity-integration snapshot.

The backend shares definition resolution while retaining separate root and child
engine construction. Preserve that distinction: a definition-bound root must
retain main-session permission asks, user-context handling and guardrails. Reusing
a definition must not make the root behave like a delegated child.

The inspected implementation persists the selected definition name and authority
ceiling, but does not establish a complete resolved credential identity. Unsupported
restart reconstruction fails closed. It also deliberately excludes definition-bound
roots from the configured broker attachment path; enabling that path requires an
explicit integration contract.

### Required identity integration

**Carry one resolved identity through execution.** Trusted definition resolution
should preserve category, namespace, and exact name, with the session incarnation
identifying the instance. Bind these to the session and pass them into execution
context rather than reconstructing them from role strings or authority provenance.
Keep the built-in `system` definitions `main` and `explorer` distinct; their
namespace assignment remains to be specified.

**Keep configuration, identity and authority separate.** `AgentDef` supplies
configuration; the resolved identity identifies the agent; `Session.Authority`
supplies the effective permissions and ceiling. Do not copy permissions into the
identity value or use the ceiling in place of the invocation’s effective authority.
Identity and authority should be bound through an aggregate-controlled operation
that rejects inconsistent state and rebinding.

**Use delegation state for root-versus-child checks.** Audit existing role and
`Session.Kind` checks before changing them. Where behavior depends only on
delegation, use the authoritative delegation relationship/depth, with zero
representing the root. Do not introduce a second depth counter. Fork and clear
ancestry must not count as delegation. Retain execution-specific distinctions where
team coordination, parallel execution, scheduling or debugging actually require them.

**Preserve definition association across lifecycle operations.** A persisted name
alone does not establish which namespace and source supplied it. Resume/recovery
retains the instance incarnation and pinned definition revision. Peer fork/clear
retains the definition identity but creates a new incarnation and resolves the
current revision. Detailed revision/dependency retention is separate design work;
unsupported recovery must fail explicitly rather than select another definition
or silently update the existing instance.

Choose a recoverable, issuer-verified source identifier for the instance and each
creation-ancestry hop; the readable category/name is insufficient. Source adapters
provide provenance without requiring the engine to understand Git, Kubernetes or
database revision formats. Decide whether associated state or a versioned application
claim carries what a verifier needs. A source-native revision can miss dirty local
content, while a digest cannot reconstruct it; specify coverage and snapshot
retention before relying on either. Keep private repository locations and filenames
in guarded records rather than readable JWTs. Missing optional claims imply nothing,
and claims repeating path fields must agree with them. Identifier, transport, claim
fields and source-specific mapping remain open.

**Integrate named roots with the broker without widening authority.** Define which
broker tools a selected definition may expose, intersect discovery with its bound
ceiling, and support the required attachment and continuation paths. Do not bypass
the PR’s broker exclusion merely by attaching the deployment’s default catalog.

**Keep credential projection at the adapter boundary.** The engine carries the
resolved identity and checked execution facts. The issuer adapter converts these
into its credential representation; it must not assume that every root is
`system/main` or parse diagnostic role strings to discover an agent’s identity.

Verify that the same definition used as a main agent and delegated specialist
retains the same definition reference while producing distinct instance subjects
and run/call bindings. Cover built-in definitions, source/namespace collisions,
fork/clear behavior, definition changes on restore, child narrowing, broker discovery
beyond the ceiling, and preservation of root permission asks and guardrails. Unsupported
execution paths must remain rejected rather than gaining credential issuance
implicitly. The concrete core representation and migration remain to be selected;
these requirements do not mandate constructing every root through an `AgentDef`
or replacing the existing root/child engine builders.

### Integration, deployment and A–D qualification

Reconcile identity integration and broker squash without silently merging their
claims. Resolve/retest credential-profile defects and qualify allow/deny,
target/argument substitution and narrowed-child authority through the real remote
path. A malicious reviewer cannot gain merge permission by spawning a broader
specialist; a separately authorized top-level deployer can act only within all its
ceilings. Coverage is registered protected MCP, not every agent action. Issuer
enablement is tracked by #478.

| Design stage | Delivery position and outstanding work |
|---|---|
| A: workload-only SA | Selected first broker implementation/checkpoint. Narrower than logical-agent admission; never silently substitute SA identity for an actor credential. |
| B: SA + logical agent | Substantially implemented on the identity-integration branch, but needs profile correction/revalidation and deployment qualification. Historical P findings are not proof of current I behavior. |
| C: SPIRE JWT-SVID | Planned, not wired. Implement Workload API acquisition, workload allowlists, credential renewal and platform trust-bundle refresh separately from broker logical-agent keys. |
| D: SPIRE X.509-SVID | Possible later move, not a promised selectable alternative. Design TLS termination, identity propagation, certificate/bundle rotation and connection renewal together, with no silent bearer fallback. |

Whether earlier authentication methods remain supported is unselected. C retains
B's bearer request pattern; stolen bootstrap JWT-SVIDs remain replayable until
expiry even if a subsequently issued access token uses DPoP. D does not itself
sender-bind an OAuth token; the DPoP task below remains separate.

Select the ToolHive release/exchange capabilities and direct-client/proxy topology;
prove intended key/credential isolation and absence of alternate bypasses. The
direct client holds a broker-audience workload token; only a selected proxy would
hide it from the harness. The initial combined broker is one replica with `Recreate`,
process-local active state and no HA claim. A later narrow Unix-socket client proxy
is an open option, not a second policy engine or generic HTTP proxy. Splitting issuer
and invocation into separate deployments requires its own authentication and key
distribution design. Signing algorithm, key-storage backend and any further
structured resource constraints belong in the implementation specification.

### Durable broker evidence and replacement

Make identity provenance, exact operation, policy decision, dispatch and observed
completion/unknown outcome inspectable independently of the harness log and durable
across restart. Define linkage to harness session, run and tool call; existing IDs
alone are not assumed sufficient, and no new correlation identifier is selected.
Credential recovery restores access, not completion evidence; restart interrupts
active work.

```text
known not dispatched -> eligible for a carefully authorized retry
known completed      -> return the durable outcome
possibly dispatched  -> unknown/ambiguous; never retry automatically
```

Qualify this distinction across replacement, including a provider effect completed
before its response was recorded. Reconciliation may use provider idempotency/status
evidence, not a general exactly-once claim. Durable audit retention can precede
replication. Replicated continuity separately needs durable callback claims,
refresh/disconnect fences, routing generations and operation state; adding Redis
or replicas is not that contract and is not required for a useful singleton checkpoint.

## Security follow-on: workload-level DPoP

DPoP is selected **after the foundation**, not implemented by the identity credential.
Implement sender binding for the worker's broker-facing OAuth credential while
retaining logical-agent assertion and exact-call checks. Define issuance, key
custody, rotation/restart, nonce handling and replay-state ownership. Prove rejection
of missing, wrong-key, stale and replayed proofs with no bearer fallback.

[DPoP](https://www.rfc-editor.org/rfc/rfc9449.html) can use one key per worker; subagents
need not be workloads. Verify the actor's association with the authenticated
presenter separately. DPoP covers HTTP method and URI, not MCP arguments/body, and
does not change the logical-agent JWT-SVID profile. Its initial claim is stolen
access-token replay protection, not compromised-harness or per-agent isolation.

Keep keys/tokens/proofs out of model tools, results, logs and environment variables;
expose no general-purpose signer. A process-lifetime key is a minimal custody option,
with fresh token issuance after restart and no cached-token reuse under a new key.
Custody/lifecycle still need an implementation plan. A stolen projected bootstrap
bearer may let an attacker obtain a new token bound to their own key.

**Stronger runtime isolation is deferred.** A shell able to reach worker memory,
files or signer can defeat this protection. A keystore/HSM can prevent extraction,
but a compromised authorized client may still request signatures. Design an enforced
boundary between hostile tool execution and protected broker client; an accessible
sidecar signing socket is insufficient. Independently proving which agent made a
call is a further requirement, not provided by DPoP, a separate signer or separate
IDs/keys held by one compromised process.

## Separate future consent: schedules

The [generic scheduler][scheduler] restores owner and placement, not fresh user
authentication or permission to spend a provider account unattended. The live
identity wrapper does not supply the scheduled journey. Issue #373 covers a separate,
still undesigned authorization, provenance, freshness and revocation contract.

Permission must bind to schedule revision, operation, cadence and limits. A
model-created Schedule call attributed to Alice does not prove she approved those
facts. Changes must not widen the authorized job. Before each manual or timer fire,
revalidate the applicable authority's expiry and revocation, schedule revision,
current policy, agent/integration revisions and operation eligibility before
obtaining fresh execution credentials. Neither path may fall back to a service
identity. No signed schedule grant, reference store or approval mechanism is selected.
Provider offline credentials remain in
broker custody, not as user proof, and saved login bearers are not a solution.

The follow-up handover sketched authenticate owner → resolve agent/operations →
obtain unattended consent → establish provider offline access → store bounded
schedule authority/reference. That is a discussion sketch, not a contract. Offline
consent does not require a second broker OAuth self-client: the confidential
ToolHive client already exists, while unattended permission is an authorization
question. This work does not block the interactive foundation.

## Exploratory extension: task mandates

No task-mandate model is selected. A logical-agent credential establishes actor and
tool ceiling; a mandate could express authority for this task, resources and
conditions; the broker would still enforce its own current policy on the exact call.
For example, a mandate could limit a reviewer to PR 42 even if standing policy
permits other PRs. Neither identity nor the enforcement point decides who may grant
that permission or proves Alice's consent.

One candidate is a signed token carrying Cedar policy in RAR-shaped
`authorization_details`, not merely operation data for broker-local policies. The
broker would verify grant/actor binding and require both carried and local policy
to allow the operation, with neither expanding the other. The credential stays
outside model content. This is an option, not a logical-agent profile change.

Before selecting a model, decide:

- Issuers, authorization/consent for issuance and user/actor/presenter binding.
- Operation/resource semantics and mapping from registered calls, including Cedar
  schema and trusted entity/context inputs.
- Task versus one-operation scope, separately from reuse/single-use semantics;
  narrow policy alone does not make a grant single-use.
- Expiry, revocation and delegation narrowing, including issuance ceilings.

Do not add speculative token fields or interfaces now. Existing acting-as-user,
registered-target, tool-ceiling and exact-call checks remain. Mandates are not a
prerequisite for useful attribution/enforcement, and selecting their format would
not close consent or durable-audit gaps.

## Delivery references

Issue numbers are planning references, not runtime evidence. For shipped behavior,
use [architecture.md](architecture.md); for alternatives, [agent-identity-model.md](agent-identity-model.md)
and [agent-identity-outbound.md](agent-identity-outbound.md). Delivery proofs belong
in acceptance plans, decisions in ADRs and shipped/deferred status in
[PRODUCTION-READINESS.md](design/PRODUCTION-READINESS.md). Historical handovers are
conversation snapshots, not the evolving contract.

## Pinned references

[b2-connect]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/server/session_broker.go#L127-L144
[b2-partitions]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbrokerserver/session_api.go#L15-L64
[b2-callback]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbroker/auth.go#L584-L749
[b2-publication]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbroker/session_api.go#L662-L712
[b2-account]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbroker/toolhive_process.go#L120-L153
[b2-production-tests]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/server/session_broker_production_test.go#L659-L738
[b2-occurrences]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbroker/session_api_occurrence_test.go#L53-L105
[b2-client]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbroker/toolhive_process.go#L678-L717
[b2-recovered]: https://github.com/stacklok/mecatl/blob/0d9467664c7a496b8dda287183f89dedea3ec786/internal/adapter/mcpbroker/toolhive_recovered_credential.go#L30-L94
[current-authn]: https://github.com/stacklok/mecatl/blob/2ee96819939367e5f7c57fd48ead21fca828bd0a/internal/adapter/server/authn.go#L463-L495
[current-evidence]: https://github.com/stacklok/mecatl/blob/2ee96819939367e5f7c57fd48ead21fca828bd0a/engine/port/authentication_evidence.go#L12-L40
[current-execute]: https://github.com/stacklok/mecatl/blob/2ee96819939367e5f7c57fd48ead21fca828bd0a/internal/adapter/mcpbrokergrpc/server_execute.go#L216-L229
[issuer-adr]: https://github.com/stacklok/mecatl/blob/ed319f45cd972de3cb95031b39e57b8eec425789/docs/adr/0303-identity-issuer-substrate.md
[agent-adr]: https://github.com/stacklok/mecatl/blob/ed319f45cd972de3cb95031b39e57b8eec425789/docs/adr/0304-logical-agent-identity-projection.md
[exchange-adr]: https://github.com/stacklok/mecatl/blob/ed319f45cd972de3cb95031b39e57b8eec425789/docs/adr/0305-acting-as-user-exchange.md
[delegation]: https://github.com/stacklok/mecatl/blob/a711451616edf84f50e793c9bcbfb697ef400347/engine/agent/authority_delegation.go
[scheduler]: https://github.com/stacklok/mecatl/blob/a711451616edf84f50e793c9bcbfb697ef400347/internal/app/scheduler_fire.go
[broker-auth]: https://github.com/stacklok/mecatl/blob/53eab6b753869b33c317da49a67295575807ac61/internal/adapter/mcpbrokerserver/authentication.go
[custody]: https://github.com/stacklok/mecatl/blob/53eab6b753869b33c317da49a67295575807ac61/internal/adapter/mcpbrokergrpc/continuity_rpc.go
[storage]: https://github.com/stacklok/mecatl/blob/53eab6b753869b33c317da49a67295575807ac61/internal/adapter/mcpbroker/toolhive_protected_storage.go
[receipts]: https://github.com/stacklok/mecatl/blob/53eab6b753869b33c317da49a67295575807ac61/internal/adapter/mcpbrokergrpc/server_execute.go
[track-b]: https://github.com/stacklok/mecatl/tree/b6dc951c091197bcb4c3ec444a80308d43dc9e5a/docs
[integration-adr]: https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/docs/adr/0306-singleton-identity-mcp-admission.md
[wrapper]: https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/app/mcp_identity_attester.go
[ingress]: https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/adapter/mcpbrokerserver/server.go
[admission]: https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/adapter/mcpbrokergrpc/broker.go
[cedar]: https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/adapter/mcpbrokergrpc/admission.go
[fixtures]: https://github.com/stacklok/mecatl/blob/db5bcd8845f87f05fa0c0efecb2092fd6c5e497c/internal/adapter/server/mcp_broker_live_admission_toolhive_test.go

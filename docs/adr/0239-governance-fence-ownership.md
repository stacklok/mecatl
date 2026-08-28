# ADR 0239 — Canonical untrusted-content fences live in governance

- Status: Accepted
- Date: 2026-08-28
- Scope: Engine fence ownership, public API, and adapter dependency direction
- Supersedes: ADR 0021 and ADR 0105 fence-ownership decisions only
- Superseded by: —

## Context

Model-visible prompts and delegation results share security-sensitive framing rules.
Those rules began in `engine/agent`, where team and guardrail prompt construction first
needed them. Built-in WebFetch and WebSearch later needed the same byte-identical policy,
which forced reference adapters to import the outward application package or duplicate the
matcher. Either choice violates the intended dependency direction or creates multiple
security policies that can drift.

The primitives require only the standard library and no session state. The pre-v1 engine
API can still take a clean breaking change rather than preserving forwarding exports that
would leave two apparent owners indefinitely.

## Decision

Make `engine/governance/fence.go` the single canonical owner of the five public APIs:
`UntrustedFence`, `WriteUntrustedBlock`, `FenceUntrusted`, `NeutraliseFraming`, and
`NeutraliseDelegationResult`.

Remove the former `engine/agent` fence exports without compatibility aliases. Keep only
agent-specific parsing and composition in `engine/agent`, including
`StripLoneCodeFence`. Do not duplicate fence constants, rendering, or matcher policy in
adapters. Fenced prompt bodies must use `WriteUntrustedBlock` or `FenceUntrusted`;
`NeutraliseDelegationResult` is only for model-influenced text embedded in a
harness-composed delegation result.

Keep governance as the stdlib-only, session-free domain leaf. Do not create an
`engine/fence` package now. A second unrelated non-permission primitive placed in
`governance` is the signal to extract a dedicated leaf.

Enforce the resulting direction explicitly: production `engine/adapter/*` code must not
import `engine/agent`. Host adapters remain allowed to import the application package
where their role requires it, including the server relay, tokenizer seams, and modelhook's
shared lone-code-fence parser.

## Consequences

Reference WebFetch and WebSearch adapters can consume one inward, canonical fencing
policy without depending on the agent loop. Prompt builders, delegation renderers, and
host adapters use the same markers and neutralisation behavior.

External pre-v1 users of the former agent exports must update imports to governance. This
is intentionally a clean break and is recorded in the engine API snapshots and changelog;
there is no deprecation window or duplicate compatibility surface.

Governance now contains a security primitive adjacent to permission policy. That is the
smallest dependency-correct home today, but its cohesion must be revisited if another
unrelated primitive arrives.

This supersedes only the ownership claims in ADR 0021 and ADR 0105. Their guardrail and
WebFetch behavior, limits, and security decisions remain frozen and authoritative.

## See also

- [Architecture overview](../architecture.md#1-what-it-is)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
- [ADR 0021 — Guardrails](./0021-guardrails.md)
- [ADR 0105 — Built-in WebFetch](./0105-built-in-webfetch.md)
- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)

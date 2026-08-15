# ADR 0215 — OpenAI subscription with a manual access token

- Status: Accepted
- Date: 2026-08-05
- Scope: OpenAI subscription authentication, provider registration and selection,
  live model inventory, session persistence, and the local composition roots
- Supersedes: none
- Superseded by: none

## Context

mecatl currently reaches OpenAI through the public Responses API using an API key.
An OpenAI subscription is a separate billing and entitlement identity: Codex clients
use a short-lived access token and account identifier against the private
`https://chatgpt.com/backend-api/codex` surface. Treating that credential as an API
key would hide the different trust boundary and could select models that the
subscription does not actually entitle the account to use.

The backend is private and experimental. Its compatibility with a third-party client
was therefore treated as a gate, not an assumption. A one-shot manual probe demonstrated that
the service accepts an honest mecatl originator for model discovery, text generation,
and a function-tool round trip. If the service rejects an unknown originator, work
must stop; mecatl will not claim to be Codex CLI or copy its client-identifying
headers. Automated tests remain offline and consume only sanitized synthetic
fixtures.

This decision extends, but does not supersede, the provider-neutral composition in
[ADR 0016](./0016-multi-provider.md), the stateless Responses translation in
[ADR 0017](./0017-openai-responses-api.md), the storage-free Kubernetes posture in
[ADR 0048](./0048-mecak8s.md), and the intent-driven provider precedent in
[ADR 0064](./0064-toolhive-llm-gateway-provider.md).

## Decision

Add an experimental native provider named `openai-codex`. It is an adjunct to the
existing `openai` provider, not a replacement. Both provider entries reuse the same
`provider/openai.Provider` and the same Responses request/stream translator;
subscription-specific authentication, endpoint headers, and inventory stay in
adapter options and composition. The provider-neutral engine port is not widened.

The first release accepts only an operator-supplied, immutable manual access token
and account identifier in mecatl's existing `auth.yaml` credential file. Reading
`~/.codex/auth.json` is permitted only in the explicit manual compatibility probe; it
is not a product integration or an alternate runtime credential source. Login,
OAuth authorization-code flow, refresh, expiry management, keyring storage, and
per-client credential custody are deferred to a later decision.

Every request identifies itself with the literal `originator: mecatl` and an honest
mecatl user agent. No Codex CLI name, version, device identity, or other impersonation
header is sent. A backend rejection of that originator is the stop condition for this
ADR: leave the record Proposed and do not begin product implementation.

Model inventory for `openai-codex` is entitlement-authoritative and live-only. Before
the first successful `/codex/models` result, a failure publishes no inventory but does
not unregister an explicitly selected provider; a successful empty result publishes
an empty inventory. After a success, the existing process-local last-known-good
inventory may survive any refresh error,
including an authentication or entitlement error, for display and selection through
the shared live-model pipeline. Requests still fail honestly with the current error,
and the public OpenAI API catalog never invents fallback inventory for this distinct
billing identity. The backend's opaque model slug is preserved unchanged.

Provider precedence remains explicit and deterministic. An explicit selector always
wins. A configured deployment default wins over automatic selection. Existing API-key
OpenAI behavior remains unchanged; merely adding subscription credentials does not
silently migrate an existing default. When `openai-codex` is selected, the explicit
selector is persisted with the session, and rehydration must resolve that same provider
or fail honestly rather than fall back to `openai`.

The credential remains a plaintext-file secret under the existing `auth.yaml` warning
and file-permission contract. It must not enter logs, diagnostics, command-runner
environments, proto messages, session snapshots, URLs, or command arguments. The
implementation must also account for the same-UID boundary: filesystem mode `0600`
does not isolate a long-lived daemon from other processes running as that user.

`mecak8s` excludes only the local `openai-codex.oauth` credential in this first
release. Its existing `auth.yaml` API-key entries remain supported; adding local
subscription OAuth custody to its storage-free, managed-service posture would conflict
with ADR 0048. A future Kubernetes integration may accept an explicitly mounted Secret
or external secret provider under a separate deployment decision.

The passing probe emitted three allowlist-sanitized SSE fixtures for offline translator
and replay work. Every identifier, function name/argument, and text value in them is a
fixed synthetic sentinel; no provider value is retained:

- `provider/openai/testdata/subscription_compatibility_text.sse`
- `provider/openai/testdata/subscription_compatibility_tool_call.sse`
- `provider/openai/testdata/subscription_compatibility_tool_continuation.sse`

Redirects are refused before every secret-bearing request. The probe's offline error
evidence pins 401/403 to authentication-or-originator rejection and 429 to quota/rate
limiting for both endpoints; it does not induce those failures against the live service.

## Compatibility gate

The 2026-08-05 probe must establish all of the following before this ADR can become
Accepted:

1. `GET /backend-api/codex/models` accepts `originator: mecatl` and returns a model
   envelope containing opaque slugs and display/visibility metadata.
2. `POST /backend-api/codex/responses` accepts a minimal stateless text/tool request
   through the same honest originator.
3. A function-call output can be replayed with the returned output items, including
   opaque item IDs where present, and reaches a completed turn.
4. The stream exposes the Responses item/event shapes needed by the shared translator,
   including phase and usage presence. Any additional actionable event is listed in
   the sanitized record before adapter work begins.

Failure of any item leaves this ADR Proposed and stops the implementation. Passing all
items permits changing only the status line to Accepted.

## Consequences

Operators can use subscription entitlements without duplicating the OpenAI provider
or contaminating the neutral request port. Live-only discovery prevents a public API
catalog from making false entitlement claims, and persisted explicit selectors keep
session identity stable.

The feature depends on an undocumented backend that may change or revoke third-party
access without notice. Manual tokens expire and require operator replacement. The
plaintext same-UID risk is real, and the first release deliberately provides neither
refresh nor managed secret custody. Kubernetes users do not receive this credential
path yet.

## See also

- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)
- [OpenAI Codex repository](https://github.com/openai/codex)
- [OpenAI authentication overview](https://platform.openai.com/docs/api-reference/authentication)

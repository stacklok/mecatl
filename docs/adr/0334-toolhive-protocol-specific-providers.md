# ADR 0334 — Protocol-specific ToolHive gateway providers

- Status: Proposed
- Date: 2026-09-10
- Scope: ToolHive LLM registry composition, native Anthropic routing, shared direct-mode authentication, live model inventory, provider status, and mecatui provider classification
- Supersedes: ADR 0064's D8 exclusion of an Anthropic-protocol gateway path; ADR 0102's token-source/authenticated-transport construction ownership only
- Superseded by: none

## Context

The ToolHive AI gateway exposes two catalogs and inference protocols for the same configured
gateway identity. `GET /v1/models` and `POST /v1/responses` are OpenAI-compatible, while
`GET /anthropic/v1/models` and `POST /anthropic/v1/messages` are native Anthropic. Mecatl
currently registers only the wire-stable `toolhive` provider and routes every selected model
through the OpenAI Responses adapter. Native-only Anthropic models are therefore neither
advertised nor usable.

Mecatl already has a native Anthropic Messages adapter and rich Anthropic lister. Combining both
catalogs under `toolhive` would be incorrect: the provider ID selects the adapter, so a native
model merged into that catalog would still execute through `/v1/responses`. The protocols need
separate provider identities even though they share detection, routing mode, and credentials.

## Decision

Register two intent-driven providers from one ToolHive configuration:

- `toolhive` retains its current OpenAI Responses behavior and precedence.
- `toolhive-anthropic` uses `provider/anthropic` and `anthropic.NewLister` exclusively.

The provider ID is stable and protocol-specific. A model listed by `toolhive-anthropic` always
uses Anthropic Messages wire format; it is never merged into `toolhive` or sent to
`/v1/responses`. Reserve the new ID against operator-defined provider collisions.

Keep the existing `toolhive` OpenAI base URL byte-for-byte. Derive only the new Anthropic base with
`net/url`: in proxy mode, replace a terminal path segment exactly equal to `v1` with `anthropic`,
or append `anthropic` when no terminal `v1` exists; in direct mode, append `anthropic` to the
configured `gateway_url`. Preserve any path prefix and normalize trailing slashes. Userinfo,
query, and fragment components are not copied into the derived Anthropic base or diagnostics; no
API base uses them as credentials. The Anthropic SDK then appends `/v1/models` or `/v1/messages`.
Do not construct paths with raw string concatenation.

In direct mode, construct the ToolHive OIDC token source and authenticated HTTP client once and
share them across both protocol entries and both listing/inference paths. On every request, clone
the request, delete `Authorization` and `x-api-key`, obtain a fresh bearer from the token source,
and set the authoritative `Authorization: Bearer <token>`. Refuse redirects. Never log or persist
the token, and retain the existing HTTPS requirement plus loopback exception. Both SDK adapters
continue suppressing ambient credential discovery.

In proxy mode, both entries target the loopback ToolHive proxy. The existing OpenAI entry and its
placeholder bearer remain byte-identical. For native Anthropic, an auth-normalizing transport
deletes the SDK's placeholder `x-api-key` and any `Authorization`, then sets the non-secret
placeholder as `Authorization: Bearer thv-proxy` only for the loopback hop. The ToolHive proxy
strips that header before injecting its OIDC bearer, so neither an `x-api-key` nor the placeholder
reaches the upstream gateway. Redirect refusal still covers listing and inference.

Keep live inventory, last-known-good data, and status keyed by provider ID. Fetch both ToolHive
protocol entries independently and concurrently under each existing family-wide bound: 1.5 seconds
at Build, 10 seconds for background refresh, and 2 seconds for on-demand stale refresh. Publish
results deterministically; one slow endpoint neither doubles a bound nor prevents the other outcome
from being recorded. Map `toolhive-anthropic` to the Anthropic metadata namespace for
context/output limits and capability fallbacks without advertising models absent from the gateway's
native catalog. Keep `toolhive` first among intent-driven providers, so adding the sibling never
changes the zero-selector provider or model. An honest empty catalog is fatal only when that
protocol entry is the resolved default; a transport/auth failure of the default remains non-fatal,
and any non-default sibling failure is non-fatal.

Keep status rows, counts, and last-known-good snapshots separate. Remediation is routing-aware:
proxy-unreachable instructs the operator to start the ToolHive proxy; direct-unreachable instructs
them to check configured gateway connectivity or use proxy mode; unauthorized instructs them to
re-authenticate; empty retains the platform-admin/setup guidance.

Treat both IDs as ToolHive gateway providers in mecatui disclosure and catalog provenance. Keep
both rows visible in the picker and status inventory, but suppress “another gateway available”
footer/welcome notices when either member of the same ToolHive family is already active. No
protobuf shape, CLI flag, configuration key, event, or snapshot migration is added; existing open
provider-ID fields carry the new value.

## Consequences

Native-only Anthropic gateway models become selectable and execute through their correct protocol,
while existing ToolHive OpenAI discovery, inference, defaults, and last-known-good behavior remain
unchanged. One login and token-refresh flow serves both protocol surfaces in direct mode.

The registry now owns a two-entry ToolHive family and must keep protocol URL derivation, concurrent
fetching, routing-aware status hints, and UI classification aligned. The shared deadlines and
deterministic publication need explicit tests. Clients that filter provider
IDs rather than consuming the advertised inventory need a follow-up to display the new provider.

## See also

- [ToolHive native Anthropic acceptance plan](../acceptance/toolhive-native-anthropic.md)
- [ADR 0064 — ToolHive LLM gateway provider](./0064-toolhive-llm-gateway-provider.md)
- [ADR 0102 — ToolHive direct mode](./0102-toolhive-direct-mode.md)
- [Provider architecture](../architecture/providers.md)

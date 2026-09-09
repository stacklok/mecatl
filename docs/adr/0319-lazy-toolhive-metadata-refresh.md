# ADR 0319 — Lazy ToolHive grants refresh declared metadata

- Status: Proposed
- Date: 2026-09-09
- Scope: session-scoped MCP broker protected-tool admission
- Supersedes: ADR 0310's pre-prompt-only authenticated-discovery clause
- Superseded by: None

## Context

ADR 0310 publishes static protected-tool declarations so their first call can start
ToolHive's bundle authorization. It also leaves those declarations unchanged after
the lazy grant and reserves authenticated discovery for pre-prompt enrollment.
Consequently, a session that uses the normal lazy path keeps shared placeholder
metadata even after ToolHive has established session-specific authority. Descriptions,
schemas, and read-only hints can therefore remain stale or identify a different
operator context.

The lazy transaction already authorizes ToolHive's complete configured protected
backend bundle. It is not a per-backend grant. Lazy admission must nevertheless keep
ADR 0310's narrower declared-tool surface; only explicit pre-prompt enrollment may
publish previously undeclared tools.

## Decision

After the exact lazy bundle authorization succeeds, query every configured protected
backend before resuming the parked tool call. For names with trusted static
declarations, authenticated discovery controls membership, description, input schema,
and `ReadOnly`: a matching live definition replaces the placeholder and an omitted
declaration disappears. Authenticated tools without declarations remain hidden.

Validate matching live definitions through the same qualified-name, UTF-8, size,
JSON-object-schema, private-material, and collision boundary used by complete catalogue
enrollment. Publish the declared-only result atomically after all backend queries and
lifecycle checks succeed, then rebuild the parked session engine from that exact
snapshot. A discovery, admission, or lifecycle failure before publication retains the previous
catalogue and restores or settles the claimed continuation through the existing compensation path.
An engine-build or registration failure leaves the prior registered engine authoritative and uses
the same claim compensation; a retry rebuilds from the already-published exact snapshot.

This lazy publication is keyed by the exact external authorization and does not invent
a workspace-enrollment reference. Pre-prompt `/tools-connect` remains the independent
complete-catalogue path and may publish authenticated tools that were not declared.
Neither successful path adds a later refresh cadence, token-refresh hook, persistence
format, or public control.

## Consequences

Static values are placeholders on both authorization paths rather than permanent
metadata pins. Sessions using lazy authorization see authenticated metadata for their
declared surface without exposing additional tools. The authenticated `ReadOnly` hint
therefore controls plan-mode visibility and dispatch classification after the grant;
configured permission rules remain independent and deny-dominant.

Lazy completion now performs authenticated discovery before the original call resumes,
so discovery failure prevents that continuation instead of executing with stale
metadata. The attachment and session engine both change only while the run is parked
for authorization.

## See also

- [ADR 0310 — Lazy ToolHive authorization for statically declared protected tools](./0310-lazy-toolhive-static-tools.md)
- [Live authenticated MCP metadata acceptance plan](../acceptance/authenticated-mcp-metadata-replaces-static-standins.md)
- [Architecture guide](../architecture.md)

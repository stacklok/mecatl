# Session MCP authorization reconstruction review ledger

- Review fixed point: `5ed0e242f9a7c89e8897297b36512e4ece4fd7bd`
- Post-review reconciliation: `935f74713d351617bf41e0c578443454b153dd49`
- Comparison base: `e49d183d0`
- Panel: 13 read-only `gpt-5.6-terra` reviews
- Scope: 31 commits, 136 files, +23,225/-4,101

This is the durable work queue for the reconstructed branch. `known` means the
required behavior and implementation direction are understood; it does not
mean the fix is small. `decision` means implementation must wait for an explicit
contract, product, or architecture choice.

Status vocabulary: `open`, `in_progress`, `fixed`, `accepted`, `superseded`.

## Critical

| ID | Knowledge | Status | Finding | Primary locations |
|---|---|---|---|---|
| C-K1 | known | fixed | Server-only TLS is incorrectly accepted as verified caller identity for broker controls. Require token/OIDC or verified mTLS client identity. | `cmd/mecak8s/serve.go` (`validateBrokerControlOwnership`) |
| C-K2 | known | fixed | `mecated` resolves explicit broker authority canonically and atomically mounts the complete broker handler bundle on its fully populated HTTP mux after all-route, standard-method collision preflight. | `cmd/mecated/main.go`; `internal/adapter/mcpbroker/handlers.go`; `cmd/mecated/mcp_broker_command_root_test.go` |
| C-D1 | decision | fixed | Production workspace enrollment is implemented by the concrete broker `Attachment`, backed by its owning ToolHive `Process`: begin/observe/cancel reuse broker authorization state, authenticated observation freezes the catalogue, and composition advertises the capability only when the process requires it. | `internal/adapter/mcpbroker/workspace_enrollment.go`; `internal/adapter/mcpbroker/toolhive_process.go`; `internal/app/build.go`; `internal/adapter/server/workspace_enrollment.go` |

## High

| ID | Knowledge | Status | Finding | Primary locations |
|---|---|---|---|---|
| H-K1 | known | fixed | `mecak8s` defaults configured operator MCP profiles to broker authority, preserves legacy `--mcp-server` as global, and the Helm zero-server render explicitly selects global without constructing broker resources. | `cmd/mecak8s/flags.go`; `internal/app/build.go`; `deploy/helm/mecak8s/templates/` |
| H-K2 | known | fixed | `mecatui` and `mecatequi` retain legacy MCP profile loading while routing operator MCP configuration through the canonical authority resolver; mecatui supports broker authority and mecatequi rejects broker mode explicitly. | `cmd/mecatui/main.go`; `cmd/mecatui/main_test.go`; `cmd/mecatequi/flags.go`; `cmd/mecatequi/mcp_summary_test.go` |
| H-K3 | known | open | HTTP authorization continuations remain bound to the control request context. | `internal/adapter/server/http.go` (`relayMCPAuthorizationControlSSE`) |
| H-K4 | known | open | Initial gRPC control-status send failure does not drain and finish the registered continuation. | `internal/adapter/server/grpc.go` (`relayMCPAuthorizationControl`) |
| H-K5 | known | open | Non-EOF control closure/send failure still owns and cancels the resumed run. | `internal/adapter/server/grpc.go`; `internal/adapter/server/http.go` |
| H-K6 | known | open | Broker ToolHive construction drops the configured OAuth network policy. | `internal/app/mcp_broker_toolhive.go`; `internal/adapter/mcpbroker/toolhive_construction.go` |
| H-K7 | known | fixed | `HandlerBundle.Mount` now preflights every fixed/callback route across standard methods before any registration; command-root tests prove method-qualified collisions leave no partial mount. | `internal/adapter/mcpbroker/handlers.go`; `cmd/mecated/mcp_broker_command_root_test.go` |
| H-K8 | known | fixed | Enrollment observation and cancellation require the returned broker ref to equal the exact pending ref (ID, required-service count, and instant); a valid result for another enrollment cannot mutate the aggregate. | `internal/adapter/server/workspace_enrollment.go` |
| H-K9 | known | open | Executable tools and persisted authority are derived from different catalogue values. | `internal/adapter/server/workspace_enrollment.go` |
| H-K10 | known | open | Restart can leave pending enrollment permanently wedged. | `internal/adapter/server/workspace_enrollment.go`; `internal/adapter/server/mcp_broker.go` |
| H-K11 | known | open | Enrollment compensation removes cancellation without adding a bounded cleanup timeout. | `internal/adapter/server/workspace_enrollment.go` |
| H-K12 | known | open | TUI workspace enrollment is a forced startup modal rather than the later on-demand `/tools-connect` flow. | `cmd/mecatui/ui/workspace_enrollment.go`; `cmd/mecatui/ui/update.go` |
| H-K13 | known | open | Browser completion requires manual recheck; ID/generation-pinned background polling is missing. | `cmd/mecatui/ui/mcp_authorization.go`; `cmd/mecatui/ui/workspace_enrollment.go` |
| H-K14 | known | open | Workspace enrollment has gRPC but no HTTP control parity. | `internal/adapter/server/http.go` |
| H-K15 | known | open | Chart-owned permission configuration can silently override operator `--permission-config`. | `deploy/helm/mecak8s/templates/deployment.yaml` |
| H-K16 | known | open | Living architecture still says public controls are later work. | `docs/architecture.md` |
| H-K17 | known | open | Broker deletion can succeed before durable session deletion fails, leaving an unrecoverable stored session. | `internal/adapter/server/service.go` |
| H-K18 | known | fixed | External-authorization lifecycle events are durable debugger evidence only; both ordinary run relays and live subscriptions now reject `authorization.required` and `authorization.resolved` through the shared public-event filter. | `internal/adapter/server/grpc.go`; `internal/adapter/server/service.go` |
| H-D1 | decision | open | Multiple OAuth upstreams are rejected. Define bundle identity, anchor selection, callback correlation, and grant scope. | `internal/cliconfig/mcp_authority.go`; `internal/adapter/mcpbroker/toolhive_construction.go` |
| H-D2 | decision | open | Recent lazy static protected-tool behavior conflicts with ADR 0291. Choose and document the authoritative contract. | source ADR 0291; `internal/adapter/mcpbroker/toolhive_construction.go` |
| H-D3 | decision | resolved | Failed preparation or registration restores the exact claimed continuation through `Session.RestoreAuthorizationClaim`, persists `authorizing`, and leaves the existing expiry timer armed; the claim is consumed only after the prepared run is registered. | `engine/session/authorization.go`; `internal/adapter/server/mcp_authorization.go` |
| H-D4 | decision | open | A nominally remote-ready broker boundary returns executable `tool.Tool` values. Decide local-wrapper versus data-oriented remote protocol. | `internal/mcpbroker/broker.go` |
| H-D5 | decision | open | `app.Build` hard-codes and exports the concrete in-process broker. Define remote broker injection/ownership. | `internal/app/build.go` |
| H-D6 | decision | open | Ambiguous `Attachment.Commit` failure has no recoverable creation operation for a remote broker. | `internal/adapter/server/service.go`; `internal/mcpbroker/broker.go` |
| H-D7 | decision | open | Helm defaults to two replicas while broker state is process-local. Choose single replica, affinity limitation, or durable broker requirement. | `deploy/helm/mecak8s/values.yaml` |
| H-D8 | decision | open | No frozen ADR covers the final state, ownership, API, and restart decisions. Write it after the preceding contract decisions settle. | `docs/adr/` |

## Medium

| ID | Knowledge | Status | Finding | Primary locations |
|---|---|---|---|---|
| M-K1 | known | open | Permit a valid root-path callback while retaining the remaining URL restrictions. | `internal/cliconfig/mcp_authority.go` |
| M-K2 | known | open | Reject mixed programmatic `MCPServers` and broker authority. | `internal/app/build.go` |
| M-K3 | known | fixed | Continuation preparation accepts only constructor-validated `session.AuthorizationResolution` values and returns an error for zero, pending, or unknown outcomes before constructing a run or mutating the session. | `engine/session/authorization.go`; `engine/agent/loop.go`; `internal/adapter/server/mcp_authorization.go` |
| M-K4 | known | open | Enrolled tool sets lack count and aggregate-byte bounds. | `engine/session/workspace_enrollment.go` |
| M-K5 | known | open | Restored parked authorizations do not regain deterministic expiry settlement. | `internal/adapter/server/service.go` |
| M-K6 | known | open | gRPC control send errors are drained but returned as nil. | `internal/adapter/server/grpc.go` |
| M-K7 | known | open | `request_refresh_token` is dropped before ToolHive construction. | `internal/app/mcp_broker_toolhive.go` |
| M-K8 | known | open | Workspace-enrollment browser-launch failures are uncorrelated and silently discarded. | `cmd/mecatui/ui/workspace_enrollment.go` |
| M-K9 | known | open | Enrollment errors collapse authoritative causes into one generic message. | `cmd/mecatui/ui/workspace_enrollment.go` |
| M-K10 | known | open | Authorization control readers use the application context rather than the control-generation context. | `cmd/mecatui/ui/mcp_authorization.go` |
| M-K11 | known | open | Typed-prompt recovery and exact-once text-only resubmission are absent. | `cmd/mecatui/ui/update.go` |
| M-K12 | known | open | Helm values/schema cannot express broker authority. | `deploy/helm/mecak8s/values.schema.json` |
| M-K13 | known | open | Public `user-docs/` do not cover the new operator/API/TUI surface. | `user-docs/` |
| M-K14 | known | open | Three server paths independently reconcile `CancelOutcome`; extract one private helper. | `internal/adapter/server/mcp_authorization.go` |
| M-K15 | known | fixed | Process-external session bindings use the named opaque `session.ExternalBinding` type end-to-end across the aggregate, snapshot, broker attachment contract, implementation, and server reattachment checks; the distinct per-authorization `AuthorizationBinding` remains non-interchangeable. | `engine/session/session.go`; `engine/adapter/sessnap/sessnap.go`; `internal/mcpbroker/broker.go`; `internal/adapter/mcpbroker/runtime.go`; `internal/adapter/server/mcp_broker.go` |
| M-D1 | decision | open | Decide whether `mcpauthority` is intentionally a concrete composition DTO or a neutral boundary. | `internal/adapter/mcpauthority/authority.go` |
| M-D2 | decision | open | Process-local broker state cannot reattach after restart. Keep deterministic settlement unless durable/remote scope is selected. | `internal/adapter/mcpbroker/runtime.go` |
| M-D3 | decision | open | Public enrollment proto/client controls precede authoritative server behavior in the patch series. Reorder or accept the reviewability trade-off. | commits `09af052b5`, `02dfeb853` |

## Low

| ID | Knowledge | Status | Finding | Primary locations |
|---|---|---|---|---|
| L-K1 | known | open | Align gRPC control error-return behavior with the Converse relay; covered by M-K6. | `internal/adapter/server/grpc.go` |
| L-D1 | decision | open | Define whether “frozen catalogue” promises immutable metadata/membership only or immutable executable behavior. | `internal/mcpbroker/broker.go` |

## Excluded panel reports

- The reported generic OAuth2 pointer-decoding defect was a false positive;
  `newPermconfigNodePointer(&u.OAuth2)` is already present.
- Process-local broker restart loss is not treated as a hidden regression under
  the accepted in-process-only scope; deterministic settlement remains M-D2.
- The library-reuse pass found no actionable reductions.
- Apparent duplication was rejected except for M-K14.

# MCP OAuth login timeout diagnostics — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this reliability and observability correction changes no public API, configuration, persistence format, or authority model.
**Decision record:** None — it applies the existing hardened-transport and diagnostics seams to the explicit local OAuth login path.
**Phase:** MCP OAuth reliability
**Status:** proposed, 2026-09-15. The operator approved the complete contract below for in-place Combined delivery.
**Delivery:** Combined. One compact, local login-path task changes no gRPC/protobuf, external Go API, tool schema, CLI/config, events/persistence, or security/authority contract; separate plan review adds no value after the operator's direct decision.
**Expected tasks:** 1
**Combined rationale:** The exact timeout hierarchy, direct-login-only scope, and per-attempt diagnostic policy are decided below; implementation is one cohesive transport/context plumbing task with offline coverage.
**Issue:** none — scope supplied for acceptance-contract review.
**Plan PR:** none — Combined delivery uses the sole implementation candidate.
**Approved baseline:** current branch baseline at plan approval

`mecated mcp login SERVER` must let an operator complete an OAuth browser callback without the MCP handshake's machine deadline cancelling that interaction. Once authorization returns, the authenticated initialize and initial listing remain bounded by the existing 30-second `ServerConfig.Timeout` and every OAuth HTTP request remains independently bounded. A direct OAuth resource request whose response headers arrive after the former 10-second ceiling but before its 30-second whole-request limit must succeed. A failed hardened transport attempt must produce one redacted warning before existing sanitized login error handling reports the failure.

## Human decisions

- [x] Interactive and machine deadlines — Decision: the explicit `mecated mcp login` path uses the caller context for controller setup and OAuth browser/callback authorization, with no additional login-wide deadline. Its initial network exchange has the existing 30-second MCP deadline; that deadline pauses while the browser/callback is pending and a fresh 30-second deadline starts after authorization completes for the SDK retry, initialize, and listing. Caller cancellation and SIGINT/SIGTERM remain authoritative. Non-login `mcp.Connect` callers retain their existing deadline behavior.
- [x] Timeout scope — Decision: change only `newOAuthHTTPClient`, which serves direct OAuth MCP resource traffic. Do not change `NewHardenedOAuthTokenClient`, whose broker use is unrelated to this incident.
- [x] Warning cardinality — Decision: emit one warning per failed `oauthHTTPTransport.RoundTrip`, including every SDK retry. No operation-level deduplication is added.

## Interface contract

- **gRPC / protobuf:** None — this local command and root-internal adapter work does not alter a service method, message, field, or generated contract.
- **Exported Go APIs / interfaces:** None — no external-consumer or `engine/` API changes. Root-internal implementation may add nil-safe diagnostics plumbing to `OAuthOptions` and `MCPLoginOptions` and private context-control seams.
- **Tool schemas:** None — OAuth login remains a host-authorized CLI operation, never an agent-callable tool.
- **CLI / config:** None — command grammar, flags, stdout success/no-browser behavior, and configuration keys are unchanged. Existing command stderr is threaded into `runMCPLogin` to construct its diagnostic sink; no new logging flag is added.
- **Events / persistence:** None — no session/event or OAuth/DCR credential-record format changes. Warnings are transient operator stderr output only.
- **Security / authority:** None — the existing explicit host-authorized browser-login boundary and hardened no-proxy, exact-origin, DNS-pinned, TLS, redirect, and credential-egress protections remain. Diagnostics use `redactDiagnostics`/`RedactURL`/`RedactError`; request bodies, headers, query values, authorization URLs, codes, tokens, and credentials never enter output.
- **Compatibility / migration:** None — existing profiles, DCR/preregistered/CIMD behavior, non-login MCP connections, and persisted state remain compatible. No migration is needed.

## In scope — 1 scenario, in implementation order

### Scenario 1 — interactive authorization survives while authenticated connection stays bounded

`mcp.Connect` currently creates one 30-second context before building the OAuth controller and passes it into the SDK connection. The SDK can invoke the controller's presenter while connecting, so a user completing the browser flow inherits that machine timeout. The combined correction separates the interactive login phase from the post-callback machine handshake in the explicit local login path; it also raises only the direct OAuth resource client's response-header timeout from ten to thirty seconds. The MCP adapter's diagnostics entry point already decorates a sink so URL/error attributes are redacted ([`internal/adapter/mcp/mcp.go`](../../internal/adapter/mcp/mcp.go)); this behavior must remain the single diagnostic safety boundary, consistent with the [diagnostics invariant](../../AGENTS.md).

**Acceptance:**
- AC1.1: An explicit local MCP login gives its initial network exchange a 30-second deadline, pauses that deadline while awaiting the browser/callback until its caller cancels, then starts a fresh 30-second bounded authenticated MCP retry, initialize, and initial-listing phase after callback completion.
  - verify: `TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_InteractiveAuthorizationSeparatesConnectDeadline`
- AC1.2: The direct OAuth resource HTTP client has a 30-second response-header timeout and retains its 30-second whole-request, five-second dial, and five-second TLS-handshake limits; `NewHardenedOAuthTokenClient` remains unchanged.
  - verify: `TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_DirectResourceTimeoutPolicy`
- AC1.3: A test-owned local resource server whose headers arrive between scaled equivalents of the old and new header thresholds succeeds under the revised direct resource client and fails under the old threshold.
  - verify: `TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_DelayedResourceHeadersSucceed`
- AC1.4: Every failed direct OAuth transport `RoundTrip` emits one `port.LevelWarn` record before its existing sanitized failure returns, with method, redacted URL, elapsed duration, and redacted underlying error; success emits no failure warning.
  - verify: `TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_RoundTripFailureDiagnostic`
- AC1.5: `mecated mcp login` supplies a default stderr `slogdiag` sink and prints the warning before its existing final error; request/query/body/header and wrapped-URL error canaries do not appear, while `--no-browser` authorization output remains stdout-only.
  - verify: `TestMCPOAuthLoginTimeoutDiagnostics_Scenario1_CLIAndDiagnosticsRedaction`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| New OAuth timeout flags or YAML | A confirmed per-deployment tuning need | Defaults and command grammar remain unchanged. |
| MCP broker token-client timeout changes | Separate broker incident | `NewHardenedOAuthTokenClient` is deliberately unchanged. |
| Changing third-party SDK retry behavior or its final collapsed error | Upstream SDK maintenance | Mecatl observes each raw transport failure before wrapping. |
| DCR registration/discovery, token persistence, or gateway latency investigation | Existing contracts / gateway operator | The correction covers direct login timing and diagnostics only. |

## Definition of done

1. The named offline scenario tests use test-owned local servers, controlled timeouts, and injected diagnostic recorders; they contact no real OAuth/MCP service.
2. `task lint`, `task test`, `task docs`, and `task api:check` pass.
3. `go run ./cmd/mecademo` remains green.
4. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- No operation-level deadline is added around the human callback; the invoking context remains the cancellation and shutdown control. Each network operation after authorization remains independently bounded.
- A third-party SDK retry may produce multiple warnings, by decision. The command preserves its existing final sanitized error rather than aggregating retry causes.

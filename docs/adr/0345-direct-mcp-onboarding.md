# ADR 0345 — Host-local direct MCP onboarding and credential custody

- Status: Proposed
- Date: 2026-09-16
- Scope: direct/global streaming-HTTP MCP profile lifecycle, issuer bootstrap, local credential-key custody, and host-administration boundary
- Supersedes: ADR 0113 only for the login-only host lifecycle and environment-key-only local setup; ADR 0325 only for requiring an exact issuer before direct-DCR discovery and for adding removal lifecycle states
- Superseded by: none

## Context

Direct OAuth MCP already has strict operator profiles, a hardened issuer-aware transport, an encrypted CAS credential store, host-owned browser login, and durable DCR registration and grant records. Setup still requires an operator to author nested YAML, know the authorization-server issuer, generate and repeatedly export an encryption key, create exact filesystem permissions, and diagnose configuration through downstream failures. Only `mecated mcp login` exists, so listing and removing profiles still require YAML editing.

The public daemon API exposes session-scoped client MCP and inspection of sources loaded into the current process. It does not expose deployment administration. Session ownership does not authorize changes to operator settings, outbound OAuth trust, or host credentials. The TypeScript SDK can spawn a local daemon, but it has no independent authority or implementation for global MCP lifecycle management.

The common workstation experience should not expose a wrapping key. Mecatl can provide that experience without weakening headless deployments, changing an established backend after failure, or merging host administration into session APIs.

## Decision

1. Make `mecated mcp add`, `list`, `login`, and `remove` the canonical host-local lifecycle for operator-configured direct MCP servers. Reusable root-internal operations own the behavior. Add no lifecycle mutation RPC to `HarnessService`; session ownership and broker authorization grant no deployment-administration authority.
2. Start onboarding from one canonical query-free HTTPS MCP resource. Before configuration exists, use a credential-free hardened bootstrap that parses Bearer challenges and exact-resource RFC 9728 metadata under a stricter same-resource-origin policy. Require one acceptable advertised issuer, validate it through the MCP RFC 8414/OIDC metadata sequence, bind it to callback state/PKCE, and only then construct the existing issuer-aware transport. For a pathful resource, omit the MCP origin-root metadata fallback because adopting its RFC 9728 root resource would change the frozen OAuth resource; report this standards-interoperability restriction as unsupported policy. Metadata never grants unbounded network authority.
3. Select and pin one local wrapping-key backend before OAuth. First delivery supports macOS Keychain and Linux Secret Service, with an affirmatively selected owner-only file key when Linux has no service. A selected but unusable keyring never falls back. Existing environment custody remains external and never selects a local key. Legacy `key_env` profiles retain the existing `mecatl-mcp-oauth` namespace; native `key:` profiles use `mecatl-mcp-oauth-native/v1` and a marker binding that namespace, backend, and key locator. Commands load only the selected profile and never enumerate, copy, decrypt, or migrate legacy records. An unmarked native keyring account or file key is ambiguous and fails closed: it is never adopted, overwritten, deleted, or migrated. Credential records remain encrypted through the existing injected-key `credentialstore.Store`; key custody stays outside that adapter.
4. Treat operator settings as desired host state and the process-wide MCP manager as one loaded daemon generation. Mutating commands have one exact writable target, reject conflicting layered MCP ownership, validate and replace narrowly and atomically, and never claim an existing daemon loaded the change. Hot reload and online generation comparison remain separate work.
5. Extend direct DCR with crash-recoverable local removal. Genuine absent lifecycle state permits profile-only removal. Ready state transitions through `removing` to a durable `removed` tombstone while the current-generation grant and profile are deleted. Pending, uncertain, corrupt, mismatched, read-only, and key-unavailable state remains preserved and blocks removal. No force-forget, wrapping-key deletion, or upstream revocation is added; re-add creates a fresh upstream registration.
6. Keep current API and SDK meanings. `CreateSessionRequest.mcp_servers` remains transient session MCP, and `ListMcpSources` remains running-generation inspection. V1 adds no SDK helper or machine-output contract; local SDK users run the CLI before `spawn()`, and remote SDK callers cannot administer host MCP configuration.

Exact CLI spelling, discovery matrices, backend metadata, filesystem modes, DCR transition proofs, and progress behavior live in the linked acceptance plan rather than this durable boundary record.

## Consequences

A workstation operator can configure the currently supported direct-DCR profile from its MCP URL, consent in a browser, and reuse encrypted credentials without manually handling a key or writing YAML. The lifecycle stays on the host that owns its settings and credentials.

Issuer discovery adds a credential-free pre-configuration network phase. Its same-resource-origin metadata and exact-issuer-origin endpoint policies intentionally reject some standards-conforming deployments. Errors must identify those as unsupported policy, not malformed OAuth.

Native keyring support adds platform behavior and a process-lifetime dependency already used by other root-module credential consumers. File-key fallback improves availability but cannot protect against a process running as the same OS account that can read both key and ciphertext. Namespace separation lets upgraded legacy and new native profiles share a root without record migration or wrong-key collisions. The absence of a native marker is deliberately a recovery failure rather than authority to adopt an unknown key artifact.

Settings mutation becomes a supported product operation. It requires strict source provenance, semantic preservation outside the MCP edit, stale-write detection, full validation, and private atomic replacement. A settings change does not alter an already-running process.

Removal deliberately retains a non-token tombstone so interruption is recoverable. It does not revoke the upstream client, and later re-add creates a new upstream registration. Shared wrapping-key cleanup and backend migration remain out of scope.

## See also

- [Direct MCP onboarding acceptance plan](../acceptance/direct-mcp-onboarding.md)
- [ADR 0113 — Operator MCP authentication profiles and explicit login](./0113-operator-mcp-auth-profiles.md)
- [ADR 0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [ADR 0225 — Operator settings validation command](./0225-operator-settings-validation.md)
- [ADR 0292 — TypeScript SDK local daemon and callback tools](./0292-typescript-sdk-local-daemon-and-tools.md)
- [ADR 0318 — Headless mecatui credential backend selection](./0318-headless-mecatui-credential-backend-selection.md)
- [ADR 0325 — Durable Dynamic Client Registration for direct MCP profiles](./0325-direct-mcp-dcr.md)
- [Architecture guide](../architecture.md#internal-credential-store)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md#mcp-oauth-controller-adr-0220)

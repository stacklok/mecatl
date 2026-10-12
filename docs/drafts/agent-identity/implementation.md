# Implementation and verification

This companion records implementation and verification requirements for the [agent identity and authority design](README.md). Requirements here are not claims about current implementation status or known vulnerabilities.

## Session ownership and isolation

Verify that session enumeration, session access, and event replay enforce the authenticated caller's ownership, including on shared storage. Include negative tests proving another user cannot obtain session titles, prompts, or archived conversation content.

Verify that scheduled invocations preserve owner attribution without treating saved ownership as fresh user authentication or permission to use a provider connection.

## SPIFFE identifier construction

Use literal, nonempty SPIFFE-safe definition names (`[A-Za-z0-9._-]+`, except `.` and `..`); reject invalid names rather than rewriting them. Incarnations use persisted `inc_` values with 26 lowercase base32 characters, or deterministic `legacy_` values for older snapshots.

Apply any issuance limit to the **whole URI**, including child hops, without truncating it. [SPIFFE-ID §2.3](https://github.com/spiffe/spiffe/blob/main/standards/SPIFFE-ID.md#23-maximum-spiffe-id-length) requires support through 2048 bytes and advises against generating longer IDs; a Mecatl cap and compatible name/depth bounds remain undecided. Legacy-name compatibility is also open. The path has no version segment, so incompatible grammar requires migration.

## Credential verification

Operator configuration fixes accepted workload issuers, audiences and identities, issuance limits, and trusted public signing keys. Tokens must not select verification endpoints, CA trust, or server names.

For each tool call, the broker authenticates the workload and validates the agent-instance JWT-SVID's signature against the configured agent trust-domain bundle, required `sub`/`aud`/`exp` claims, expected audience, expiry, and permitted presenter. JWT-SVID does not require `iss` or `jti`. The broker checks that the credential identifies the expected registered instance and session incarnation. Credential validation does not replace authorization of the requested operation.

At token exchange, ToolHive's authorization server validates the user token and the actor credential's trusted bundle association, signature, required claims, audience, expiry, and permitted presenter. The instance JWT-SVID needs an explicit ToolHive-AS audience; validity for presentation to the broker alone is insufficient.

The protected MCP endpoint validates the delegated token's issuer, audience, expiry, and bounded access, applies operation policy, and resolves only the authorized user/tenant/target's provider connection.

Whether the two presentation points use one multi-audience instance credential or separately issued credentials remains to be specified. The [walkthrough](credentials-and-lifecycle.md#interactive-execution) illustrates the former without selecting it as the deployment contract.

Verify issuance rejects a known definition without a trusted particular-instance registration, an unauthorized workload, mismatched ancestry, or a stale session incarnation. These are proposed checks under the [workload-authenticated mecak8s lifecycle registration](agent-definitions.md#registering-instances); the exact wire API, durable schema, and protection from same-process substitution remain unspecified. Verify restoration preserves the instance identity, fixed definition selection, and original bounds. Separately, the existing broker recovery path permits a new process-local attachment binding for a stable broker session within the exact authenticated workload and deployment partition; a different workload cannot assume access. Knowing a noncredential broker session reference must not suffice. Account-association checks must reject OAuth success without authorization for the company user to use the ToolHive subject and provider connection; the association mechanism remains unselected.

## Workload-specific verification

### Kubernetes ServiceAccount bearer

The broker validates the configured Kubernetes issuer, audience, and ServiceAccount allowlist. A stolen ServiceAccount token remains a bearer credential until expiry.

### SPIRE workload JWT-SVID

`mecak8s` obtains a platform JWT-SVID for the broker's audience through the SPIFFE Workload API and presents it on issuance and invocation requests. The broker checks signature, audience, expiry, and allowed workload SPIFFE ID. Platform credential renewal and trust-bundle refresh are separate from the broker issuer's key rotation; platform identity neither replaces agent-instance issuance nor creates federation.

### SPIRE workload X.509-SVID

`mecak8s` obtains an X.509-SVID and key through the Workload API and proves possession during mTLS to both broker endpoints. The broker checks platform trust and permitted workload IDs. A TLS-terminating ingress needs explicitly trusted identity propagation; a forwarded header alone is insufficient. Rotation, bundle refresh, and connection renewal must preserve mTLS checks without bearer fallback.

Workload mTLS does not bind an OAuth access token to its certificate, prove user consent, or isolate agents sharing a harness process.

## Cedar authorization and audit verification

Cedar is the selected engine for the v1 definition-derived MCP server/tool ceiling. Policy representation is an implementation choice; user, resource, argument, and dynamic delegation policies are deferred. Required focused checks are:

- Resolve `Tools`, `DisallowedTools`, and `MCPServers` with existing defaults, selection, and exclusion semantics into registered server/tool pairs. Reject ambiguous or unresolved grants; the same tool name on another server must not inherit access.
- Prove deterministic generation from fixed admitted definition inputs, with retained source, definition revision, and generated policy revision. Prose and model output must not affect grants; source edits and restoration must not silently replace the admitted ceiling.
- Select the actor's policy through trusted registration. Reject caller substitution of actor, definition, revision, or policy. Require an explicit resolved main ceiling, without name-based privilege.
- Evaluate each call, including calls using cached credentials and retries after enrollment. Default deny; generation/loading failures, evaluation failures, and any evaluation diagnostic errors deny, even if the nominal Cedar result is allow.
- Check the exact dispatched server/tool pair and recheck changed requests. Prove the reviewer read passes its ceiling while excluded merge fails; PR 42 and PR 43 reads on the same pair have the same ceiling result. Do not claim this proves PR-specific delegation.
- Preserve existing local, user/connection, ToolHive AS, and vMCP checks. A ceiling permit must not bypass their denials or substitute for user entitlement.
- Verify audit provenance, definition and policy revision correlation, safe reasons and policy IDs, and distinct decision/dispatch/outcome evidence per operation and attempt. Diagnostic errors must yield an effective deny in the record. Redaction must exclude secrets and unrestricted payloads. Test unknown outcomes without automatic replay and durability across restart once storage, ordering, and failure handling are specified; no transactional ledger is selected here.

## Parking and recovery: implementation status

Protected MCP calls can park while awaiting provider reauthorization and resume with account, connection, catalogue, and permission checks. Offline fixtures cover credential and enrolled-connection recovery after host and broker replacement.

Parked broker flows remain process-local. Resuming the same pending operation after broker replacement and renewing agent-instance JWT-SVIDs across runs remain unfinished integration work. The existing recovery path does not establish authenticated human ownership. Credential recovery does not recover external effects; unknown outcomes remain fenced against automatic replay.

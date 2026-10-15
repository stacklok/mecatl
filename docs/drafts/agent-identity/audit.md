# Audit

*This chapter describes the intended architecture, not current implementation status. The audit requirement applies to tool calls generally; the current design scope is broker-mediated MCP calls.*

The audit trail must explain which registered instance requested a call, on whose behalf, which effective ceiling the broker evaluated, whether dispatch was attempted, and what result each observer received. Once the authority work is in place, a Cedar permit establishes only the [server/tool ceiling check](mcp-authorization.md#definition-derived-ceiling), not PR-specific delegated authority or user entitlement.

The human's identity traces back to one sign-in that AS-1 witnessed through the company IdP. Later records about the user rely on that sign-in and AS-1's session record, not on a report from the harness.

## Evidence and provenance

Records must distinguish verified facts from trusted reports. Qualify subjects by issuer or configured trust domain; a workload subject alone is not globally unique.

| Evidence | Source and what it establishes | Limit |
| --- | --- | --- |
| Authenticated workload and instance | Broker validation of workload credentials and the instance JWT-SVID, with the registered session association | Does not authenticate the human or prove a task-specific delegation. |
| Definition source, admitted revision, and ceilings | For global definitions, the broker's definition loader, which read them from configured sources at startup. How other definitions are loaded is not yet decided. | Records what the loader read and resolved. It does not show which loaded definition mecak8s selected for a session. |
| Definition selection and instance ancestry | Workload-authenticated mecak8s lifecycle registration, validated and retained by the broker | A trusted lifecycle assertion, not a fact carried by the SVID or proof of same-process isolation. |
| Initiating user and session ownership | AS-1 ran the sign-in through the company IdP and issued an access token for the Mecatl API. mecak8s records that it validated the token for its API audience and checked that the company identity in the token's `sub_id` owns the host session. The record keeps both the owner (company identity) and the AS-1 subject. | Establishes that AS-1 authenticated the user at sign-in and that the user currently owns the session. It does not establish fresh human action for each call, task-specific delegation, or reported run context. |
| Session authority | AS-1 records the one-time conversion at `OpenSession`: client `mecatl` authenticated with `private_key_jwt`, user, source access-token `jti`, broker session, and hard expiry | Shows that the broker held a valid AS-1 token for that user when the session opened. The session reference is broker-asserted. The conversion does not show that the user approved any later call. |
| Run and execution-delegation links | mecak8s reports them in the workload-authenticated call request as part of the [execution context](mcp-authorization.md#authentication-and-execution-context) | Correlation is not proof that the broker enforced delegated resource bounds. Creation ancestry is separate. The delegation lives in Mecatl's harness and broker records only; the ID-JAG and `V` name the current actor, not the instance that delegated the task. |
| Ceiling evaluation and broker decision | Broker's evaluation of the registered actor's exact server/tool ceiling and route, together with its session, workload, and actor checks | A Cedar permit does not establish user entitlement or connection authority; AS-2 and vMCP enforce those. An allow proves neither dispatch nor downstream success. |
| ID-JAG issuance | AS-1 records each issuance: user from the broker refresh token's session record, session, validated actor, target AS-2, and ID-JAG `jti`. AS-1 validated the SVID and checked that the instance is registered to the broker session. | AS-1 does not see the user again per call; the user comes from the session record. Instance membership comes from the broker registry. |
| AS-2 redemption and consent | AS-2 records each redemption or denial: ID-JAG `jti`, client `mecatl`, native user, actor, resource, and decision. In external mode, AS-2 also records creation and revocation of the consent link. | AS-2 trusts AS-1's signature and receives neither the AS-1 access token nor the SVID. In external mode the consent link is AS-2's own evidence that the user chose Mecatl; in embedded mode consent is implicit. |
| vMCP decision | vMCP records its decision with the access-token reference `(iss, jti)`, native user, actor, tool, outcome, and the propagated operation ID | The gateway sees only the current actor in `act.sub`, not who delegated the task. The operation ID is reported, unverified trace context, as is other forwarded context unless independently verified. |
| Observed result and provider correlation | The response received by the recording component and safe provider references, when available | A broker-observed MCP response is not independent proof of provider-side effects. |

AS-1 and AS-2 must produce the records above, following the [AuditEvent profile](#auditevent-profile); ToolHive's authorization server records no audit events today, so this is new work.

## AuditEvent profile

Every component records evidence as a [toolhive-core](https://github.com/stacklok/toolhive-core/tree/main/audit) `audit.AuditEvent`, so that Mecatl and ToolHive records can be joined in one investigation. This profile says what Mecatl puts in each field. Where toolhive-core does not yet define a convention, Mecatl uses namespaced `mecatl.` keys in `Metadata.Extra` until the shared conventions in toolhive-core [#341](https://github.com/stacklok/toolhive-core/issues/341) and [#342](https://github.com/stacklok/toolhive-core/issues/342) land.

| Question | Field | Content |
| --- | --- | --- |
| Which event | `Metadata.AuditID` | Minted once and stored before the first send. A re-send reuses it, and ingestion removes duplicates by it. |
| What kind of event | `Type` | A `mecatl_`-prefixed snake_case type, such as `mecatl_tool_decision`. The vMCP keeps ToolHive's own types, such as `mcp_tool_call`. Whether an event is a decision, a dispatch, or an observed outcome is part of its type. |
| Result | `Outcome` | toolhive-core's `success`, `failure`, `error`, and `denied`, plus `unknown` once #341 defines it. Each type uses a fixed subset; see the next table. |
| Who acted, and for whom | `Subjects` | `user_iss` and `user_id`: the user as the recording component's own issuer knows them. `owner_iss` and `owner_id`: the company identity from `sub_id`. `workload_iss` and `workload_sub`. `agent_spiffe_id` (inside Mecatl only), `agent_actor_id` (the opaque `agent:` identifier), and `agent_instance_ref`. A credential reference is qualified by its issuer, and `client_id` by its authorization server. An identity the recorder did not verify never appears as `user_id`. |
| Verified or reported | `Metadata.Extra["mecatl.provenance"]` | A map keyed by the `Subjects` key: `verified`, `record:<AuditID>` when taken from a retained record, `reported:<component>`, or `unavailable`. A missing fact is marked `unavailable`, never left out silently. |
| Where it originated | `Source` | The network origin, as in ToolHive: the peer address, plus `request_id` and `session_id` for joining. The calling workload's identity belongs in `Subjects`, not here. |
| Where it was recorded | `Component`, `Metadata.Extra` | `mecatl-broker`, `mecatl-as1`, or the ToolHive component name, plus the recording replica and trust domain. |
| Operation and target | `Target` | toolhive-core's keys: `type=tool`, `name`, `method=tools/call`, and the MCP server. For token-endpoint events, the requested audience and resource. Request metadata appears only if allowlisted for that type. |
| When | `LoggedAt`, `Metadata.Extra` | `LoggedAt` is the recorder's clock when it created the record, in UTC; a re-send keeps it. `mecatl.occurred_at` (RFC 3339 with offset) and `mecatl.clock` record when the event happened and whose clock said so. Events from different components are ordered by attempt and a per-component sequence number, never by timestamp alone. |
| Correlation | `Metadata.Extra` | `mecatl.operation_id` and `mecatl.attempt_id`. A retry is a new attempt. A result that arrives after an `unknown` outcome is a new event that references the earlier one; records are never edited. The broker passes the operation and attempt IDs to AS-1 and the vMCP, which record them as reported. |
| Evidence references | `Data` | Built from a typed structure per event type. Credential references are `(iss, jti)` or `(authorization server, record ID)`, never values. Definition revision and provenance. The broker does not inspect `V`, so its events reference AS-1's issuance event and AS-2's redemption event instead of `V`'s `jti`. |
| OAuth actor history | `DelegationChain` | Only an `act` chain the recorder validated in a token: AS-2 and the vMCP fill it, and broker events leave it empty. |
| Creation and delegation | `Data.lineage` | The run, the delegation (`D42` from run `R41` and its instance), and the creation parent, each marked as reported by mecak8s. They are never derived from the SPIFFE path or from `act`. |
| Profile version | `Metadata.Extra` | `mecatl.profile` names this profile and its version; each `Data` structure carries its own version. |

Each event type uses a fixed set of outcomes:

| Event | Outcomes |
| --- | --- |
| Decision | `success` for allow; `denied`; `error` when evaluation failed, which makes the effective decision deny. |
| Dispatch | `success` when the request was sent; `failure` when it was not, which proves the operation did not run. |
| Observed outcome | `success`, `failure`, `error`, or `unknown` when dispatch may have happened but no completion was observed. |

The identity work needs these event families:

- definition load, instance registration, and agent-instance credential issuance;
- broker session open and close, and AS-1's conversion record at session open; per-call events point to the session-open record for the user;
- sign-out everywhere, with each run stopped, broker session deleted, refresh token revoked, and schedule authority revoked;
- an upstream re-check failure and the revocation that follows;
- ID-JAG issuance and denial at AS-1;
- redemption and denial at AS-2, and consent-link creation and revocation in external mode;
- the broker's decision, dispatch, and observed outcome for each tool call;
- the vMCP's tool-call event;
- audit delivery failure.

The authority work adds Cedar evaluation status, policy IDs, and policy and ceiling revisions. Until then, decision events record that no ceiling was evaluated.

For the reviewer's call in the [walkthrough](credentials-and-lifecycle.md#interactive-execution), the broker's decision event carries `Type` `mecatl_tool_decision`, `Outcome` `success`, `Subjects` with Alice's `user_id` `u-alice-91` (`record:` the session-open event), her `owner_id` `alice-204`, the verified workload, and the reviewer's `agent_spiffe_id` and `agent_actor_id` `agent:reviewer-gh56`, `Target` `pull_request_read` on the payments server, operation `op-pr42-001` and its attempt, and `Data.lineage` with run `R42` and delegation `D42` from `R41`, marked as reported. Its dispatch and outcome events share the operation and attempt. AS-1's issuance event links the reviewer's SPIFFE ID to `agent:reviewer-gh56` and records ID-JAG `(https://broker.example.com/as, jag-pr42-001)`; AS-2's redemption event links that ID-JAG to its access token; the vMCP's tool-call event records that access token and the reported operation ID.

These records have limits:

- They are unsigned, so they do not provide non-repudiation.
- In external mode, AS-2 and vMCP records belong to the gateway operator; the Mecatl-side trail ends at the broker's dispatch event.
- AS-2 may accept the same ID-JAG more than once (ID-JAG §4.4.3 allows re-submission), so one ID-JAG can lead to several redemption events.
- Reaching one operation from a gateway record depends on the propagated operation ID; the mechanism that carries it is not yet chosen.

The `agent:` identifiers in `act` are opaque on purpose: the SPIFFE path, with its creation ancestry, stays inside Mecatl. AS-1's issuance record must link each `agent:` identifier it writes to the registered instance it validated, so an investigator can map a gateway record back to that instance.

## Decision, dispatch, and observed outcome

The broker records its authorization decision, whether it attempted dispatch, and the outcome it observed as separate facts. If vMCP reports which provider connection it used, the broker records that identifier as observed, not as something the broker authorized. An allow is not a dispatch; a dispatch attempt is not proof of receipt or success. A denial dispatches no backend operation. If completion is missing after possible dispatch, the outcome is unknown, not evidence of nonexecution. Credential recovery cannot justify automatic replay of an unknown operation.

Decision, dispatch, and observed-outcome evidence must be durable and survive broker restart. The storage mechanism, write ordering relative to dispatch, failure handling when evidence cannot be persisted, and cross-service delivery and authenticated correlation remain explicitly unresolved implementation requirements. This design does not select a transactional ledger or claim atomicity between an audit write and an external effect.

Use operation and attempt references to connect the evidence; individual events also need their own references. The operation ID, the ID-JAG `jti`, and the AS-2 access token `(iss, jti)` are separate references. A cached access token serves several calls, so its `jti` cannot replace a per-operation reference. To reach one operation from a gateway record, the broker must propagate the operation ID with each MCP request, and vMCP records it as reported, unverified trace context. This propagation is required for correlation; the mechanism is still to be chosen, and it is not an authenticated propagation contract. Because `act` names only the current actor, this operation ID is what keeps delegation `D42` reachable from a gateway log.

## Following a call in an investigation

Starting from `op-pr42-001`, an investigator follows each attempt to the broker decision, the registered actor and fixed definition revision, and the generated policy evidence. Session `A42` leads to AS-1's conversion record, which names user `u-alice-91`, the source access-token `jti`, and the hard expiry, and from there to mecak8s's record of the request and session ownership. On a cache miss, AS-1's issuance record links `A42` and the validated reviewer to ID-JAG `jag-pr42-001`; AS-2's redemption record links that `jti` to native user `usr-alice-7c1d` and access token `access-pr42-001`. In external mode, AS-2's consent-link record shows when Alice linked Mecatl. The vMCP record contains the access-token reference it validates and the reported operation ID `op-pr42-001`. A call served from the cache has no new AS-1 or AS-2 record; it links to the earlier issuance through the cached token, and to its own operation through the reported operation ID. Run `R42` and delegation `D42` lead back to main run `R41` and the API request, with AS-1-witnessed user identity distinguished from workload-reported run, delegation, and lifecycle facts.

The investigator then follows dispatch and observed-outcome records for that attempt, rather than inferring execution from an allow or token issuance. ToolHive records can explain downstream validation or denial. The native provider-credential path is not itself a backend-to-SaaS audit trail. Where the provider supports safe tracing or audit correlation, retain the references needed to reach its operation or audit record and trace it back to the actor. Mark where correlation ends if provider evidence or authenticated cross-service correlation is unavailable. A successful response describes what the observer received, not a guarantee of provider-side effects.

## Redaction and audit-store protection

Record only allowlisted argument and result metadata needed for attribution, with redaction applied before persistence or logging. Safe status categories and nonsecret provider references usually suffice for outcomes. Exclude tokens, refresh tokens, ID-JAGs, client assertions, private keys, client secrets, raw headers, OAuth transaction secrets, and unrestricted argument or result payloads. Record credentials by issuer and `jti` or by record reference, never by value. Policy diagnostics and error text need the same treatment; dumping decoded credentials is not an audit strategy.

Protecting persisted evidence against unauthorized disclosure or modification requires audit-store access and integrity controls. Recording authenticated identities does not itself protect the store. The [credential walkthrough](credentials-and-lifecycle.md#interactive-execution) owns credential validation and OAuth examples; this chapter specifies the evidence needed to investigate their use.

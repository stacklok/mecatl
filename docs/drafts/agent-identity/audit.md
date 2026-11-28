# Audit

*This chapter describes the intended architecture, not current implementation status. The audit requirement applies to tool calls generally; the current design scope is broker-mediated MCP calls.*

The audit trail must explain which registered instance requested a call, on whose behalf, which effective ceiling the broker evaluated, whether dispatch was attempted, and what result each observer received. Once the authority work is in place, a Cedar permit establishes only the [server/tool ceiling check](mcp-authorization.md#definition-derived-ceiling), not PR-specific delegated authority or user entitlement.

The human's identity traces back to one sign-in that AS-1 witnessed through the company IdP. Later records about the user rely on that sign-in and AS-1's session record, not on a report from the harness.

## Evidence and provenance

Records must distinguish verified facts from trusted reports. Qualify subjects by issuer or configured trust domain; a workload subject alone is not globally unique.

| Evidence | Source and what it establishes | Limit |
| --- | --- | --- |
| Authenticated workload and instance | Broker validation of workload credentials and the instance JWT-SVID, with the registered session association | Does not authenticate the human or prove a task-specific delegation. |
| Definition source, admitted revision, and ceilings | For global definitions, the broker's definition loader, which read them from configured sources at startup. **Open.** How other definitions are loaded. | Records what the loader read and resolved. It does not show which loaded definition mecak8s selected for a session. |
| Definition selection and instance ancestry | Workload-authenticated mecak8s lifecycle registration, validated and retained by the broker | A trusted lifecycle assertion, not a fact carried by the SVID or proof of same-process isolation. |
| Initiating user and session ownership | AS-1 ran the sign-in through the company IdP and issued an access token for the Mecatl API. mecak8s records that it validated the token for its API audience and checked that the company identity in the token's `sub_id` owns the host session. The record keeps both the owner (company identity) and the AS-1 subject. | Establishes that AS-1 authenticated the user at sign-in and that the user currently owns the session. It does not establish fresh human action for each call, task-specific delegation, or reported run context. |
| Session authority | AS-1 records the one-time conversion at `OpenSession`: client `mecatl` authenticated with `private_key_jwt`, user, source access-token `jti`, and hard expiry. The broker records the open: broker session, the owner match, and the same `jti` | Shows that the broker held a valid AS-1 token for that user when the session opened. The session reference is broker-asserted. The conversion does not show that the user approved any later call. |
| Run and execution-delegation links | mecak8s reports them in the workload-authenticated call request as part of the [execution context](mcp-authorization.md#authentication-and-execution-context) | Correlation is not proof that the broker enforced delegated resource bounds. Creation ancestry is separate. The delegation lives in Mecatl's harness and broker records only; the ID-JAG and `V` name the current actor, not the instance that delegated the task. |
| Ceiling evaluation and broker decision | Broker's evaluation of the registered actor's exact server/tool ceiling and route, together with its session, workload, actor, and run-owner checks | A Cedar permit does not establish user entitlement or connection authority; AS-2 and vMCP enforce those. An allow proves neither dispatch nor downstream success. |
| ID-JAG issuance | AS-1 records each issuance: user from the broker refresh token's grant record, validated actor, target AS-2, ID-JAG `(iss, jti)`, and the call entry it signed. AS-1 validated the SVID and checked that the instance's session belongs to that user. The broker records the same `(iss, jti)` with the session, run, and instance. | AS-1 does not see the user again per call; the user comes from the grant record. Instance membership comes from the broker registry, and only the broker knows the session. |
| AS-2 redemption and consent | AS-2 records each redemption or denial: ID-JAG `(iss, jti)`, client `mecatl`, native user, actor, resource, and decision. In external mode, AS-2 also records creation and revocation of the consent link. | AS-2 trusts AS-1's signature and receives neither the AS-1 access token nor the SVID. In external mode the consent link is AS-2's own evidence that the user chose Mecatl; in embedded mode consent is implicit. |
| vMCP decision | vMCP records its decision with the access-token reference `(iss, jti)`, native user, actor, tool, outcome, and the propagated operation ID | The gateway sees only the current actor in `act.sub`, not who delegated the task. The operation ID is reported, unverified trace context, as is other forwarded context unless independently verified. |
| Observed result and provider correlation | The response received by the recording component and safe provider references, when available | A broker-observed MCP response is not independent proof of provider-side effects. |

AS-1 and AS-2 must produce the records above, following the [AuditEvent profile](#auditevent-profile). The [ToolHive work list](implementation.md#toolhive-work-list) has the rows.

## AuditEvent profile

Every component records evidence as a [toolhive-core](https://github.com/stacklok/toolhive-core/tree/main/audit) `audit.AuditEvent`, so that Mecatl and ToolHive records can be joined in one investigation. This profile says what Mecatl puts in each field. Where toolhive-core does not yet define a convention, Mecatl uses namespaced `mecatl.` keys in `Metadata.Extra`; the shared conventions in toolhive-core are tracked in [#341](https://github.com/stacklok/toolhive-core/issues/341) and [#342](https://github.com/stacklok/toolhive-core/issues/342).

| Question | Field | Content |
| --- | --- | --- |
| Which event | `Metadata.AuditID` | Minted once and stored before the first send. A re-send reuses it, and ingestion removes duplicates by it. |
| What kind of event | `Type` | A `mecatl_`-prefixed snake_case type, such as `mecatl_tool_decision`. The vMCP keeps ToolHive's own types, such as `mcp_tool_call`. Whether an event is a decision, a dispatch, or an observed outcome is part of its type. |
| Result | `Outcome` | toolhive-core's `success`, `failure`, `error`, and `denied`, plus `unknown` once #341 defines it. Each type uses a fixed subset; see the next table. |
| Who acted, and for whom | `Subjects` | `user_iss` and `user_id`: the user as the recording component's own issuer knows them. `owner_iss` and `owner_id`: the company identity from `sub_id`. `workload_iss` and `workload_sub`. `agent_spiffe_id` (the opaque SPIFFE ID, which is `act.sub`), `agent_actor_id` (its `ag_` identifier), and `agent_instance_ref`. `run_owner_iss` and `run_owner_id`: the [run owner](run-owner.md), reported by mecak8s. `user_client_id` and `user_client_iss`: the client the user's token was issued to, such as the TUI client, and the authorization server that issued it. A credential reference is qualified by its issuer, and `client_id` by its authorization server. An identity the recorder did not verify never appears as `user_id`. |
| Verified or reported | `Metadata.Extra["mecatl.provenance"]` | A map keyed by the `Subjects` key: `verified`, `credential-verified` when the recorder verified a credential but mecak8s reported which instance it belongs to, `record:<AuditID>` when taken from a retained record, `reported:<component>`, or `unavailable`. A missing fact is marked `unavailable`, never left out silently. |
| Where it originated | `Source` | The network origin, as in ToolHive: the peer address, plus `request_id` and `session_id` for joining. The calling workload's identity belongs in `Subjects`, not here. |
| Where it was recorded | `Component`, `Metadata.Extra` | `mecatl-broker`, `mecatl-as1`, or the ToolHive component name, plus the recording replica and deployment; components with a SPIFFE identity also record its trust domain. |
| Operation and target | `Target` | toolhive-core's keys: `type=tool`, `name`, `method=tools/call`, and the MCP server. For token-endpoint events, the requested audience and resource. Request metadata appears only if allowlisted for that type. |
| When | `LoggedAt`, `Metadata.Extra` | `LoggedAt` is the recorder's clock when it created the record, in UTC; a re-send keeps it. `mecatl.occurred_at` (RFC 3339 with offset) and `mecatl.clock` record when the event happened and whose clock said so. Events from different components are ordered by attempt and a per-component sequence number, never by timestamp alone. |
| Correlation | `Metadata.Extra` | `mecatl.operation_id` and `mecatl.attempt_id`. A retry is a new attempt. A result that arrives after an `unknown` outcome is a new observed-outcome event in the same attempt, with the same operation and attempt IDs and `mecatl.late_result=true`; the attempt's earlier observed-outcome record, the `unknown` one, is the one it follows. Records are never edited. `mecatl.supersedes`: on a refusal that undoes an earlier record of the same event, that record's `AuditID`; the refusal has the event's `Type` and its own `Outcome`, code, and reason ([write order](#delivery-protection-and-reconciliation)). The broker passes the operation ID to AS-1 inside the call entry, and the operation and attempt IDs to the vMCP, which records them as reported. |
| Evidence references | `Data` | Built from a typed structure per event type. Credential references are `(iss, jti)` or `(authorization server, record ID)`, never values. Definition revision and provenance. When `V` is a JWT, the broker's decision event records its `(iss, jti)`, read without verification and never used to authorize; otherwise it references AS-2's redemption event. |
| OAuth actor history | `DelegationChain` | Only an `act` chain the recorder validated in a token: AS-2 and the vMCP fill it, and broker events leave it empty. |
| Creation and delegation | `Data.lineage` | The run, plus references: the `AuditID` of the delegation event the call runs under and of the acting instance's registration event. The [lineage](#lineage) events hold the relationships, marked as reported by mecak8s. They are never derived from the SPIFFE path or from `act`. |
| Profile version | `Metadata.Extra` | `mecatl.profile` names this profile and its version; each `Data` structure carries its own version. |

Each event type uses a fixed set of outcomes:

| Event | Outcomes |
| --- | --- |
| Decision | `success` for allow; `denied`; `error` when evaluation failed, which makes the effective decision deny. |
| Dispatch | `success` when the request was, or may have been, sent; `failure` when it was not, which proves the operation did not run. An attempt whose own request was not sent, but whose token the gateway reports already used, may have run, so it is `success`. |
| Observed outcome | `success`, `failure`, `error`, or `unknown` when dispatch may have happened but no completion was observed. |
| Refused dispatch or outcome report | `denied`. |
| ID-JAG issuance | `success` when AS-1 issued it; `denied` when AS-1 refused it (a broker check that refuses a call is recorded as a denied decision, and no ID-JAG is requested); `error` when it could not be completed, such as `record_not_accepted`. |
| Session open, registration, delegation report, schedule setup or renewal | `success`; `denied` when a check refused it; `error` when it could not be completed, such as `record_not_accepted`. |
| Session close | `success`. |

A refusal that supersedes an earlier record, an allowing decision included, uses the outcome of its reason: `error` for `record_not_accepted`, and `denied` when a close undid the event.

The identity work needs these event families:

- sign-in and access-token issuance at AS-1;
- API requests and session ownership checks at mecak8s;
- definition load, instance registration, and agent-instance credential issuance;
- each delegation from one run to another;
- broker session open and close, AS-1's conversion record at session open, and any open the broker refused because the token's user does not own the session; per-call events point to the session-open record for the user;
- sign-out everywhere, with each run stopped, broker session closed, refresh token revoked, and schedule grants revoked, and the revocation reason;
- an upstream re-check failure and the revocation that follows;
- ID-JAG issuance and denial at AS-1;
- redemption and denial at AS-2, and consent-link creation and revocation in external mode;
- the broker's decision, dispatch, and observed outcome for each tool call;
- the vMCP's tool-call event;
- audit delivery failure, and the start and stop of each component's record stream.

The authority work adds Cedar evaluation status, policy IDs, and policy and ceiling revisions. Until then, decision events record that no ceiling was evaluated.

For the reviewer's call in the [walkthrough](credentials-and-lifecycle.md#interactive-execution), the broker's decision event carries `Type` `mecatl_tool_decision`, `Outcome` `success`, `Subjects` with Alice's `user_id` `u-alice-91` (`record:` the session-open event), her `owner_id` `alice-204`, the verified workload, and the reviewer's `agent_spiffe_id` and `agent_actor_id` `ag_7hq2x9`, `Target` tool `github.pull_request_read` on server `github`, operation `op-pr42-001` and its attempt, and `Data.lineage` with run `R42` and a reference to the delegation event for `D42`. Its dispatch and outcome events share the operation and attempt. AS-1's issuance event records the reviewer's SPIFFE ID `spiffe://agents.example.com/agent/ag_7hq2x9` as `act.sub` and records ID-JAG `(https://broker.example.com/as, jag-pr42-001)`; AS-2's redemption event links that ID-JAG to its access token; the vMCP's tool-call event records that access token and the reported operation ID.

These records have limits:

- They are unsigned, so they do not provide non-repudiation.
- In external mode, AS-2 and vMCP records belong to the gateway operator; the Mecatl-side trail ends at the broker's dispatch event.
- An external gateway may allow an ID-JAG to be re-submitted until it expires, as ID-JAG §4.4.4 permits, so one ID-JAG can lead to several redemption events there, all linked by its `(iss, jti)`. The embedded gateway accepts each ID-JAG once. In external mode, AS-2 records which approval record authorized each redemption.
- Reaching one operation from a gateway record depends on the propagated operation ID, which vMCP records as reported, not verified.

### Lineage

Two event types record how instances relate. Both are marked as reported by mecak8s, because only mecak8s observes these relationships.

- **Registration** (`mecatl_instance_registered`) records the instance with its `ag_` identifier and SPIFFE ID, its definition, its session tree, and the `AuditID` of its creation parent's registration.
- **Delegation** (`mecatl_delegation`) records the delegation, such as `D42`; the delegating run and instance; the receiving run, its owner, and its instance; and, when the delegating run was itself delegated, the `AuditID` of that parent delegation.

Lineage events are audit records. They are kept for the audit retention period, and session cleanup does not delete them. References between them are `AuditID`s in the append-only audit store, not live session state.

From any tool call, an investigator follows the delegation reference up to the root to reconstruct execution delegation, and registration references to reconstruct creation ancestry. If a reference does not resolve, the reconstruction reports the chain as incomplete, names where it stopped, and gives the reason: unresolved, truncated, or malformed. It stops on a cycle. It never presents a partial chain as complete.

Agent SPIFFE IDs, and so `act.sub`, are opaque on purpose: creation ancestry stays inside Mecatl, in the broker's registry and in the registration events above. An investigator maps a gateway record's `act.sub` to the instance's registration event, then follows the registration references up to the root. The audit store, not the live registry, is the record investigators use.

## Decision, dispatch, and observed outcome

The broker records its authorization decision. mecak8s's MCP client, which sends the call, reports whether it attempted dispatch and the outcome it observed, and the broker records those as separate facts, marked as reported. It reports each attempt once it ends, a cancelled one included: cancelled after its `tools/call` was written, the outcome is unknown; before, the dispatch failed. A report may add the client's fixed code for why the attempt was not sent or was refused, such as `invalid_token`, never free text. The broker joins each report to the decision that issued its attempt. A report for an operation the broker never decided, or an attempt it never issued, is recorded as a mismatch, not dropped; so is a second, different report of an attempt. A report whose dispatch or outcome is outside its event's set is refused, and the refusal is recorded as a mismatch. A failed report never fails the call; the client retries it. If vMCP reports which provider connection it used, the broker records that identifier as observed by vMCP, not as something the broker authorized. An allow is not a dispatch; a dispatch attempt is not proof of receipt or success. A denial dispatches no backend operation. If completion is missing after possible dispatch, the outcome is unknown, not evidence of nonexecution. Credential recovery cannot justify automatic replay of an unknown operation.

Decision, dispatch, and observed-outcome evidence must survive broker restart; [delivery, protection, and reconciliation](#delivery-protection-and-reconciliation) sets the write order and what happens when evidence cannot be delivered. This design does not claim atomicity between an audit write and an external effect.

Use operation and attempt references to connect the evidence; individual events also need their own references. The operation ID, the ID-JAG `(iss, jti)`, and the AS-2 access token `(iss, jti)` are separate references. Each access token is bound to one call and names its operation ID in its `authorization_details`. AS-1 signed that entry into the ID-JAG, so a gateway record leads to one operation through the entry that AS-1 and AS-2 signed. The broker records the result of its output check, which compares the entry in the token response with the one it sent. mecak8s also propagates the operation ID with each MCP request, and vMCP records it as reported, unverified trace context that must match the token. The broker sends the operation ID to AS-1 only inside the call entry. mecak8s sends them in each MCP request's `_meta`; that propagation is not an authenticated contract. Because `act` names only the current actor, this operation ID is what keeps delegation `D42` reachable from a gateway log.

## Delivery, protection, and reconciliation

Correctly shaped records are not enough if they can be lost, rewritten, or never compared.

**Emission and destination.** Every component records through the toolhive-core audit logger: mecak8s, the broker, AS-1, AS-2, and the vMCP. The logger's sink already writes to standard output or an HTTP endpoint, so each deployment chooses where records go. AS-1's and AS-2's events, including sign-in and token issuance, are in the [ToolHive work list](implementation.md#toolhive-work-list).

**Protection.** The deployment points every component's sink at a store that mecak8s cannot write to or delete from, such as an append-only collector or object storage with retention locks. This protects records already delivered even while the broker runs inside mecak8s: a compromise can forge or suppress records from then on, but cannot rewrite earlier ones. Investigators get read-only access to the store.

**Producers.** Each component records the event families it observes first-hand:

| Component | Event families |
| --- | --- |
| mecak8s | API requests and session ownership checks; dispatch attempts and observed outcomes, which it also reports to the broker |
| Broker | Definition load, instance registration, delegation, session open and close, sign-out everywhere, the decision for each tool call, and the dispatch and outcome mecak8s reports, marked as reported |
| AS-1 | Sign-in, including the company IdP session and authentication time; access-token issuance; the session-open conversion; ID-JAG issuance and denial; refresh-token revocation; upstream re-check failures |
| AS-2 | Redemption and denial; consent-link creation and revocation |
| vMCP | Tool calls |

**Write order and failure.** Every broker record goes through a sink that can refuse it. Whether the broker goes ahead when its sink does not accept a record depends on the event:

| Event | Its record must be accepted | If it is not accepted |
| --- | --- | --- |
| Session open, of any kind | Before the open returns | The open changes nothing: a session it would have created is closed, a refresh token the session held stays as it was, and a refresh token the open got is discarded and revoked. The broker still remembers the source `jti` it converted. An open that converted the request's token fails as for a refused token, so the client refreshes it before opening again; it gets 503 instead when the same caller failed the same way within the last minute ([step (3)](credentials-and-lifecycle.md#3-session-open-and-the-broker-refresh-token)). Any other open fails as `UNAVAILABLE`. A scheduled run's open fails as its [section](credentials-and-lifecycle.md#scheduled-run-session-open) says. |
| Instance registration | Before the instance is admitted | The registration fails as `UNAVAILABLE`, and the instance is not registered. A new helper's or team member's first registration is not retried: it does not run. The root's registration is retried when the run starts, and a re-registration after a reopen on the instance's next call, as their sections say. |
| Accepted delegation report | Before the delegation is stored | The report fails as `UNAVAILABLE`, and nothing is stored. A new helper's or team member's first report is not sent again: the helper or team member does not run. A delegation reported again after a reopen is reported again on the helper's next call. |
| ID-JAG issuance, recording its `jti` when AS-1 returns it | Before the broker redeems the ID-JAG | The broker does not redeem it, and the call fails as `UNAVAILABLE`. |
| Schedule setup or renewal | Before the schedule or its new grant is stored | As a session open that converted the request's token, including the 503 on a repeat: nothing is stored, the grant AS-1 issued is revoked, and Alice's client refreshes before trying again. A failed renewal keeps the schedule's current grant. |
| Decision that allows a tool call or a tool listing | Before the broker returns the access token | The broker returns no token, and the call fails as `UNAVAILABLE`, so it cannot be dispatched. |
| Session close, from any path | Never waited for | The close takes effect. |
| Refusal: a refused open, registration, or report, or a denied call | Never waited for | The refusal is returned. A refused open never leaves a session, whatever happens to its record. |
| Any other broker record | Never waited for, unless the section that defines the event says otherwise. An event that later records reference by `AuditID` is listed above or waits as its section says. A late result after an `unknown` outcome finds the earlier one as its attempt's observed outcome ([correlation](#auditevent-profile)), so it does not depend on that record's `AuditID` | The event takes effect. |

Later records reference the events that wait by `AuditID`: per-call events point to the session-open record, lineage to registration and delegation records, and a scheduled run's records to the schedule-setup record. The ID-JAG and the decision wait so that nothing is redeemed or dispatched before its record exists. A session, instance, or delegation without its record would leave those references unresolved, so it is not kept. A sink can store a record and still fail to confirm it, for example an HTTP sink that times out after the collector wrote it, so a record the broker treated as not accepted may exist. When the broker undoes an event after its record was not accepted, or after its record was accepted because the session closed while it was recorded, including a decision whose token it then withholds, it records a refusal whose `Metadata.Extra["mecatl.supersedes"]` names that record's `AuditID`. The trail then shows the earlier record as rolled back, and lineage checks and reconciliation skip it rather than treat it as forged. A close or a refusal is never held back, because holding back a close would keep the session's refresh token usable, and holding back a refusal would turn a denial into a retryable error. The broker mints each attempt ID. This is the broker's choice, not a library default. The order across a broker restart, between a record and the state it covers, is part of the broker's durable state ([#2563](https://github.com/stacklok/mecatl/issues/2563)). A record being accepted by a sink is not the same as durable storage; the deployment's destination provides durability. Delivery failures are reported on a separate channel, as toolhive-core [#343](https://github.com/stacklok/toolhive-core/issues/343) proposes, so a failing destination does not hide its own failure.

**Lossless delivery.** Some deployments, such as those under government compliance regimes, must not lose audit records. There, every component's sink must be one whose acceptance means the record was durably received, for example an HTTP sink pointed at a collector that acknowledges only after a durable write. Standard output read by a log shipper does not qualify, because the shipper can drop records under load or on a crash. When the sink is slow or unavailable, components block or refuse the operation rather than buffer and drop records. The broker's rules above then mean that a tool call is dispatched only after its decision record is durably stored. How records the broker does not wait for, such as closes and refusals, are delivered under a lossless sink is open in [#2848](https://github.com/stacklok/mecatl/issues/2848).

**Gap detection.** Each component numbers its records with a counter that is contiguous for each component, replica, and process start, and records a start and stop event for each stream. A missing number or stream end shows that records were lost or removed. All components use a synchronized time source, and each record names whose clock it used.

**Reconciliation.** Records from different stores are checked against each other, and each mismatch is flagged:

| Mismatch | What it can mean |
| --- | --- |
| An AS-2 redemption with no matching AS-1 ID-JAG issuance | An ID-JAG minted outside the recorded path |
| An AS-1 ID-JAG issuance with no broker record for its `(iss, jti)` | A response lost after AS-1 issued the ID-JAG; the signed call entry still names the operation |
| A vMCP tool call with no matching broker decision for the same token `jti` and operation | An access token used outside the broker |
| A reported dispatch with no matching vMCP tool call, or a vMCP tool call with no reported dispatch | A record suppressed or misreported on one side |
| A session-open conversion whose source token has no AS-1 issuance and sign-in | A user token minted outside the recorded path |

In external mode, the gateway's records belong to its operator, so these checks need the operator's records.

**Lineage consistency.** When mecak8s reports a delegation, the broker checks that the delegating and receiving instances are registered to the same session tree, and that the parent delegation's receiving run is the delegating run. A delegation that passes is marked as reported and consistency-checked. These checks catch mistakes, not a compromised mecak8s.

**Retention.** A lineage event is kept at least as long as any event that references it. Each deployment names who owns retention and for how long. When the gateway is external, the deployment agrees with its operator on how long gateway records are kept and how investigators reach them.

**Coverage.** These records cover broker-mediated MCP calls and the identity lifecycle around them. Filesystem tools, Shell, and spawning are not broker-mediated, so the harness's own logs are their only record.

**Later.** After the broker becomes a separate service, each component can hash-chain its records and anchor signed checkpoints outside its own store. While the broker runs inside mecak8s, the signing key would sit with the attacker, so the external store already gives most of the value. Signed records for non-repudiation are also later work.

## Following a call in an investigation

Starting from `op-pr42-001`, an investigator follows each attempt to the broker decision, the registered actor and fixed definition revision, and the generated policy evidence. Session `A42` leads to AS-1's conversion record, which names user `u-alice-91`, the source access-token `jti`, and the hard expiry, and from there to mecak8s's record of the request and session ownership. For each call, AS-1's issuance record links `A42` and the validated reviewer to ID-JAG `jag-pr42-001`; AS-2's redemption record links that `jti` to native user `usr-alice-7c1d` and access token `access-pr42-001`. In external mode, AS-2's consent-link record shows when Alice linked Mecatl. The vMCP record contains the access-token reference it validates and the reported operation ID `op-pr42-001`. Each call has its own access token, so the vMCP record links to the issuance through that token, and to its operation through the operation ID in the token's binding. Run `R42` and delegation `D42` lead back to main run `R41` and the API request, with AS-1-witnessed user identity distinguished from workload-reported run, delegation, and lifecycle facts.

Delegation can run deeper. Suppose the reviewer delegates a dependency check to a `security-reviewer` specialist, whose run calls a tool. The specialist's decision event references its delegation event, which names the reviewer's run `R42` and references the delegation event for `D42`. That event names main's run `R41`, which belongs to Alice's session. The registration events show separately that main created the reviewer and the reviewer created the specialist. Every link in that walk is reported by mecak8s; Alice's identity comes from AS-1's record. Two specialists from the same definition stay distinct because each has its own instance and registration.

The investigator then follows dispatch and observed-outcome records for that attempt, rather than inferring execution from an allow or token issuance. ToolHive records can explain downstream validation or denial. The native provider-credential path is not itself a backend-to-SaaS audit trail. Where the provider supports safe tracing or audit correlation, retain the references needed to reach its operation or audit record and trace it back to the actor. Mark where correlation ends if provider evidence or authenticated cross-service correlation is unavailable. A successful response describes what the observer received, not a guarantee of provider-side effects.

## Redaction and audit-store protection

Record only allowlisted argument and result metadata needed for attribution, with redaction applied before persistence or logging. Safe status categories and nonsecret provider references usually suffice for outcomes. Exclude tokens, refresh tokens, ID-JAGs, client assertions, private keys, client secrets, raw headers, OAuth transaction secrets, and unrestricted argument or result payloads. Record credentials by issuer and `jti` or by record reference, never by value. Policy diagnostics and error text need the same treatment; dumping decoded credentials is not an audit strategy.

Recording authenticated identities does not itself protect the store; [delivery, protection, and reconciliation](#delivery-protection-and-reconciliation) covers the destination and its access. The [credential walkthrough](credentials-and-lifecycle.md#interactive-execution) owns credential validation and OAuth examples; this chapter specifies the evidence needed to investigate their use.

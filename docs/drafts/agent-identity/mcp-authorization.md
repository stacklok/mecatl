# MCP authorization

*This chapter describes the intended architecture, not current implementation status. The ceiling check it describes belongs to the [authority work](README.md#order-of-work), which follows the identity work.*

The proposal extends the existing broker's MCP invocation responsibility with agent-instance identity and authority checks; it does not introduce a separate authorization service. **Cedar is the selected policy engine. It evaluates only the acting instance's effective MCP server/tool ceiling: its definition ceiling narrowed by its parent's. This check is part of the [authority work](README.md#order-of-work), which follows the identity work.** It supplements existing local permissions and the user and connection checks that AS-2 and vMCP enforce downstream; a Cedar permit is not proof of user entitlement. Obtaining an OAuth token is a separate credential step, not a substitute for authorization.

## Definition-derived ceiling

Trusted definition admission resolves the existing `Tools`, `DisallowedTools`, and `MCPServers` semantics against registered MCP servers and their tools into permitted server/tool pairs. Resolution must preserve existing selection and exclusion semantics, including defaults, rather than interpreting a raw tool list as grants. A tool name alone cannot identify an authorized pair across servers. Unresolved or ambiguous references cannot grant access.

Cedar policies are generated deterministically from that resolved ceiling and tied to the fixed admitted definition source and revision. The same admitted inputs produce the same ceiling and generated policy revision. Generation uses structured definition facts, never an LLM or inferred intent from `Body`, descriptions, skills, or other prose. Retain the resolution inputs and generated policy revision so that the decision can be explained after the source changes. Each call is evaluated against the acting instance's effective ceiling, defined under [delegated authority](#delegated-authority). Representation, including the Cedar schema and entity/action/resource layout, is left to the implementer.

After authenticating the instance, the broker obtains its definition, stored effective ceiling, and policy association from trusted registration records. Caller-supplied definition names, revisions, policy identifiers, or tool lists cannot select a more privileged ceiling. The main instance also needs an explicit resolved ceiling; `main`, `system`, or a root identity grants no name-based privilege.

For every call, the broker evaluates the exact registered server/tool pair that will be dispatched. Default deny applies: no matching permit means denial. Policy generation or loading failures and evaluation failures deny; **any Cedar evaluation diagnostic error makes the effective decision deny, even if Cedar also returns a nominal allow.** An error-free permit satisfies only this ceiling check. A changed target or tool requires another evaluation before dispatch.

## Authentication and execution context

Workload authentication establishes which mecak8s service is calling. The instance JWT-SVID establishes which registered agent instance is acting. The broker validates both credentials against configured trust, audiences, expiry, and permitted presenters, and checks the instance's registered session association. Neither credential alone establishes the initiating user, current run, or delegated authority.

The broker binds each call to its **execution context**: workload, acting instance, session, initiating user, run, and any delegation. Workload, instance, and session are verified from credentials and registration. The initiating user comes from Alice's sign-in at AS-1, not from a harness report. At session open the broker obtains a session-bound broker refresh token from AS-1, and every later ID-JAG names the user from that token's AS-1 record, not from caller input. See [session open](credentials-and-lifecycle.md#3-session-open-and-the-broker-refresh-token). User, run, and delegation are not inputs to the ceiling check. Run and delegation context remain trusted harness reports. mecak8s reports each delegation to the broker when it happens, so the broker can record it as a [lineage event](audit.md#lineage). Enforcing task-specific delegated bounds requires a verified per-operation binding for those reported fields. Missing or inconsistent context fails closed. [Instance registration](agent-definitions.md#registering-instances) supplies identity records, not authorization for a run or tool call.

## Delegated authority

A child's permitted MCP operations must stay within the authority delegated by its parent and the child's definition ceiling. Delegation can preserve or narrow that authority, not widen it.

Delegation history records how the acting instance came to act. An ancestor's permissions do not give the current actor additional rights. At registration, the broker computes the instance's **effective ceiling**: its definition ceiling intersected with its parent's effective ceiling. A main has no parent, so its effective ceiling is its definition ceiling. The broker stores the result with the instance's incarnation and never recomputes or widens it: renewal and restoration reload the stored set, and a fresh incarnation gets a fresh computation. Registration fails if the parent is not a registered, unrevoked instance of the same session. An empty intersection registers an instance whose MCP calls all deny. Revoking an instance revokes its descendants.

For example, a read-only reviewer spawns a child from a `deployer` definition whose ceiling includes `pull_request_merge`. The child's effective ceiling is the deployer ceiling intersected with the reviewer's, so it excludes merge. The broker denies the child's merge call whatever the deployer definition allows and whatever another `deployer` instance may do. The harness performs the same intersection today; the broker computes it independently. Existing harness delegation restrictions remain in force.

The intersection narrows a child's definition but trusts the registered parent link: a compromised mecak8s that registers a child under a more privileged parent still inflates its effective ceiling (see [registering instances](agent-definitions.md#registering-instances)). Task-specific limits, such as keeping the reviewer to PR 42 under `D42`, are dynamic delegation policy and remain deferred. An instance JWT-SVID identifies the agent; it does not prove those limits.

The gateway sees delegation only as identity, not as authority. AS-1 writes only the acting instance into the ID-JAG's `act`, as an opaque `agent:` identifier, and AS-2 copies it into `V`. vMCP binds its session to `act.sub`. The instance that delegated the task does not appear in the token; Mecatl's harness and broker records keep the delegation, and a later chained exchange may add it as a nested `act`. `act` does not carry the agent's ceiling, so the broker alone enforces the effective ceiling, and the gateway enforces only user and connection. The long-term plan is for AS-1 to write the instance's effective ceiling into the ID-JAG as scope or RFC 9396 `authorization_details`, for AS-2 to carry it into `V`, and for the gateway to enforce it as a second check. The ID-JAG and `V` formats reserve that slot; until it is filled, a gateway allow says nothing about the agent ceiling. See the [ID-JAG step](credentials-and-lifecycle.md#7-id-jag-from-as-1) and [actor history](credentials-and-lifecycle.md#as-2-access-token-use-and-actor-history).

## Operations and resources

The ceiling check distinguishes registered MCP servers and tools, but does not evaluate resource or argument policies. Permission to call a tool is not evidence of authorization for a particular repository or PR. User, resource, argument, and dynamic delegation policies are deferred, and the design does not yet provide comprehensive enterprise user-policy enforcement.

For example, a reviewer whose resolved ceiling includes `pull_request_read` on the registered payments server can pass the ceiling check for a read, but cannot pass it for `pull_request_merge` if merge is excluded. Reading PR 42 and reading PR 43 with that same server/tool pair produce the same ceiling result. The broker's ceiling check cannot establish that the reviewer stayed within Alice's PR-42-only request. Existing connection and downstream checks still apply to either call.

Future broker policies decide on what the broker knows first-hand:

- the user and the identity-provider attributes AS-1 carries for them;
- the agent instance, with its definition and provenance;
- the MCP server and tool;
- the resources the request names in its arguments;
- the session and delegation context.

They do not decide on provider connections or provider-side permissions; the gateway and the provider own those. Argument-based policy checks what was requested, not what the backend does. A tool whose effects its arguments do not bound, such as a code search across every repository the user can see, is restricted through the ceiling instead. Resource policy also needs a per-tool mapping from arguments to resources, such as which argument names the repository.

Agent-instance identity does not prevent a prompt-injected agent from misusing operations within its permitted authority. The broker constrains protected MCP calls through independent authorization checks; it does not establish that an allowed call faithfully serves the user's intent or protect the entire agent loop.

## User token and the authorized request

Mecatl validates Alice's AS-1 access token for its API and checks current session ownership. The broker turns it into a session-bound broker refresh token at AS-1; no company IdP token reaches AS-1 or the broker. AS-1 issues an ID-JAG naming Alice and the acting instance only while the session is open and the instance is registered to it, and the broker redeems that ID-JAG at AS-2 for `V`. Neither the AS-1 access token, the refresh token, nor the SVID reaches AS-2. Model-generated arguments cannot choose or override identity evidence. The exchange itself is described [below](#authorization-credentials-and-dispatch); see [per-operation authorization](credentials-and-lifecycle.md#6-per-operation-authorization) and the steps after it for token types, fields, and validation.

The broker checks the actual MCP server/tool pair against the effective ceiling before any exchange or dispatch. User and connection authorization are enforced downstream: AS-2 redeems the ID-JAG only for a mapped user (embedded) or a consent-linked user (external), and vMCP dispatches only with the user's stored credential, keyed by (gateway, native user, provider). The broker does not check provider connections itself: it could only copy the gateway's data, and the provider enforces its own permissions on the request. The final request, including arguments, must remain bound to the checks and dispatch, but recording or binding arguments does not mean the ceiling check authorizes their resource-specific meaning.

Request transformations must occur before the applicable checks, or the changed request must be checked again. Dispatch must use the checked request.

## Two requests, two authorization decisions

In the [interactive walkthrough](credentials-and-lifecycle.md#interactive-execution), Alice owns session `hostS42`. The main instance lists pull requests, then delegates review of PR 42 to a reviewer. The tool names and arguments are illustrative, not a selected MCP server contract.

| Request | Effective-ceiling check |
| --- | --- |
| Main run `R41`: `pull_request_list(repository=acme/payments)` | The main's effective ceiling, which is its definition ceiling, must permit the resolved server and list tool. Main has no implicit exemption. |
| Reviewer run `R42` via `D42`: `pull_request_read(repository=acme/payments, number=42)` | The reviewer's effective ceiling, its definition ceiling intersected with the main's, must permit the resolved server and read tool. `D42` records the delegated task but is not an input to the ceiling check. |

Both requests also require valid credentials and execution-context checks, and must pass local checks and the downstream AS-2 and vMCP checks. The reviewer's SVID identifies the child; even with creation ancestry in its subject, it does not prove authority under `D42`. The child presents its own credential, not the parent's SVID. Future broker enforcement of task-specific delegated bounds will need trusted evidence of those bounds.

For both calls, Alice's use of her GitHub account is checked downstream. AS-2 redeems the ID-JAG only for a mapped or consent-linked native user, and vMCP loads only the credential stored for `(gateway.example.com, usr-alice-7c1d, github)`. The model cannot select another user's credential. Matching account names or email addresses establish nothing. The broker holds no connection evidence of its own. See [provider-connection selection](credentials-and-lifecycle.md#provider-connection-selection).

## Authorization, credentials, and dispatch

Local permissions and hooks apply before invocation. The broker then authenticates the workload and actor, checks session and execution context, evaluates the effective server/tool ceiling, and verifies the route. A failure stops the call before any exchange. If these checks pass, the broker uses a cached `V` or obtains one through AS-1 and AS-2, then dispatches to vMCP.

| Outcome | Dispatch behavior |
| --- | --- |
| Denial | Invalid credentials or context, a denied ceiling, an expired or revoked broker refresh token, or denied downstream policy stops the request before provider dispatch. |
| Authorization pending | AS-2 refuses to redeem because no consent link exists (external mode), or vMCP finds no stored provider credential. The broker returns the existing `mcp_authorization_pending` outcome; the user completes the link or connection, and the call retries through every check. How an external gateway reports these refusals to the broker as a structured outcome is open. Connecting does not approve the pending operation. |
| Allow | The broker's checks pass and the broker dispatches with `V`. vMCP validates `V`, applies its operation policy, and loads the user's stored provider credential; it dispatches only if it finds one. |

Missing, invalid, stale, or revoked identity, consent, connection, or authorization evidence fails closed, without fallback to a service credential, another user, or a more privileged agent.

When no valid cached `V` exists for the session, instance, and gateway, the broker authenticates to AS-1 as OAuth client `mecatl` with `private_key_jwt` and requests an ID-JAG by RFC 8693 token exchange, with the session's broker refresh token as `subject_token` and the actor SVID as `actor_token`. It redeems the ID-JAG at AS-2 with RFC 7523's JWT-bearer grant, authenticating again as `mecatl`. AS-2 resolves the native user and returns `V` naming that user, the acting agent in `act`, and client `mecatl`. AS-2 issues no refresh token; when `V` expires, the broker repeats the exchange. A cached `V` skips the exchange and redemption, never the broker's checks or vMCP's authorization. AS-2 and vMCP make their own grant, identity, operation, and connection decisions; a downstream allow does not replace the broker's ceiling decision, and a broker allow does not replace theirs. See [credential roles](credentials-and-lifecycle.md#credential-roles) and the [interactive walkthrough](credentials-and-lifecycle.md#interactive-execution) for the full sequence.

An allow does not prove dispatch or provider success. The [audit contract](audit.md#decision-dispatch-and-observed-outcome) owns durable evidence and correlation requirements. An unknown outcome must not be automatically replayed merely because credentials were recovered.

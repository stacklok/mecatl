# MCP authorization

*This chapter describes the intended architecture, not current implementation status.*

The proposal extends the existing broker's MCP invocation responsibility with agent-instance identity and authority checks; it does not introduce a separate authorization service. **Cedar is the selected policy engine. V1 evaluates only the acting instance's effective MCP server/tool ceiling: its definition ceiling narrowed by its parent's.** It supplements existing local permissions, user/connection checks, and ToolHive authorization; a Cedar permit is not proof of user entitlement. Obtaining an OAuth token is a separate credential step, not a substitute for authorization.

## Definition-derived ceiling

Trusted definition admission resolves the existing `Tools`, `DisallowedTools`, and `MCPServers` semantics against registered MCP servers and their tools into permitted server/tool pairs. Resolution must preserve existing selection and exclusion semantics, including defaults, rather than interpreting a raw tool list as grants. A tool name alone cannot identify an authorized pair across servers. Unresolved or ambiguous references cannot grant access.

Cedar policies are generated deterministically from that resolved ceiling and tied to the fixed admitted definition source and revision. The same admitted inputs produce the same ceiling and generated policy revision. Generation uses structured definition facts, never an LLM or inferred intent from `Body`, descriptions, skills, or other prose. Retain the resolution inputs and generated policy revision so that the decision can be explained after the source changes. Each call is evaluated against the acting instance's effective ceiling, defined under [delegated authority](#delegated-authority). Representation, including the Cedar schema and entity/action/resource layout, is left to the implementer.

After authenticating the instance, the broker obtains its definition, stored effective ceiling, and policy association from trusted registration records. Caller-supplied definition names, revisions, policy identifiers, or tool lists cannot select a more privileged ceiling. The main instance also needs an explicit resolved ceiling; `main`, `system`, or a root identity grants no name-based privilege.

For every call, the broker evaluates the exact registered server/tool pair that will be dispatched. Default deny applies: no matching permit means denial. Policy generation or loading failures and evaluation failures deny; **any Cedar evaluation diagnostic error makes the effective decision deny, even if Cedar also returns a nominal allow.** An error-free permit satisfies only this ceiling check. A changed target or tool requires another evaluation before dispatch.

## Authentication and execution context

Workload authentication establishes which mecak8s service is calling. The instance JWT-SVID establishes which registered agent instance is acting. The broker validates both credentials against configured trust, audiences, expiry, and permitted presenters, and checks the instance's registered session association. Neither credential alone establishes the initiating user, current run, or delegated authority.

The broker binds each call to its **execution context**: workload, acting instance, session, initiating user, run, and any delegation. Workload, instance, and session are verified from credentials and registration. In v1, mecak8s reports the user, run, and delegation in the workload-authenticated call request; the broker records them as reported, and none of them is a v1 Cedar input. Enforcing task-specific delegated bounds will require a verified per-operation binding for those reported fields. Missing or inconsistent context fails closed. [Instance registration](agent-definitions.md#registering-instances) supplies identity records, not authorization for a run or tool call.

## Delegated authority

A child's permitted MCP operations must stay within the authority delegated by its parent and the child's definition ceiling. Delegation can preserve or narrow that authority, not widen it.

Delegation history records how the acting instance came to act. An ancestor's permissions do not give the current actor additional rights. At registration, the broker computes the instance's **effective ceiling**: its definition ceiling intersected with its parent's effective ceiling. A main has no parent, so its effective ceiling is its definition ceiling. The broker stores the result with the instance's incarnation and never recomputes or widens it: renewal and restoration reload the stored set, and a fresh incarnation gets a fresh computation. Registration fails if the parent is not a registered, unrevoked instance of the same session. An empty intersection registers an instance whose MCP calls all deny. Revoking an instance revokes its descendants.

For example, a read-only reviewer spawns a child from a `deployer` definition whose ceiling includes `pull_request_merge`. The child's effective ceiling is the deployer ceiling intersected with the reviewer's, so it excludes merge. The broker denies the child's merge call whatever the deployer definition allows and whatever another `deployer` instance may do. The harness performs the same intersection today; the broker computes it independently. Existing harness delegation restrictions remain in force.

The intersection narrows a child's definition but trusts the registered parent link: a compromised mecak8s that registers a child under a more privileged parent still inflates its effective ceiling (see [registering instances](agent-definitions.md#registering-instances)). Task-specific limits, such as keeping the reviewer to PR 42 under `D42`, are dynamic delegation policy and remain deferred. An instance JWT-SVID identifies the agent; it does not prove those limits.

## Operations and resources

V1 distinguishes registered MCP servers and tools, but does not evaluate resource or argument policies. Permission to call a tool is not evidence of authorization for a particular repository or PR. User, resource, argument, and dynamic delegation policies are deferred; v1 does not provide comprehensive enterprise user-policy enforcement.

For example, a reviewer whose resolved ceiling includes `pull_request_read` on the registered payments server can pass the ceiling check for a read, but cannot pass it for `pull_request_merge` if merge is excluded. Reading PR 42 and reading PR 43 with that same server/tool pair produce the same ceiling result. The broker's v1 Cedar check cannot establish that the reviewer stayed within Alice's PR-42-only request. Existing connection and downstream checks still apply to either call.

Agent-instance identity does not prevent a prompt-injected agent from misusing operations within its permitted authority. The broker constrains protected MCP calls through independent authorization checks; it does not establish that an allowed call faithfully serves the user's intent or protect the entire agent loop.

## User reports and the authorized request

Mecak8s reports the initiating user from the verified session owner. The broker authenticates the Mecak8s workload and relies on its user report under an explicit trust relationship; it does not independently authenticate the human from that report. Model-generated arguments must not choose or override the reported user.

The broker checks the actual MCP server/tool pair against the ceiling and retains existing checks on the resolved provider connection. The final request, including arguments, must remain bound to those checks and dispatch, but recording or binding arguments does not mean v1 Cedar authorizes their resource-specific meaning.

Request transformations must occur before the applicable checks, or the changed request must be checked again. Dispatch must use the checked request and provider connection.

## Two requests, two authorization decisions

In the [interactive walkthrough](credentials-and-lifecycle.md#interactive-execution), Alice owns session `S42`. The main instance lists pull requests, then delegates review of PR 42 to a reviewer. The tool names and arguments are illustrative, not a selected MCP server contract.

| Request | V1 effective-ceiling check |
| --- | --- |
| Main run `R41`: `pull_request_list(repository=acme/payments)` | The main's effective ceiling, which is its definition ceiling, must permit the resolved server and list tool. Main has no implicit exemption. |
| Reviewer run `R42` via `D42`: `pull_request_read(repository=acme/payments, number=42)` | The reviewer's effective ceiling, its definition ceiling intersected with the main's, must permit the resolved server and read tool. `D42` records the delegated task but is not a v1 Cedar policy input. |

Both requests also require valid credentials and execution-context checks, and must pass existing local, connection, and ToolHive checks. The reviewer's SVID identifies the child; even with creation ancestry in its subject, it does not prove authority under `D42`. The child presents its own credential, not the parent's SVID. Future broker enforcement of task-specific delegated bounds will need trusted evidence of those bounds.

For both calls, connection access includes authorization for Alice to use the ToolHive subject and provider account behind the connection. The walkthrough assumes this association for `github-conn-42`; successful OAuth or matching account names cannot establish it. The [association mechanism](credentials-and-lifecycle.md#provider-connection-selection) remains unresolved.

## Authorization, credentials, and dispatch

Local permissions and hooks apply before invocation. The broker then authenticates the caller and actor, checks execution context, and evaluates the final operation and connection. Credential readiness determines whether an eligible request can proceed, not whether it is authorized.

| Outcome | Dispatch behavior |
| --- | --- |
| Denial | Invalid credentials or context, or denied policy, dispatch no backend operation. |
| Enrollment required | Missing enrollment or required user reauthorization dispatches nothing. mecak8s pauses and presents the broker's enrollment link. After completion, the pending request retries through all checks; enrollment does not imply permission to execute. |
| Allow and credentials ready | The broker dispatches only the exact authorized request using the authorized connection. The allow covers this call, not later calls, spawning, or run execution. |

Missing enrollment can leave authorization incomplete. Refreshable expiry is handled in credential custody where possible; otherwise the broker requires user reauthorization. Missing, invalid, stale, or revoked identity, connection, or authorization evidence must fail closed, without fallback to a service credential, another user, or a more privileged agent.

When needed, the broker obtains a current ToolHive user access token and exchanges it with the acting instance's SVID. ToolHive's authorization server independently validates the user and actor tokens and broker client, and authorizes the requested target and access bounds. The protected vMCP independently validates the delegated token, applies operation policy, and resolves the authorized connection before using ToolHive's native provider credential. Neither downstream check replaces the broker's exact-operation decision. Credentials stay in their designated custody, outside model history; see [credential roles and custody](credentials-and-lifecycle.md#credential-custody).

Cached credentials are not cached authorization. Every subsequent call repeats the effective-ceiling evaluation and existing connection and downstream checks; any delegated-token reuse must stay within the execution context for which it is permitted.

An allow does not prove dispatch or provider success. The [audit contract](audit.md#decision-dispatch-and-observed-outcome) owns durable evidence and correlation requirements. An unknown outcome must not be automatically replayed merely because credentials were recovered.

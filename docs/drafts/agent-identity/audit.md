# Audit

*This chapter describes the intended architecture, not current implementation status. The audit requirement applies to tool calls generally; the current design scope is broker-mediated MCP calls.*

The audit trail must explain which registered instance requested a call, which definition-derived ceiling the broker evaluated, whether dispatch was attempted, and what result each observer received. A v1 Cedar permit establishes only the [server/tool ceiling check](mcp-authorization.md#definition-derived-ceiling), not PR-specific delegated authority or user entitlement.

## Evidence and provenance

Records must distinguish verified facts from trusted reports. Qualify subjects by issuer or configured trust domain; a workload subject alone is not globally unique.

| Evidence | Source and what it establishes | Limit |
| --- | --- | --- |
| Authenticated workload and instance | Broker validation of workload credentials and the instance JWT-SVID, with the registered session association | Does not authenticate the human or prove a task-specific delegation. |
| Definition source, admitted revision, ceiling, and instance ancestry | Workload-authenticated mecak8s lifecycle registration, validated and retained by the broker | A trusted lifecycle assertion, not a fact carried by the SVID or proof of same-process isolation. |
| Initiating user and session ownership | API authentication and server-side ownership checks, reported to the broker | The broker trusts the API report; it does not independently authenticate the human from it. |
| Run and execution-delegation links | Attributed lifecycle/run reports under the execution-context binding | Correlation is not proof that v1 Cedar enforced delegated resource bounds. Creation ancestry is separate. |
| Ceiling evaluation and effective broker decision | Broker evaluation of the registered actor's generated policy and the exact server/tool pair, plus applicable existing checks | An allow proves neither dispatch nor downstream success. |
| Downstream identity and authorization | ToolHive AS and vMCP records of their own validations and decisions | Forwarded context remains reported unless independently verified there. |
| Observed result and provider correlation | The response received by the recording component and safe provider references, when available | A broker-observed MCP response is not independent proof of provider-side effects. |

## Illustrative authorization evidence

The following readable fields illustrate a proposed broker authorization event for the reviewer's call in the [interactive walkthrough](credentials-and-lifecycle.md#interactive-execution). They are an audit shape to guide implementation, not an immutable event API or a Cedar policy representation.

| Field group | Illustrative values or required evidence |
| --- | --- |
| Event and call references | Event `authz-42`, operation `op-pr42-001`, attempt `attempt-1`, recording component, and observation time. Distinguish another attempt from another event for the same attempt. |
| Actor and context | Verified reviewer subject from its registered instance; verified workload with trust qualifier; session `S42` and its incarnation; run `R42`; creation-parent reference; reported delegation `D42` from `R41`; API-reported Alice with issuer/subject and request reference. Preserve the provenance above. |
| Definition and policy | Registered `project/code-reviewer`, source reference, admitted revision `review-def-r17`, resolved ceiling reference, generated policy revision `review-ceiling-r17-g1`. These come from registration, not request-selected policy. |
| Target | Registered payments MCP server reference, exact `pull_request_read` tool, authorized connection `github-conn-42`. |
| Evaluation | Cedar nominal decision `allow`, evaluation status `completed-without-errors`, relevant policy IDs, and generated policy revision. |
| Effective decision | `allow` only after all required checks pass; safe reason `definition-ceiling-permits-pair-and-required-checks-passed`. An evaluation diagnostic error instead requires effective `deny`, even with a nominal Cedar allow. Record a safe error category, not unrestricted diagnostics. |
| Safe request metadata | Repository `acme/payments` and PR number `42` only if explicitly allowlisted for audit. Their presence does not mean Cedar evaluated a PR-specific policy. |

The policy revision and policy IDs must lead to retained generation inputs and policy evidence sufficient to explain the ceiling result. A definition name alone cannot explain which policy was used. Error and denial records need the same correlation and attribution where established; unavailable or unverified facts must remain marked as such.

## Decision, dispatch, and observed outcome

The broker records its authorization decision, whether it attempted dispatch, and the outcome it observed as separate facts. An allow is not a dispatch; a dispatch attempt is not proof of receipt or success. A denial dispatches no backend operation. If completion is missing after possible dispatch, the outcome is unknown, not evidence of nonexecution. Credential recovery cannot justify automatic replay of an unknown operation.

Decision, dispatch, and observed-outcome evidence must be durable and survive broker restart. The storage mechanism, write ordering relative to dispatch, failure handling when evidence cannot be persisted, and cross-service delivery and authenticated correlation remain explicitly unresolved implementation requirements. This design does not select a transactional ledger or claim atomicity between an audit write and an external effect.

Use operation and attempt references to connect the evidence; individual events also need their own references. A token identifier such as `jti`, when present, is supplementary because several calls can reuse one token. This correlation requirement does not establish an authenticated propagation contract between services.

## Following a call in an investigation

Starting from `op-pr42-001`, an investigator follows each attempt to the broker decision, the registered actor and fixed definition revision, and the generated policy evidence. The decision explains why the server/tool pair passed or failed and whether any other required check prevented dispatch. Run `R42` and delegation `D42` lead back to main run `R41` and the API request, with reported user and lifecycle facts distinguished from broker-verified credentials.

The investigator then follows dispatch and observed-outcome records for that attempt, rather than inferring execution from the allow. ToolHive records can explain downstream validation or denial. Where the provider supports safe tracing or audit correlation, retain the references needed to reach its operation or audit record and to trace that record back to the actor. Mark where correlation ends if provider evidence or authenticated cross-service correlation is unavailable. A successful response describes what the observer received, not a guarantee of provider-side effects.

## Redaction and audit-store protection

Record only allowlisted argument and result metadata needed for attribution, with redaction applied before persistence or logging. Safe status categories and nonsecret provider references usually suffice for outcomes. Exclude tokens, refresh tokens, private keys, client secrets, raw headers, OAuth transaction secrets, and unrestricted argument or result payloads. Policy diagnostics and error text need the same treatment; dumping decoded credentials is not an audit strategy.

Protecting persisted evidence against unauthorized disclosure or modification requires audit-store access and integrity controls. Recording authenticated identities does not itself protect the store. The [credential walkthrough](credentials-and-lifecycle.md#interactive-execution) owns credential validation and OAuth examples; this chapter specifies the evidence needed to investigate their use.

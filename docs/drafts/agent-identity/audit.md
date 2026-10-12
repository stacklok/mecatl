# Audit

*This chapter describes the intended architecture, not current implementation status. The audit requirement applies to tool calls generally; the current design scope is broker-mediated MCP calls.*

## Following a tool call and its result

An audit record must connect the initiating user, authenticated workload, acting agent instance, definition source and revision, and the authorized provider connection used, where applicable, to the requested tool call and its result. It must preserve the session incarnation, run, creation ancestry, and separate execution-delegation links needed to trace how that instance came to act. Qualify subjects by their issuer or configured trust domain; a workload subject alone is not globally unique.

Use a per-operation correlation identifier across decision, dispatch, and outcome records. This is an audit requirement, not an established cross-service join or authenticated propagation contract; those mechanisms remain unspecified. A token identifier such as `jti`, when present, is supplementary: multiple operations can reuse one token. Preserve the target, tool, safe operation/resource metadata, policy and delegation references, and authorized connection needed to explain the decision.

Each observer must distinguish what it verified from what it received as a report. API authentication establishes the company user at the API; the broker verifies the workload but trusts the API's user report. ToolHive AS and vMCP record the identities and decisions they validated themselves. Propagated run context or provider responses do not become independently verified facts merely because they appear in an audit record.

The broker records its authorization decision, whether it attempted dispatch, and the outcome it observed. An allow is not a dispatch; a dispatch attempt is not proof of receipt or success. A denial dispatches no backend operation. Decision, dispatch, and observed-outcome evidence must be durable and survive broker restart. This requirement does not select a persistence ordering or cross-service delivery protocol. A missing completion after possible dispatch is an unknown outcome, not evidence that no action occurred. Observed success does not guarantee provider-side effects.

Where an external SaaS provider supports tracing or audit correlation, the integration must preserve the identifiers needed to connect the broker's record to the provider's operation or audit event. This allows an investigation to follow an action into the provider, or trace a provider event back to the initiating user and agent chain.

End-to-end correlation depends on the provider's capabilities and configuration; the system cannot guarantee it for every external tool. When that evidence is unavailable, the audit trail must make clear where correlation ends.

Credentials must not appear in audit records or logs: exclude serialized tokens, refresh tokens, private keys, client secrets, authorization headers, and OAuth transaction secrets. Record selected validated metadata and nonsecret record references instead. Redact sensitive tool arguments and results while retaining sufficient safe operation attribution; dumping decoded credential payloads is not an audit strategy.

The [PR 42 credential walkthrough](credentials-and-lifecycle.md#interactive-execution) places credential examples, validation checks, and selected audit evidence alongside each producing step.

## Additional audit hardening

Protecting persisted audit evidence against unauthorized disclosure or modification is additional work beyond this identity and authorization design. That work must address access to the stored records and their integrity after collection; recording authenticated identities does not itself protect the audit store.

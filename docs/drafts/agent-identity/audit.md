# Audit

*This chapter describes the intended architecture, not current implementation status. The audit requirement applies to tool calls generally; the current design scope is broker-mediated MCP calls.*

## Following a tool call and its result

An audit record must connect the initiating user, authenticated workload, acting agent instance, definition source and revision, and the authorized provider connection used, where applicable, to the requested tool call and its result. It must preserve the run and delegation links needed to trace how that instance came to act.

The broker records its authorization decision, whether it dispatched the operation, and the outcome it observed. Audit records must survive broker restart. The record distinguishes broker-verified facts from harness assertions and provider-reported results. A missing completion is an unknown outcome, not evidence that no action occurred.

Where an external SaaS provider supports tracing or audit correlation, the integration must preserve the identifiers needed to connect the broker's record to the provider's operation or audit event. This allows an investigation to follow an action into the provider, or trace a provider event back to the initiating user and agent chain.

End-to-end correlation depends on the provider's capabilities and configuration; the system cannot guarantee it for every external tool. When that evidence is unavailable, the audit trail must make clear where correlation ends.

Credentials must not appear in audit records.

## Additional audit hardening

Protecting persisted audit evidence against unauthorized disclosure or modification is additional work beyond this identity and authorization design. That work must address access to the stored records and their integrity after collection; recording authenticated identities does not itself protect the audit store.

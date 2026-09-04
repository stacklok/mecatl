# ADR 0295 — Safe HTTP rejection display evidence

- Status: Accepted
- Date: 2026-09-03
- Scope: user-visible provider HTTP rejection errors
- Supersedes: ADR 0203's user-display exclusion only
- Superseded by: —

## Context

The provider adapters intentionally kept SDK errors unwrap-only because their default
strings can contain a raw response body, headers, request URL, credentials, or opaque
provider data. ADR 0203's permanent-error presentation consequently excluded request
targets and correlation IDs. That leaves an operator unable to distinguish a rejected
configured endpoint from a provider request failure without turning to unsafe raw SDK
diagnostics.

## Decision

For a structured HTTP/API rejection only, append a display-safe actual request target
and at most one validated opaque provider request/correlation ID to the existing
user-visible error text. The target retains only `http` or `https`, host, optional valid
port, and cleaned escaped path. Userinfo, query, and fragment are removed. IDs are
bounded ASCII opaque tokens and are omitted when invalid.

`port.AppendHTTPErrorDisplay` owns this narrow, stdlib-only projection so independently
versioned provider modules use the same rule. The original SDK error remains
unwrap-visible and no retry, status, or classification metadata changes. In-band SSE
errors do not invent HTTP target or ID evidence.

Raw response bodies, headers, arbitrary URLs, prompts, credentials, and unvalidated
identifiers remain prohibited from user-visible errors and durable attempt evidence.

## Consequences

A terminal HTTP rejection can identify the safe endpoint and a support correlation
handle without requiring raw SDK output. Missing or malformed target/ID data simply
produces the existing message. The exception is intentionally limited to current
structured HTTP errors; it is not a general error-dump facility.

## See also

- [ADR 0203](./0203-permanent-provider-error-signal.md)
- [ADR 0239](./0239-semantic-stream-retry.md)
- [ADR 0255](./0255-sanitized-network-attempt-evidence.md)
- [Architecture overview](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
- [TUI guide](../tui.md)

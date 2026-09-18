# ADR 0348 - Cause-free TypeScript SDK malformed-success decoding

- Status: Proposed
- Date: 2026-09-18
- Scope: the TypeScript SDK's shared HTTP unary and SSE successful-response
  decoding boundary
- Supersedes: none
- Superseded by: none

## Context

The TypeScript SDK's HTTP transport decodes successful unary response bodies and
ordinary SSE data frames in two stages. It first parses JSON, then normalizes
descriptor-declared well-known types and decodes the value with protobuf-es.
Failures at either stage become the existing `ProtocolError` with HTTP status
and transport metadata.

Those errors currently retain the native JSON or protobuf decoder exception as
`Error.cause`. Decoder messages differ among JavaScript runtimes and can include
an excerpt of the rejected response. An application logger that recursively
captures causes can therefore copy untrusted response content, including an
ephemeral presentation URL, into its logs. The SDK does not log these errors,
and `MecatlError.toJSON()` already omits causes, but neither fact controls how an
application logs an `Error` instance.

The neighboring causes have different semantics. A parsed non-2xx RFC 9457
problem is the server's typed rejection and remains useful to the application.
A fetch failure or abort is the transport failure itself. Removing those causes
would discard established diagnostics without addressing malformed successful
payloads.

## Decision

1. Once a successful unary response body has been acquired, the shared HTTP
   transport creates a cause-free `ProtocolError` when its JSON parsing,
   well-known-type normalization, or protobuf-es decoding fails. Ordinary SSE
   data-frame JSON and protobuf decoding follows the same boundary.
2. These errors retain the existing generic SDK-authored message, `protocol`
   code, `http` transport, HTTP status, and the response's `X-Request-ID` when
   present. They never copy the decoder message or rejected value into the
   error.
3. The SDK does not attempt best-effort redaction of arbitrary decoder messages.
   Cause-free construction is single-sourced inside the shared HTTP transport,
   but private helper and call-site topology are not part of the durable public
   contract.
4. A failure while acquiring the unary response body after headers remains a
   caused `ProtocolError`; it is a transport/read failure rather than a decoder
   rejection. SSE reader failures likewise remain outside the decoder boundary.
5. A successfully parsed SSE frame whose event type is `error` continues through
   `errorFromProblem`. Non-2xx problem normalization, credential-provider and
   HTTP authentication errors, fetch and network `TransportError` causes, abort
   behavior, and stream/control lifecycle semantics remain unchanged.
6. The public `ProtocolError`, `MecatlErrorOptions`, and transport method
   signatures do not change. This is an intentional compatibility narrowing of
   diagnostic detail for malformed successful HTTP and SSE payloads.

## Consequences

Application loggers cannot recover rejected successful-response content by
walking `ProtocolError.cause` at this boundary. HTTP status and response request
IDs remain available for correlation when supplied, and the generic message
still distinguishes JSON parsing from response or event decoding.

Runtime-specific decoder detail is no longer available on these four errors.
Applications that need to inspect a malformed upstream payload must capture it
at an explicitly secured transport or server boundary rather than through the
SDK error object.

Typed server rejections, authentication failures, and transport/body-read
failures keep their established causes. This makes the policy depend on whether
a value was rejected by the successful-payload decoder or by an adjacent error
or transport boundary, rather than merely on which API threw it.

## See also

- [Issue #1694](https://github.com/stacklok/mecatl/issues/1694)
- [Malformed-success decoding acceptance plan](../acceptance/sdk-malformed-success-decoding.md)
- [ADR 0248 - SDK compatibility discovery and the typed error contract](./0248-sdk-compatibility-and-error-contract.md)
- [ADR 0279 - TypeScript SDK architecture](./0279-typescript-sdk-architecture.md)
- [TypeScript SDK architecture](../architecture.md#typescript-sdk)

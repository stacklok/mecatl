# `@stacklok/mecatl-sdk`

The TypeScript SDK for the [mecatl](https://github.com/stacklok/mecatl) agentic coding
harness. The package is ESM-only and supports Node.js 24 or newer.

This first milestone establishes the package and its public entry points:

- `@stacklok/mecatl-sdk` — transport-neutral core and the browser HTTP/SSE transport;
- `@stacklok/mecatl-sdk/node` — Node/Bun gRPC transport and local-process features;
- `@stacklok/mecatl-sdk/gen` — protobuf-es types and service descriptors.

## Durable attachment

`session.attach(runId?)` follows one run, while `session.activity()` follows the
cross-run durable timeline. Both expose a serializable cursor for application-owned
checkpoint persistence. A checkpoint advances when the consumer requests the next
envelope, so reconnecting from the exposed cursor can redeliver the last processed
envelope; consumers with side effects must therefore be idempotent.

The guarantee is ordered, at-least-once delivery **for durably appended events**.
It is deliberately not absolute: if the event-log backend is totally unavailable and
the process recording the event is lost, the failed append can leave an undetectable
gap. When a gap is detectable, the raw watch reports `{ kind: "gap" }`; the ergonomic
attachment instead ends with `ActivityGapError` and leaves its cursor at the last
envelope before the gap. `CursorExpiredError` also ends the attachment and requires the
caller to choose an explicit restart from the beginning or a transcript reload.

## Development

Run the SDK gates from the repository root:

```sh
task sdk:install
task sdk:lint
task sdk:typecheck
task sdk:test
task sdk:build
task sdk:api:check
task sdk:pack
```

The package is licensed under Apache-2.0.

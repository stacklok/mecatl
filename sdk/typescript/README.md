# `@stacklok/mecatl-sdk`

The TypeScript SDK for the [mecatl](https://github.com/stacklok/mecatl) agentic coding
harness. The package is ESM-only and supports Node.js 22 or newer.

This first milestone establishes the package and its public entry points:

- `@stacklok/mecatl-sdk` — transport-neutral core and the browser HTTP/SSE transport;
- `@stacklok/mecatl-sdk/node` — Node/Bun gRPC transport and local-process features;
- `@stacklok/mecatl-sdk/gen` — protobuf-es types and service descriptors.

The entry points are scaffolds in Scenario 1. Proto generation and client behavior land in
the following SDK slices.

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

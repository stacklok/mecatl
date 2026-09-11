# ADR 0334 - Deno uses the TypeScript SDK HTTP/SSE entry point

- Status: Accepted
- Date: 2026-09-11
- Superseded by: [ADR 0335](./0335-typescript-sdk-deno-command.md), for the public entry-point and
  local-process decisions.
- Scope: `sdk/typescript/`, its npm package metadata, emitted declarations, runtime matrix,
  release verification, and public documentation.
- Supersedes: [ADR 0279](./0279-typescript-sdk-architecture.md), only for the supported-runtime
  set. Its transport and package architecture remain unchanged.

## Context

The TypeScript SDK already separates its web-platform HTTP/SSE implementation in `.` from the
Node/Bun gRPC, process, filesystem, and callback-tool implementation in `./node`. That makes the
transport-neutral entry point executable in Deno without a new transport or server protocol.

Execution alone is not a support contract. Deno does not apply TypeScript's adjacent `.d.ts`
resolution rule to JavaScript modules. The package's NodeNext output therefore runs in Deno but a
stable `deno check` loses the type-only exports when one declaration follows a relative `.js`
specifier. Deno's unstable sloppy-import resolution hides the problem, but requiring an unstable
consumer flag would make the package, rather than the SDK artifact, own type correctness.

The Node/Bun entry point has a different boundary. It relies on `@connectrpc/connect-node`, Unix
domain sockets, inherited process behavior, child-process lifetime signaling, filesystem media
helpers, and a loopback callback host. Deno's Node compatibility layer can load parts of that graph,
but importability does not qualify those lifecycle and security contracts.

## Decision

**1. Support Deno 2.9.3 through the Deno 2 major for `.` and `./gen`.** Deno applications import
the npm package and connect to an operator-owned daemon through the existing HTTP/JSON/SSE
transport. The package declares `engines.deno` as `>=2.9.3 <3`. Deno 3 requires a later explicit
qualification.

**2. Keep `./node` Node/Bun-only.** Deno support does not include real gRPC, local `spawn()` or
`query()`, path media helpers, callback-tool hosting, Unix domain sockets, or daemon lifetime
ownership. No Deno-specific public subpath or second client API is added.

**3. Give every emitted JavaScript module an explicit Deno type slot.** After `tsc` emits the
unbundled package, the build prepends the stable `@ts-self-types` directive naming that module's
sibling declaration. The same build inserts one unmapped source-map line, preserving the original
generated mappings. A package test requires the directive, declaration, and adjusted source map for
every shipped JavaScript module, including generated protobuf modules.

**4. Qualify the package at the floor and current Deno 2.x.** CI runs stable `deno check` with no
unstable flags and a real HTTP/SSE session lifecycle against the same-checkout mock daemon at the
declared floor and current Deno 2.x. The SDK release workflow repeats the floor qualification before
publishing its inspected npm artifact.

## Consequences

- Deno shares the existing client, event, error, media, and generated protobuf types with browser
  and Node/Bun callers. There is no transport fork to maintain.
- Emitted JavaScript gains one Deno-only comment and one unmapped source-map line. Node, Bun, and
  browser execution remain unchanged.
- Deno applications need only network permission for ordinary remote use. The integration test has
  additional read, write, and run permissions because it owns the offline fixture daemon.
- Deno Deploy and other edge runtimes remain unqualified. Their platform-specific networking,
  execution limits, and deployment packaging need separate evidence.

## See also

- [TypeScript SDK architecture](./0279-typescript-sdk-architecture.md)
- [TypeScript SDK public surface and v0.1.0 release](./0304-typescript-sdk-public-surface-and-release.md)
- [TypeScript SDK Deno support acceptance plan](../acceptance/sdk-typescript-deno.md)
- [TypeScript SDK architecture](../architecture.md#typescript-sdk)

# TypeScript SDK Deno runtime support - acceptance plan

**Contract:** human-reviewed/v2
**Phase:** TypeScript SDK runtime compatibility after v0.1.0
**Release scope:** Excluded from v0.1.0. A later release version is not yet assigned.
**Status:** landed
**Work classification:** Architectural - this expands the SDK's durable public runtime and local
process ownership contract.
**Decision record:** [ADR 0335](../adr/0335-typescript-sdk-deno-command.md)
**Delivery:** Split is the normal requirement for this public-interface change; this PR uses the
directing-user exception below, not the workflow-only Combined exception.
**Delivery exception:** Implementation and acceptance documentation are presented together at the
directing user's request to implement native `Deno.Command` support and open the implementation
PR. There is no separately merged Plan / Interface PR or approved commit baseline.
**Expected tasks:** 1
**Issue:** None assigned.

Support Deno applications that connect to an operator-owned daemon or own a local `mecated`
process through `Deno.Command`. Keep Deno on HTTP/SSE while preserving the Node/Bun boundary for
real gRPC, path media, and callback tools.

## Human decisions

- [x] Release scope — Decision: defer Deno support beyond v0.1.0 at the directing user's request. Do not include this integration in the v0.1.0 release; select its later release version separately.
- [x] Runtime and entry-point scope — Decision: support `.`, `./gen`, and Deno-native `./deno`; `./deno` adds local `spawn()` and `query()` without importing `./node`.
- [x] Local transport and lifecycle — Decision: use `Deno.Command`, loopback HTTP/SSE, piped-stdin parent-liveness EOF, bounded signal fallbacks, and private-directory cleanup.
- [x] Compatibility floor — Decision: support Deno `>=2.9.3 <3`, test the exact floor and current Deno 2.x, and require a new qualification before claiming Deno 3.
- [x] Declaration strategy — Decision: retain NodeNext output, add stable `@ts-self-types` directives with source-map adjustment, and require no unstable Deno resolution flag.

## Interface contract

- **gRPC / protobuf:** None — a Deno-owned daemon uses the existing gRPC listener and HTTP/JSON/SSE
  routes. The Deno client communicates only through HTTP/SSE.
- **Exported Go APIs / interfaces:** None — the change adds no exported Go symbol or interface.
- **Tool schemas:** None — Deno-spawned TCP clients do not receive client-provided MCP authority and
  expose no callback-tool registration API.
- **CLI / config:** `--lifetime-stdin` is mutually exclusive with `--lifetime-pipe-fd`. It is an
  SDK hosting primitive rather than an operator configuration default. `task sdk:deno` is the
  repository qualification gate.
- **Events / persistence:** None — Deno receives the existing event union and durable-watch cursors
  without changing stored records.
- **Security / authority:** Deno enforces run, read, write, and loopback network permissions. The
  SDK invokes no shell, reserves its listener and lifetime arguments, validates the ready document
  against the captured child, bounds and redacts stderr, and signals only that child handle.
- **Compatibility / migration:** Add `engines.deno: ">=2.9.3 <3"` and the `./deno` package export.
  Existing Node, Bun, browser, and remote Deno imports require no migration.

### Scenario 1 - A typed Deno application owns and uses a local daemon

A Deno application imports the packed npm package, starts `mecated` with `Deno.Command`, completes
a run over HTTP/SSE, follows the run through durable attachment, and disposes the owned process and
runtime directory without Node compatibility or unstable flags.

This scenario enforces the runtime and process-ownership boundary in
[ADR 0335](../adr/0335-typescript-sdk-deno-command.md).

**Acceptance:**

- AC1.1: Stable `deno check` resolves all public types from `.`, `./deno`, and `./gen` without
  `--unstable-sloppy-imports`. Every emitted JavaScript module names its sibling declaration, and
  its source map begins with one unmapped line for the inserted directive.
  - verify: vitest:sdk/typescript/test/package.test.ts#ZXZlcnkgSmF2YVNjcmlwdCBtb2R1bGUgZGVjbGFyZXMgaXRzIERlbm8gdHlwZSBzbG90IHdpdGhvdXQgc2hpZnRpbmcgbWFwcGluZ3M
- AC1.2: At Deno 2.9.3 and current Deno 2.x, `spawn()` uses the same-checkout binary, validates its
  ready document, completes an HTTP/SSE run, observes the same terminal through durable attachment,
  loads a generated service descriptor, and removes the daemon runtime after close. Deno `query()`
  repeats the spawn-create-run-cleanup lifecycle.
  - verify: vitest:sdk/typescript/test/examples.test.ts#dGhlIERlbm8gZ2F0ZSBydW5zIHRoZSBsb2NhbCBEZW5vLkNvbW1hbmQgbGlmZWN5Y2xl
- AC1.3: The package declares the bounded Deno engine range and `./deno` export, CI tests the floor
  and current 2.x, release verification repeats the floor integration, and the repository task runs
  stable check plus the real integration.
  - verify: TestTypeScriptSDKDeno_Scenario1_RuntimeMatrix
- AC1.4: The public guide gives runnable remote and local Deno paths with the required permission
  flags. It keeps real gRPC, path media, and callback tools under Node/Bun.
  - verify: vitest:sdk/typescript/test/examples.test.ts#dGhlIERlbm8gZXhhbXBsZXMgY292ZXIgcmVtb3RlIGNvbm5lY3QgYW5kIERlbm8uQ29tbWFuZC1iYWNrZWQgbG9jYWwgc3Bhd24

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Deno use of `./node` | None planned | Deno has a native `./deno` process adapter |
| Real gRPC, UDS, path media, and callback tools in Deno | Separate proposal | These require independent transport or authority designs |
| Deno Deploy and other edge runtimes | Separate proposal | A Deno CLI proof does not establish an edge deployment contract |

## Definition of done

1. `task sdk:deno`, `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:api:check`,
   `task sdk:pack`, and `task sdk:package:check` pass.
2. CI and the TypeScript SDK release workflow run the Deno gate at the declared versions.
3. `task docs`, `task site:build`, and `task ac-trace-strict` pass.
4. The npm package, architecture guide, SDK README, examples, and user guide state the same runtime
   and entry-point boundary.

## Deferred decisions and known risks

Deno's npm compatibility loads the package dependencies without a separate bundle. Dependency
upgrades can introduce a runtime-specific assumption, so the real-wire floor/current matrix remains
a release gate. Deno's process API has no arbitrary child-descriptor mapping; the explicit piped
stdin hosting contract is therefore part of compatibility. The Deno 2.x lane does not predict Deno
3 or any edge runtime's deployment restrictions.

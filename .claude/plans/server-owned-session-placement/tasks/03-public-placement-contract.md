---
id: 03-public-placement-contract
title: Path-free Harness and HTTP placement contract
blocked_by: [01-placement-foundation, 02-durable-placement-state]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Perform the coordinated breaking public-contract migration. Define the tagged three-variant placement selector and exact placement-ref/safe-metadata projections in the Harness protobuf, then regenerate bindings. Remove workspace/cwd/root/mount/path-shaped selectors and projections from create, session inventory/get, discovery, team/direct delegation, fork, and related Harness/HTTP DTOs; add the path-free `ClearSession` message/RPC shapes needed by the successor task. Route gRPC and HTTP creation through one server create method that invokes the foundation `Bind`, persists only after binding succeeds, and returns only the exact ref plus bounded safe metadata. Old path-bearing requests must be absent/unsupported, not ignored or translated. Add a structural contract guard over source descriptors/generated clients/HTTP DTOs.

Expected focus: `contracts/proto/mecatl/v1`, regenerated `contracts/gen`, `internal/adapter/server/grpc.go`, `http.go`, contract mappers/DTOs, and creation-boundary tests. Define later consumer wire shapes here to avoid multiple generated-file writers; do not implement discovery, clear/fork behavior, scheduling, ACP, or docs.

## Acceptance criteria

- AC1.1: `CreateSession` accepts only the three tagged selector variants. Omission binds
the deployment default; no-FS attenuates it; an ID is reauthorized. Its response exposes
only `PlacementRef` and safe metadata.
  - verify: `TestADR_0280_CreateSessionPlacementProtocol`
- AC1.4: Harness protobuf/HTTP requests and responses, generated clients, and public
session inventory contain no filesystem path, cwd, workspace root, mount, or
path-shaped environment selector. Old path-bearing requests are unsupported.
  - verify: `TestADR_0280_PublicHarnessContractContainsNoFilesystemPaths`

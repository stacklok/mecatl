# Changelog

Notable changes to `@stacklok-oss/mecatl-sdk` are recorded here.

For installation and API entry points, see the [TypeScript SDK README](./README.md).

## Unreleased

- Added `session.controls(runId)`: strict, run-id-addressed HTTP controls
  (`resolveAsk`, `cancel`, `steer`, `cancelSteer`) that need no event stream,
  so a client can act on a run it re-attached to or observes through a durable
  watch. `steer` returns the server's `accepted` / `appended` / `too_late`
  outcome and accepts a client-minted `messageId` and media parts; a control
  that outlives its run surfaces as `ServerError` `stale_run_control`.
- The HTTP transport now decodes `google.protobuf.Timestamp` and
  `google.protobuf.Duration` fields the daemon marshals with stdlib
  `encoding/json` (`{"seconds", "nanos"}` objects) — learning proposals,
  learned skills, dream plans, schedule specs and fires, and session
  snapshots no longer fail with `ProtocolError` against a real daemon.
## [0.2.0](https://www.npmjs.com/package/%40stacklok-oss%2Fmecatl-sdk/v/0.2.0)

- fix(sdk): align Biome schema version (#1426) ([`ebb14c7`](https://github.com/stacklok/mecatl/commit/ebb14c78823946909a5ec2957d42b485bcef1e96))
- feat(sdk): automate reviewed npm release tags (#1425) ([`ca5afe4`](https://github.com/stacklok/mecatl/commit/ca5afe43a0f24fc9b3ef87986b6fcfb5be537f17))
- fix: restore selected-model image capabilities (#1479) ([`8208ae4`](https://github.com/stacklok/mecatl/commit/8208ae404aaab44f8b044667198a2bb8ee05dd64))
- feat(redisstore): isolate bounded follow capacity and migrate to Go 1.27 (#1480) ([`bd7a4d5`](https://github.com/stacklok/mecatl/commit/bd7a4d5eb148c89b87327db5075d22c71e7d55de))
- feat(sdk): add native Deno integration with shared gRPC (post-v0.1.0) (#1423) ([`b1ec0ae`](https://github.com/stacklok/mecatl/commit/b1ec0ae63b7f6fdf6b29611f25ea7ff26133ed5b))
- feat(sdk): generate release changelog entries (#1482) ([`f1d8359`](https://github.com/stacklok/mecatl/commit/f1d83593c29b3080777966d84216ed8997eea31a))
- feat: add draft-aware session inventory (#1491) ([`29841c4`](https://github.com/stacklok/mecatl/commit/29841c4610e98b4706dbd3965b757ae426ad6559))
- feat(sdk): expose session lifecycle surface (#1542) ([`5828a72`](https://github.com/stacklok/mecatl/commit/5828a726350468d2bf6b662edb7cede238632104))
- feat: distinguish synthetic prompts in session replay (#1504) ([`fb05301`](https://github.com/stacklok/mecatl/commit/fb05301c45136c4b03c4892360adb46d2ff7930f))
- fix: prevent premature compaction on cold gateway resume (#1546) ([`28eb283`](https://github.com/stacklok/mecatl/commit/28eb283ff413113268aed0391a4f0ca9a01d723c))
- feat(sdk): expose server discovery (#1559) ([`f21089b`](https://github.com/stacklok/mecatl/commit/f21089b9e44a72b76622f1972f5704629cac26c5))

[Compare changes](https://github.com/stacklok/mecatl/compare/sdk/typescript/v0.1.0...sdk/typescript/v0.2.0)

## [0.1.0](https://www.npmjs.com/package/%40stacklok-oss%2Fmecatl-sdk/v/0.1.0)

- Initial public npm release.

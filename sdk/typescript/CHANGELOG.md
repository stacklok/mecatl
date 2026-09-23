# Changelog

Notable changes to `@stacklok-oss/mecatl-sdk` are recorded here.

For installation and API entry points, see the [TypeScript SDK README](./README.md).

## [0.4.0](https://www.npmjs.com/package/%40stacklok-oss%2Fmecatl-sdk/v/0.4.0)

- refactor(mecatui): prepare conversation cards functionally (#1744) ([`3aebbb5`](https://github.com/stacklok/mecatl/commit/3aebbb5eeb0450a501a6292f16c6e89c402f2c2f))
- refactor: remove obsolete alpha compatibility paths (#1725) ([`b0cf03a`](https://github.com/stacklok/mecatl/commit/b0cf03a423df907e07269e085105fda2dc81ea47))
- feat(router): add Jev delegated-model routing (#1738) ([`117f6d0`](https://github.com/stacklok/mecatl/commit/117f6d0fb65438aeafc9b53497f0f526b30c561b))

[Compare changes](https://github.com/stacklok/mecatl/compare/sdk/typescript/v0.3.0...sdk/typescript/v0.4.0)

## Unreleased

- **Breaking (alpha):** use canonical title metadata, typed approval verdicts, typed event usage and retry disposition, exact-run controls, and compatibility-info capabilities; remove storage migration and deprecated watch aliases.

## [0.3.0](https://www.npmjs.com/package/%40stacklok-oss%2Fmecatl-sdk/v/0.3.0)

- fix(mcp): authorize client-registered MCP tools in session capability set (#1627) ([`2f33adf`](https://github.com/stacklok/mecatl/commit/2f33adfc9cc2a8aa3a3502eff9a7216ab71fc6cf))
- feat(sdk): add run-ID-addressed controls (#1642) ([`0bf37d0`](https://github.com/stacklok/mecatl/commit/0bf37d08bfe0934ab5fa3caa1dd121624ca323b0))
- feat(provider): ask for the prompt cache through the protocol, not the vendor (ADR 0346) (#1573) ([`2eba2cc`](https://github.com/stacklok/mecatl/commit/2eba2cc2737a0362588520ab06a51c6da16c2544))
- feat(sdk): expose MCP workspace enrollment (#1690) ([`4c31f11`](https://github.com/stacklok/mecatl/commit/4c31f11e0b33ea276463df59a535bbeadc3ffda0))
- fix(sdk): decode daemon HTTP timestamp and duration objects (#1686) ([`d1b659c`](https://github.com/stacklok/mecatl/commit/d1b659c1fe99bb9d5b5d8486c04b00d8edfe7b5a))
- plan: TypeScript SDK MCP authorization lifecycle (#1687) ([`ad1cfe3`](https://github.com/stacklok/mecatl/commit/ad1cfe3c89a640905b88fb69f9905df498ba5c9c))
- slack-bot: DM manual permission approvals instead of auto-approving (#1707) ([`e14c88d`](https://github.com/stacklok/mecatl/commit/e14c88d2c0639382cb49129c44cb494650e37cf2))
- feat(sdk): add MCP authorization lifecycle (#1692) ([`5b0db5a`](https://github.com/stacklok/mecatl/commit/5b0db5a2608781f0c683f19864f8ecb8252969b8))
- chore(deps): bump the npm-minor-patch group in /sdk/typescript with 3 updates (#1715) ([`39715ed`](https://github.com/stacklok/mecatl/commit/39715ed6c584b577c1f62603a21c8e2fe089e3ab))
- slack-bot: relabel "Allow always" button to "Allow for session" (#1724) ([`9ac4798`](https://github.com/stacklok/mecatl/commit/9ac4798acb6e991365b678a7c469381765572522))
- fix(sdk): sanitize malformed-success decode errors (#1700) ([`44786d1`](https://github.com/stacklok/mecatl/commit/44786d1755c85900becd87587b19682c49d6485b))

[Compare changes](https://github.com/stacklok/mecatl/compare/sdk/typescript/v0.2.0...sdk/typescript/v0.3.0)

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

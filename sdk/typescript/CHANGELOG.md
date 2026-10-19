# Changelog

Notable changes to `@stacklok-oss/mecatl-sdk` are recorded here.

For installation and API entry points, see the [TypeScript SDK README](./README.md).

## [0.5.0](https://www.npmjs.com/package/%40stacklok-oss%2Fmecatl-sdk/v/0.5.0)

- docs: include user-docs in matlatl and use local documentation links (#1934) ([`b113292`](https://github.com/stacklok/mecatl/commit/b1132929dd9a3d881dbf059fb2b508bd1964b862))
- feat(mecatui): restore session status metrics (#1891) ([`851b437`](https://github.com/stacklok/mecatl/commit/851b437b13087d4f6ec4933c4e62fe917de9af99))
- feat: add microVM execution environments (#580) ([`178ed3f`](https://github.com/stacklok/mecatl/commit/178ed3fd0b264ccad4da8a4fb3cf6d5dcc2a9dd9))
- feat: draft native Kubernetes execution provider (#1614) ([`804298a`](https://github.com/stacklok/mecatl/commit/804298a504d06b59e4b8155cc6ae94be05c91853))
- fix(slack-bot): stop empty arg values from breaking the approval card (#1987) ([`3261c4f`](https://github.com/stacklok/mecatl/commit/3261c4f2f68a3139b21c8db1ec6534540a9b5a71))
- fix(sdk): update fast-uri to clear Node audit (#1996) ([`8583fc0`](https://github.com/stacklok/mecatl/commit/8583fc038800344fd49b5be69e7cb37d37226b9a))
- fix(sdk): update brace-expansion advisory resolution (#2007) ([`0213c10`](https://github.com/stacklok/mecatl/commit/0213c108fdda21ee79176ccbcf95d71ec8fdf69c))
- Bound mecatui saved-memory browser (#2012) ([`a231359`](https://github.com/stacklok/mecatl/commit/a2313597ddf63f66f9b3b9e9f77debdcda9bba41))
- feat: publish live tool-result availability before canonical batch drain (#2013) ([`6473fa3`](https://github.com/stacklok/mecatl/commit/6473fa34100cee15353c29c0cbd72c580d5c3069))
- fix(slack-bot): harden release image (#1182) ([`473c050`](https://github.com/stacklok/mecatl/commit/473c05074cba0607c9d05f9f0c6e8bfdc5f05783))
- chore(deps): update docker images (#2068) ([`e8b43d6`](https://github.com/stacklok/mecatl/commit/e8b43d61d70fbba5bc8552214397e4d6e5a9fe75))
- chore: delete ADRs, acceptance plans, and process scaffolding (#2105) ([`b6fdf3d`](https://github.com/stacklok/mecatl/commit/b6fdf3df09904265d5876d7e09f05ef8220a6240))
- fix(sdk): clear new dependency advisories in the SDK audit (#2132) ([`3c8366f`](https://github.com/stacklok/mecatl/commit/3c8366f5ee5f15a08a21f256c5adcc2752cba717))
- docs: ADR-reference cleanup, rule hygiene, module AGENTS.md files (#2148) ([`443c8d3`](https://github.com/stacklok/mecatl/commit/443c8d3dc08668406cd4fc0fff7b1d0ac18fc63c))
- docs(proto): drop ADR references from public Harness and Schedule comments (#2153) ([`a3f30b8`](https://github.com/stacklok/mecatl/commit/a3f30b84f740aebcdba7a2defed2c97b83d9aa15))
- docs: restructure user documentation journeys (#2136) ([`21bafbc`](https://github.com/stacklok/mecatl/commit/21bafbc1a2cc1f18c1e708ee3d42407e5da0937e))
- docs: copyedit public documentation (#2137) ([`4b834fd`](https://github.com/stacklok/mecatl/commit/4b834fd9769c5e2449e63e5d5a70cc1e814e33ca))
- feat(mecatui): inspect Edit requests and bounded Subagent activity (#2139) ([`dcf1ea4`](https://github.com/stacklok/mecatl/commit/dcf1ea4a2c393c83f6cbcea20aebbf753e9cb2e9))
- chore: remove dead ADR references and fix stale code comments (#2169) ([`0efb317`](https://github.com/stacklok/mecatl/commit/0efb31712d46a64e5e8c7b82bb9152e1c99a3dcb))
- chore: drop stale acceptance-plan citations from code comments (#2187) ([`37583f5`](https://github.com/stacklok/mecatl/commit/37583f5a714d20ee028507c39d11c208a3d685ba))
- feat(mcp): accept client secrets from bounded files (#2188) ([`024ba88`](https://github.com/stacklok/mecatl/commit/024ba883a53e82c24a6da7cf88de6ac63fba0ab3))
- chore(deps): update typescript sdk (#1956) ([`e692a04`](https://github.com/stacklok/mecatl/commit/e692a049648c172dac3bb4b377c8242489a0031d))
- feat: add provider-aware model aliases and delegation selectors (#2019) ([`34f485d`](https://github.com/stacklok/mecatl/commit/34f485da08490d538944314966b98a76bb333190))
- fix(security): update Go and x/net vulnerability floors (#2221) ([`e9c216a`](https://github.com/stacklok/mecatl/commit/e9c216aa1894b120c1a2be7fb21053196f0c9301))
- spike(mecatui): migrate help browser to the reusable scroll system (#2042) ([`c6d7338`](https://github.com/stacklok/mecatl/commit/c6d7338888b94935b67535a0bd37902dd944ff71))
- feat: add execution templates with platform-managed identity (#2219) ([`987f0c3`](https://github.com/stacklok/mecatl/commit/987f0c31ce457ed1514c1da1a611285d9e846b2f))
- feat(mecatui): enforce API compatibility before startup (#2269) ([`2fbdea1`](https://github.com/stacklok/mecatl/commit/2fbdea1787ab4fbccc577cacdaa174d04a29d3a6))
- feat(skills): read a skill's files through the client API (#2192) ([`63ab9dc`](https://github.com/stacklok/mecatl/commit/63ab9dcce987d2e2e66adcb49d39a768f743cc12))

[Compare changes](https://github.com/stacklok/mecatl/compare/sdk/typescript/v0.4.0...sdk/typescript/v0.5.0)

## [0.4.0](https://www.npmjs.com/package/%40stacklok-oss%2Fmecatl-sdk/v/0.4.0)

- refactor(mecatui): prepare conversation cards functionally (#1744) ([`3aebbb5`](https://github.com/stacklok/mecatl/commit/3aebbb5eeb0450a501a6292f16c6e89c402f2c2f))
- refactor: remove obsolete alpha compatibility paths (#1725) ([`b0cf03a`](https://github.com/stacklok/mecatl/commit/b0cf03a423df907e07269e085105fda2dc81ea47))
- feat(router): add Jev delegated-model routing (#1738) ([`117f6d0`](https://github.com/stacklok/mecatl/commit/117f6d0fb65438aeafc9b53497f0f526b30c561b))
- feat(mcp): reconcile sources and refresh session tools (#1673) ([`cb70893`](https://github.com/stacklok/mecatl/commit/cb70893ae36e284c3bf28e92c3a5a49888f0d7b3))
- feat: centralize provider-scoped model discovery (#1841) ([`2f2d1de`](https://github.com/stacklok/mecatl/commit/2f2d1de2917fb71d1dcd12f8af03b9c236351c7f))
- feat: integrate contextual guardrails into execution and approvals (#1510) ([`409ea74`](https://github.com/stacklok/mecatl/commit/409ea74e89044c9d67a138aa8efbf09e17fb1fd8))
- test(sdk): enforce SDK/TUI high-level parity (#1471) (#1917) ([`e5039f8`](https://github.com/stacklok/mecatl/commit/e5039f8e64d7cfa6984c487cd977414ac9a1e2e6))
- feat(studio): add exact plan control and authorization review prerequisite (#1909) ([`b09f573`](https://github.com/stacklok/mecatl/commit/b09f573ac9fc64a2df79c0db52f13ba83dc26d56))

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

# GitHub Actions workflows

The workflow YAML files in this directory are the source of truth for CI and
release behavior. This page is only a stable index to operational guidance.

- [CI race-shard maintenance](../scripts/root-race-packages.sh)
- [Live end-to-end testing](../../e2e/README.md)
- [Performance tracking rationale](../../docs/adr/0019-perf-tracking.md)
- [Mecatequi CI adoption](https://mecatl.dev/docs/building/deployment/mecatequi) and its
  [design rationale](../../docs/adr/0028-mecatequi.md)

Mecatequi's same-repository action pins are maintained by the
[release-pin script](../../.claude/skills/cut-release/scripts/bump-release-pins.sh)
and checked by [`task lint:reusable-pins`](../actions/check-reusable-pins.sh).

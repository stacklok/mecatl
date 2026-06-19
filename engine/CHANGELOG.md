# Changelog — `github.com/stacklok/mecatl/engine`

All notable changes to the engine module's public API are recorded here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); changes
are classified per [COMPATIBILITY.md](./COMPATIBILITY.md) (Added = minor;
Changed/Deprecated/Removed = breaking, pre-v1 a minor bump).

The covered surface is the seven core packages (`session`, `governance`, `tool`,
`prompt`, `port`, `team`, `agent`); their committed API snapshots live in
[`engine/api/`](./api/).

## [Unreleased]

This is the **initial baseline**. The entries below record the establishment of
the module and its compatibility contract, not a change to a previously-published
surface.

### Added

- The engine became its own Go module, `github.com/stacklok/mecatl/engine`,
  importable independently of the host repo (monorepo via `go.work`). (#113,
  [ADR 0036](../docs/adr/0036-engine-module.md))
- A public API stability contract for the seven core packages: this CHANGELOG,
  [COMPATIBILITY.md](./COMPATIBILITY.md), the committed text snapshots under
  [`engine/api/`](./api/), and the `api-compat` freshness gate that fails CI on
  an unflagged change to the exported surface. (#114,
  [ADR 0037](../docs/adr/0037-engine-stability-contract.md))

# Contributing to mecatl <!-- omit from toc -->

Thank you for contributing to mecatl. It is released under the Apache 2.0
license. This guide covers contributions **to** mecatl; people using,
embedding, or operating mecatl should start with the [README](README.md) and
the linked user and operator documentation.

## Table of contents <!-- omit from toc -->

- [Code of conduct](#code-of-conduct)
- [Reporting security vulnerabilities](#reporting-security-vulnerabilities)
- [How to contribute](#how-to-contribute)
  - [Using GitHub Issues](#using-github-issues)
  - [Claiming an issue](#claiming-an-issue)
  - [Development tool versions](#development-tool-versions)
  - [Development workflow](#development-workflow)

## Code of conduct

This project follows the [Code of Conduct](CODE_OF_CONDUCT.md). By
participating, you are expected to uphold it. Report unacceptable behavior to
[code-of-conduct@stacklok.com](mailto:code-of-conduct@stacklok.com).

## Reporting security vulnerabilities

Do not disclose security vulnerabilities in GitHub issues. Follow the private
reporting process in [SECURITY.md](SECURITY.md).

## How to contribute

### Using GitHub Issues

GitHub issues track bugs and enhancements. Before proposing a substantial
change, open an issue so maintainers and contributors can agree on the problem
and scope. Bug reports are most useful with a small, reproducible example and
relevant environment details.

### Claiming an issue

Before starting work on an existing issue, leave a comment saying that you
would like to work on it and wait for a maintainer to assign it. This avoids
duplicated effort. If you open an issue that you plan to implement, say so in
the issue description.

### Development tool versions

Use these versions to align your development environment with
[CI](.github/workflows/ci.yml). The SDK's supported runtime minimums are separate
from these contributor test targets.

| Tool | Suggested version | Needed for |
| --- | --- | --- |
| Go | Current stable, at least 1.27 | Go builds and tests; CI selects `stable` |
| Task | 3.54.0 | Repository build and test commands |
| Node.js (SDK and docs) | 24.x | TypeScript SDK and documentation tooling |
| Node.js (Studio) | 26.x | Studio, selected by [`apps/.node-version`](apps/.node-version) |
| pnpm | 12.15.0 | TypeScript SDK and Studio dependencies |
| Bun | 1.4.4 | Node/Bun offline SDK wire tests (`task sdk:e2e`) |
| Deno | 2.9.3 | Deno SDK checks (`task sdk:deno`); CI also tests current 2.x |
| Buf | 1.75.0 | Protocol contract generation (`task generate`) |

The [Go module](go.mod) owns the Go language minimum. Each workspace's
`packageManager` field owns its pnpm version: [SDK](sdk/typescript/package.json)
and [Studio](apps/package.json). Update this table when changing the corresponding
CI or package declarations.

With [mise](https://mise.jdx.dev/), you can select the SDK runtimes for one command
without changing your defaults:

```sh
mise exec node@24 pnpm@12.15.0 bun@1.4.4 -- task sdk:e2e
```

To make Bun your personal default, use `mise use --global bun@1.4.4`.
`mise use` installs and selects a tool; `mise install` alone only installs it.
Check `node --version`, `pnpm --version`, and `bun --version` in the checkout
before running the gates. A runtime-manager shim on `PATH` does not by itself
mean a version is selected.

### Development workflow

Read [AGENTS.md](AGENTS.md) before editing. It holds the build and test
commands and the code invariants that this guide intentionally does not
duplicate. External contributors use a fork-and-pull-request workflow.

Use the Taskfile rather than bare root-level build commands:

```sh
task build
task test          # complete fast offline suite while iterating
task lint
task test:race     # complete race suite before the PR is ready
go run ./cmd/mecademo
```

## Develop against a local engine checkout

The engine is a separate Go module. For a consumer application that needs local
engine changes, run these commands from its module directory:

```sh
go work init .
go work use <PATH_TO_MECATL_CHECKOUT>/engine
```

Use the workspace for local development; published consumers depend on a tagged
engine module. Verify the engine independently with `GOWORK=off` so workspace
resolution does not hide missing dependencies.

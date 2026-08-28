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

### Development workflow

Read [AGENTS.md](AGENTS.md) before editing. It is the canonical technical
contract: it defines the architecture, layering rules, safety invariants,
generated files, and workflow details that this guide intentionally does not
duplicate. External contributors use a fork-and-pull-request workflow.
Repository maintainers and automation use the internal workflow defined in
AGENTS.md; these are audience-specific paths, not conflicting instructions.

Use the Taskfile rather than bare root-level build commands:

```sh
task build
task test
task lint
go run ./cmd/mecademo
```

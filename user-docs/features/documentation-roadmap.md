---
sidebar_position: 2
title: Feature documentation roadmap
description: Plan the capability inventory and user-facing feature documentation.
---

# Feature documentation roadmap

This is the working plan for completing the feature documentation tree. It is
kept next to the scaffolds so contributors and documentation agents share the
same order of work and evidence standard.

:::note[Working plan]

The pages in this section are intentionally scaffolded first. Complete the
inventory and classification review before turning a scaffold into a claim
about current behavior.

:::

## 1. Complete the capability matrix

Populate [the capability and deployment matrix](./capability-matrix.md) from
both the current source tree and the complete Git history.

For each capability, record:

- the user-facing capability;
- the source proof, such as a flag, configuration key, port, tool, composition
  path, or driver service;
- availability in `mecated`, `mecak8s`, `mecatequi`, `mecatui`, and embedded
  `engine` deployments;
- configuration and capability gates;
- the current Git-history status;
- a provisional classification; and
- any historical correction or open product decision.

Use these classifications:

- **Portable**: a core port or wire contract exists and at least one
  non-filesystem implementation satisfies it.
- **Local-only by design**: the capability is inherently tied to a local shell,
  checkout, process, editor, or host namespace.
- **Local-only gap**: a local implementation exists, but no structural barrier
  prevents a remote implementation and none is currently wired.

Keep distinct capabilities separate. For example, periodic dreaming, manual
reviewed dreaming, session migration, session retention, and session adoption
should not become one broad row.

## 2. Review ambiguous classifications

Stop after the inventory and ask for a product or architecture decision on
ambiguous rows, including:

- `.claude/rules` remote support;
- remote user-model and learning stores;
- reviewed dreaming and its process-local plan state;
- MCP OAuth credential backends;
- remote execution-environment reattachment;
- driver support for storage health, migration, and cleanup; and
- SkillDraft quarantine and promotion.

Do not publish a gap classification until intentional local-only behavior has
been separated from missing remote implementation.

## 3. Add evidence links

Link matrix rows to the relevant feature page and source documentation. Verify
claims against current composition and capability advertisement, not only ADR
titles or historical implementation commits.

A commit in `git log --all` does not establish current availability unless it is
reachable from the current product or is explicitly marked branch-only,
superseded, deferred, or removed.

## 4. Write the first user-facing pages

Complete the pages in this order:

1. [Start and resume sessions](./start-and-resume-sessions.md)
2. [Choose models and providers](./choose-models.md)
3. [Use mecatui](./use-mecatui.md)
4. [Define named agents](./named-agents.md)

These answer the most important questions for someone arriving at mecatl for
the first time.

## 5. Continue by user journey

After the first four pages, complete:

1. [Permissions and posture](./permissions-and-posture.md)
2. [Skills, commands, and soul](./skills-commands-and-soul.md)
3. [Execution environments](./execution-environments.md), including background Bash
4. [Session continuity](./session-continuity.md) and storage maintenance
5. [Scheduled tasks](./scheduled-tasks.md)
6. [MCP OAuth and credentials](./mcp-oauth-and-credentials.md)
7. [Caller identity and OIDC](./caller-identity.md)
8. [Learning](./learning.md) and [dreaming](./dreaming.md)
9. [Multimodal input](./multimodal-input.md)
10. [Context windows](./context-windows.md) and
    [OpenRouter routing](./openrouter-routing.md)

## 6. Strengthen the authoring contract

After the first completed page, update `website/CLAUDE.md` with the reusable
feature-page template:

1. What the feature does
2. Availability
3. Quickstart or usage
4. Configuration
5. Limitations
6. Next steps
7. Related information
8. Troubleshooting, when useful

Every availability statement should be checked against flags, composition,
capability advertisement, driver interfaces, and current deployment behavior.

## 8. Add generated API references

Add machine-generated references for both public wire surfaces without replacing the
human guides:

- generate a gRPC reference from `contracts/proto/mecatl/v1/*.proto` as part of
  `task generate`;
- introduce an OpenAPI source for the JSON HTTP endpoints and generate its
  endpoint reference; and
- document the SSE event stream and cross-surface lifecycle in the curated
  guides, where behavior and examples are easier to explain.

Keep the existing human guides as the user-oriented entry points:

- [`docs/usage/grpc-api.md`](https://github.com/stacklok/mecatl/blob/main/docs/usage/grpc-api.md)
- [`docs/usage/http-sse-api.md`](https://github.com/stacklok/mecatl/blob/main/docs/usage/http-sse-api.md)

The generated references should be checked for drift in CI. The implementation
must choose one source of truth for the HTTP route contract rather than
maintaining an independently edited OpenAPI file and route table. Add links
from the user-facing API flow page once the generated references exist.

## 9. Validate each page

Run these checks after each completed page or page group:

```sh
task site:build
task docs
```

The site build is the broken-link gate. `task docs` refreshes generated
artifacts and runs the strict documentation checks.

## Related information

- [Features](./index.md)
- [Capability and deployment matrix](./capability-matrix.md)
- [Documentation conventions](https://github.com/stacklok/mecatl/blob/main/docs/design/README.md)


---
title: CI integration
description: Run a bounded agent task in CI and publish its patch and result safely.
sidebar_position: 1
---

# CI integration

Use `mecatequi` for one bounded agent prompt in a CI job. It writes a patch,
summary, and optional event log, then exits. Your workflow owns the checked-out
workspace, provider credentials, and publication of the result.

[Single-shot CI with mecatequi](mecatequi.md) covers artifacts, exit codes,
permission behavior, and the reusable GitHub Actions workflow. Its
split-privilege model separates the agent run from the credentialed publishing
step; preserve that separation when adapting the workflow to another CI system.

For an application that maintains sessions across prompts, use the
[Go engine](/building/go/index.md) or [TypeScript SDK](/building/typescript-sdk/index.md).

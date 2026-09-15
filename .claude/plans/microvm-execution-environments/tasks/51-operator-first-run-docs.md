---
id: 51-operator-first-run-docs
title: Add copy-paste operator quickstart, config, services, and troubleshooting
blocked_by: [49-first-run-empty-xdg-e2e]
status: done
branch: "plan-microvm-execution-environments/51-operator-first-run-docs"
worktree: ".scratch/task-microvm-51"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Documentation round

Turn `docs/usage/microvm-environments.md` into a first-run operator runbook grounded in the shipped commands/defaults. Include the shortest `mecatui microvm init` and daily `mecatui --microvm` path, one complete valid daemon JSON/reference-generated config explanation, XDG paths/modes, installed release/OCI trust flow, daemon foreground/start/reuse behavior, doctor/status/recover/delete commands, source/worktree/guest inspection, detach/delete semantics, and explicit current limits. Add systemd user and launchd examples only if they match actual foreground process behavior and path requirements. Add a symptom→diagnosis→command/remediation table for KVM, HVF, socket paths/permissions, Sigstore/evidence, stale generations, quota, retained dirty worktrees, egress, OCI/platform errors.

Verify every command/flag/config field against code and tests; no invented commands. Run docs/configref/matlatl/site gates.

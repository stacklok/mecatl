# ADR 0248 — Multimodal steer preserves prompt content

- Status: Accepted
- Date: 2026-08-31
- Scope: Engine steer inbox, gRPC steer frames, and mecatui attachment lifecycle
- Supersedes: [ADR 0232](./0232-steer-while-running.md) — the text-only steer payload and capability-negotiation decision only; its timing, inbox, correlation, and promotion decisions stand
- Superseded by: None

## Context

ADR 0232 introduced text-only steer while ordinary prompts already supported typed
image and audio parts. Mecatui therefore stripped attachment markers and sent bytes
for an idle prompt, but sent only the literal marker when the same draft was entered
during a run. Falling back to a later local prompt would avoid loss but would also
remove steer semantics.

## Decision

A steer carries the same `session.Content` media parts as an ordinary prompt.
`Steer` and `SteerEcho` carry repeated `Content` fields, and the existing
`ServerCapabilities.steer` bit gates the complete multimodal contract. Mecatui
uses native steer when that bit is true and otherwise keeps all mid-run input in
its local merge queue, preserving runtime feature disabling without duplicate
capability state.
The run inbox atomically owns one `{text, parts}` bundle: text fragments retain the
existing conditional blank-line merge, while media parts append in fragment order.
The combined bundle is validated through `session.ValidateMediaParts` before an
append becomes visible. Cancel clears both values.

The drain records and emits text and parts together through the existing user-content
and event projection paths. Too-late promotion passes the same bundle through
`StartRunContent`. The engine, Service, and client each expose one multimodal steer
entry point.

Mecatui uses one attachment-preparation path for direct prompts and steers. A queued
steer send owns its prepared media and the staged source needed by edit-back;
rejection leaves the draft stores unchanged, `↑` restores both, and retraction or
drain releases both. The committed echo uses the existing media-aware conversation
projection.

## Consequences

Media-only steers are legal. Pending media remains run-scoped and restart-lost by the same deliberate policy as
a pending text steer; once drained, it is durable ordinary conversation content.
The inbox must retain bytes until drain, cancel, or terminal promotion completes.

## See also

- [Agent loop architecture](../architecture/agent-loop.md#steer-while-running)
- [TUI guide](../tui.md#steer-mode-mid-run-steer-when-the-server-advertises-it)
- [ADR 0232](./0232-steer-while-running.md)

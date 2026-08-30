---
id: 01-status-protocol
title: Shared Input and safe StatusML protocol
blocked_by: []
status: done
branch: plan-mecatui-status-line/01-status-protocol
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Create a small dependency-leaf status-line protocol package usable by mecatui composition and UI. Define an allowlisted, versioned `Input` with display-safe session/model/server/usage/context/delegation/workspace/layout/clock data and explicit zero values. Define bounded StatusML parsing for optional header/footer surfaces, optional left/right blocks, and the semantic tags `text`, `muted`, `primary`, `secondary`, `accent`, `success`, `warning`, `error`, and `info`. Reject or safely literalize malformed/unknown markup and strip terminal controls. Map semantic tokens through the active theme without freezing #799's cross-widget contract. Do not import engine/internal/proto, read settings, or start processes.

## Acceptance criteria

- AC1.2: The status input excludes prompts, transcript/tool content, credentials, authentication metadata, and diagnostics; the server identity is display-only and never carries authentication data.
  - verify: `TestStatusCustomization_Scenario1_StatusInputExcludesSensitiveContent`
- AC2.5: Each StatusML semantic token resolves through the active theme, including custom palettes; no raw ANSI/OSC sequence reaches the terminal.
  - verify: `TestStatusLine_Scenario2_StatusMLUsesThemeAndSanitizesTerminal`

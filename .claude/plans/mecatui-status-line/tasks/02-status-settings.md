---
id: 02-status-settings
title: Strict user-global status customization settings
blocked_by: [01-status-protocol]
status: done
branch: plan-mecatui-status-line/02-status-settings
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Extend only the strict client-owned `$XDG_CONFIG_HOME/mecatui/settings.yaml` schema with `status_customization`. Support exactly one of responsive template surfaces or local command configuration, plus an optional interval no shorter than one second. Validate schema and command fields without echoing user values. Keep the legacy `mecatl/settings.yaml`, projects, server configuration, CLI, and gRPC untouched. This task returns inert parsed configuration only—no templates, processes, or UI layout.

## Acceptance criteria

- AC1.4: `status_customization:` is parsed only from the user-global mecatui settings document, beside key bindings; absent settings select the shipped default template.
  - verify: `TestStatusCustomization_Scenario1_UserSettingsOwnCustomization`

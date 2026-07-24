---
id: 04-remove-declarative-cli
title: Remove the settings.yaml schedules: block + the mecated schedules CLI (clean)
blocked_by: [01-schedule-tool-core]
status: done
branch: "plan-schedule-tool/04-remove-declarative-cli"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-tool
---

# Task brief

Delete the two redundant operator authoring surfaces the in-chat `Schedule`
tool replaces: the operator-tier `settings.yaml` `schedules:` block and the
`mecated schedules <verb>` CLI. CLEAN removal — no deprecated aliases, no WARN
shims, no presence markers (the user's explicit instruction). The gRPC
`ScheduleService` + REST `/v1/schedules` surface (the mecatui `/schedule`
overlay's transport, and out-of-band management) is RETAINED.

**permconfig (`internal/adapter/permconfig`).** Delete the `schedules:` schema:
`SchedulesSection`, the `Config.Schedules` field, `Resolver.OperatorSchedules`,
the project-tier-ignore WARN path, and their strict-parse tests
(`internal/adapter/permconfig/schedules_test.go`). A residual `schedules:` key
in an old config is silently ignored by the lenient top-level decode (plain
`yaml.Unmarshal`, no custom top-level `UnmarshalYAML`) — that is the intended
clean-removal behaviour, same as any removed YAML key. Do NOT add a presence
marker / WARN.

**Composition (`internal/app`).** Delete `foldOperatorSchedules` +
`reconcileSchedules` (`internal/app/schedules.go`) and the
`Config.DeclaredSchedules` field (`internal/app/build.go:937-943`) and its fold
site (`internal/app/build.go:1574`). Delete the declarative/reconcile/fold
tests (`schedules_declarative_test.go`, `schedules_fold_test.go`,
`schedules_reconcile_test.go`). Startup no longer reconciles declared
schedules.

**cmd (`cmd/mecated`).** Delete `schedules_cmd.go` + `schedules_cmd_test.go`
and the `os.Args[1]=="schedules"` dispatch + `runSchedulesDispatch` in
`main.go` (`main.go:571-590`) and the `schedules <verb>` line in the usage
banner (`main.go:1332`). A bare `mecated schedules` then falls through to
normal startup / unknown-subcommand handling, never to the deleted HTTP client.

**Docs.** Remove the `schedules:` subtree from `docs/configuration-reference.md`
(note: `task docs` regenerates it via configgen — remove it from the SOURCE
`internal/configgen/settings.skeleton.yaml` if that is where it is generated
from, then regenerate). Remove the declarative-settings + `mecated schedules`
CLI sections from `docs/usage.md` (replace with a pointer to the in-chat
`Schedule` tool + the REST/gRPC API). Update `docs/architecture.md`'s
scheduled-tasks section. Run `task docs` (llms.txt regen + matlatl strict
gate) — you touched markdown.

**Keep green.** AC3.3 asserts the gRPC/REST surface survives — the mecatui
`/schedule` overlay must keep working.

## Acceptance criteria

- AC3.1: No `schedules:` key is honoured from any settings tier. The schema is
  deleted outright; a residual `schedules:` block in an old config file is
  silently ignored by the lenient top-level decode (no hard failure, no WARN
  machinery carried for a removed feature).
  - verify: `TestScheduleTool_SettingsSchedulesBlockRemoved`
- AC3.2: `mecated schedules <verb>` is gone; a bare `mecated schedules` falls
  through to normal startup (or an unknown-subcommand error), never to the
  deleted HTTP client.
  - verify: `TestScheduleTool_SchedulesCLIRemoved`
- AC3.3: The gRPC `ScheduleService` and REST `/v1/schedules` routes still serve
  create/list/get/update/delete/pause/resume/fire/fires (the overlay + out-of-band
  surface is unaffected by the settings/CLI removal).
  - verify: `TestScheduleTool_WireSurvivesSettingsCLIRemoval`

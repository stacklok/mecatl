---
id: 48-microvm-lifecycle-cli-ux
title: Expose safe doctor, status, recovery, and deletion UX
blocked_by: [46-user-bootstrap-daemon-manager, 47-mecatui-microvm-first-run]
status: done
branch: "plan-microvm-execution-environments/48-microvm-lifecycle-cli-ux"
worktree: ".scratch/task-microvm-48"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Usability round

Extend task 47's existing `mecatui microvm doctor|status|recover|delete` commands with authenticated daemon lifecycle behavior. Add a bounded owner-scoped inventory RPC to microvmd/client/manager rather than reading registry files directly. Status reports healthy/stale/error generations and worktree paths; recover restarts/reconciles but never creates a missing generation; confirmed permanent delete reports clean removal or retained dirty worktree. TUI close remains detach-only and says so. Preserve exact owner/session/ref/generation binding and noninteractive destructive guards.

Verification: authenticated owner-filtered inventory; status healthy/stale/error; exact recovery/no fallback; destructive confirmation; dirty retention path; actionable errors; existing task-47 command/help tests remain green.

# Mecatui diagnostic-log retention — acceptance plan

**Phase:** capability — bounded local diagnostics
**Status:** draft, 2026-09-06. Startup retention for the embedded mecatui diagnostic sink.
**Accumulator branch:** `acc/mecatui-diaglog-retention` (off `main`).

The smallest change makes both the default `$XDG_STATE_HOME/mecatl/mecatui.log` sink and an explicit `--diagnostics-log` override retain at most a fixed 10 MiB recent tail before opening for append. When trimming cuts through records, the retained tail starts at the next complete newline when one exists; otherwise it remains the exact final 10 MiB. Small and new files retain their current contents, and any unsafe or failed replacement degrades to `io.Discard` without damaging the prior file.

This is a startup-only local sink change. It does not add cadence or configuration, start a goroutine, alter sessions or stores, or require an ADR: the existing sink ownership and no-global-slog policy are already frozen by [ADR-0020](../adr/0020-diagnostics.md).

## Why these scope cuts

- [`docs/architecture/observability.md` — Diagnostics](../architecture/observability.md) keeps operational diagnostics injected and distinct from events, audit, and session persistence.
- [ADR-0020](../adr/0020-diagnostics.md) assigns mecatui's embedded file sink to the command root and requires degraded logging rather than alt-screen corruption.
- [`AGENTS.md` — diagnostics and resource invariants](../../AGENTS.md) requires safe injected diagnostics, no unexpected cadence/goroutine/session-store work, and atomicity/data-loss protections.

## In scope — 1 scenario

### Scenario 1 — Startup opens a bounded, safe diagnostic tail

On startup, the embedded mecatui path and the explicit diagnostic-log override each inspect their selected regular file before opening it for append. A file larger than 10 MiB is replaced atomically from a same-directory temporary file containing only the bounded recent tail; the cut prefers a complete newline. The original remains authoritative if any temporary-file, sync, rename, or related preparation step fails, and the returned writer becomes `io.Discard`. Symlinks and non-regular paths fail closed: they are neither followed for trimming nor replaced. The normal append path remains unchanged for small and new files. This preserves the existing alt-screen-safe sink behavior in [ADR-0020](../adr/0020-diagnostics.md) and the repository's [error-handling and data-loss invariants](../../AGENTS.md).

**Acceptance:**

- AC1.1: A file over 10 MiB is trimmed to the exact expected recent tail, no larger than 10 MiB, preferring the first complete newline in the retained window when available; the writer then appends after that tail.
  - verify: `TestDiagLogRetention_Scenario1_ExactRetainedTail`
- AC1.2: A new file and an existing file at or below 10 MiB keep their bytes unchanged before the first append, and the existing directory/file permissions and normal append behavior remain intact.
  - verify: `TestDiagLogRetention_Scenario1_SmallAndNewFilesUnchanged`
- AC1.3: Appending after a capped file never grows the retained prefix beyond the fixed cap; the resulting file contains the selected tail followed by the new diagnostic bytes.
  - verify: `TestDiagLogRetention_Scenario1_AppendAfterCap`
- AC1.4: A failure during same-directory temporary-file preparation, sync, or rename leaves the original bytes and path untouched and returns `io.Discard` for degraded logging.
  - verify: `TestDiagLogRetention_Scenario1_AtomicFailurePreservesOriginal`; `TestInvariant_diagnostic_log_retention_atomic_failure`
- AC1.5: Symlink and non-regular targets fail closed without following, truncating, or replacing the target; logging degrades to `io.Discard`.
  - verify: `TestDiagLogRetention_Scenario1_SymlinkAndNonRegularFailClosed`
- AC1.6: The explicit override path receives the same retention, newline-boundary, append, and failure behavior as the default state path; the default path remains selected when no override is supplied.
  - verify: `TestDiagLogRetention_Scenario1_OverridePath`
- AC1.7: Startup performs this one-time retention only; no cadence/configuration field, goroutine, session-store behavior, or unrelated transport mode is introduced or changed.
  - verify: inspection — the focused implementation and tests show retention is confined to the existing startup sink opener; the required full gates provide regression coverage.

## Out of scope

- Runtime rotation, periodic trimming, configurable retention, or a new diagnostic-log setting.
- Retention for mecated, other binaries, session stores, event logs, tool audit records, or arbitrary user files.
- Following symlinks, replacing non-regular paths, or recovering from a failed sink by writing to stderr/stdout.
- Changes to diagnostic content, formatting, levels, global-slog ownership, or TUI rendering.

## Deferred decisions

- A different retention size or configurable rotation policy requires a separate design and acceptance plan.
- Cross-platform filesystem guarantees beyond the supported atomic same-directory temp/sync/rename contract are not expanded here.

## Definition of done

1. `task lint` and `task test` pass (both modules, with `-race`).
2. `task docs` passes and leaves generated documentation current.
3. `task site:build` passes.
4. `task ac-trace-strict` passes once this plan is landed.
5. `go run ./cmd/mecademo` still prints a complete offline session.
6. The named tests run offline and cover both default and override paths without changing cadence, configuration, goroutines, or session-store behavior.

## Exit criteria

The plan is satisfied when Scenario 1's named tests and all Definition-of-done gates pass on the accumulator.

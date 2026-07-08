# Audit: Technical Accuracy Review — Round 3 — 2026-07-08

## Expert persona

**Role:** Technical accuracy reviewers — engineers who read the implementation closely and check the consumer docs against what the code actually does.

**Attack vector:** Drift from ~40 commits that landed on `main` since round 2 (2026-06-28): two engine releases (v0.3.0, v0.4.0), an entirely new scheduled-tasks subsystem, MCP typed tool results + server notifications, a writable named-specialist subagent mode, and three guardrails ADRs (Bash-default rule, then a human-override mechanism that was itself superseded by an approve-once flow). Stale flags/signatures/version numbers, and doc content describing a mechanism that has since been replaced.

**Model:** Claude Opus 4.8 (audit execution, four parallel clusters) / Claude Sonnet 5 (new-content drafting, four parallel tasks) / Claude Sonnet 5 (orchestration, fix application, manual audit of the what-you-get cluster after two agent attempts on that cluster failed to return output).

**Scope:** The entire site. This round also included a branch rebase (docs-toolkit-consumer was 40 commits behind `origin/main`) and four new content sections covering subsystems the site had zero coverage of.

---

## Cost (medium confidence)

Measured via `meter:report`. The ten sub-agents spawned for this round (six audit passes across four clusters — two clusters needed a second attempt after the first agent went silent — plus four new-content drafts) are cleanly attributable:

| Agent | Role | Cost |
|---|---|---|
| `audit-deploy` | Opus, deployment cluster audit | $6.95 |
| `audit-wyg2` | Opus, what-you-get cluster audit (2nd attempt) | $5.26 |
| `audit-ext2` | Opus, extension-points cluster audit (2nd attempt) | $4.83 |
| `audit-root` | Opus, root/getting-started cluster audit | $3.89 |
| `audit-ext` | Opus, extension-points cluster audit (1st attempt, went silent) | $3.82 |
| `audit-wyg` | Opus, what-you-get cluster audit (1st attempt, went silent) | $3.70 |
| `write-scheduled-tasks` | Sonnet, new page | $1.21 |
| `write-mcp-typed` | Sonnet, MCP sections | $1.22 |
| `write-guardrails-update` | Sonnet, guardrails section | $1.15 |
| `write-writable-specialist` | Sonnet, writable-specialist section | $1.10 |
| **Sub-agent total** | | **≈ $33.13** |

**What's NOT cleanly separable:** the main-session (orchestration) cost. `meter:report` walks a *resumed session chain* that also includes prior, unrelated work in three other local projects/worktrees (`mecatl`, `docs-cloud-native-kit`, `docs-cloud-native-kit-definition`) plus 4 small stray sub-agent calls (~$1.94 combined) from that same chain — none of that is this task. No cost mark was set at the start of this task, so there's no clean before/after split of the orchestration cost the way the sub-agent total above is clean. The whole-chain session transcript reports **$17.83**, but that figure is an upper bound spanning unrelated sessions, not this task alone — treat it as directional only, not as an addend to the $33.13 above.

---

## Part 1: New content added

The site had no coverage at all of four features that shipped on `main` after the original authoring session:

1. **Scheduled tasks** (new page, `what-you-get/scheduled-tasks.md`, 215 lines) — `port.ScheduleStore`, the store adapters, the tick loop + leader-lease, the gRPC `ScheduleService` + REST surface, the `settings.yaml schedules:` declarative block, the `mecated schedules` CLI, events, and metrics. See ADR 0059-scheduled-tasks.
2. **MCP typed tool results, structured-result truncation, and server notifications** (extended `what-you-get/mcp-client.md`) — `ToolResult.Parts`, capability-gated routing, `Content.Audience` as advisory-only, `resource_link` SSRF handling, `CallMcpWithQuery`, and `list_changed` notifications. See ADRs 0057, 0059-mcp-typed-tool-results, 0063.
3. **Writable named-specialist subagents** (extended `extension-points/agent-definitions.md`) — `mode:"read-write"` + `agent`, its scope limits, and the tradeoffs. See ADR 0058.
4. **Guardrails: Bash-default rule + approve-once** (extended `what-you-get/permissions.md`) — the read-only pre-filter, the Bash-specific danger-category rubric, and the out-of-band approve-once recovery flow that replaced the (now-superseded) `/guardrail-allow` prompt directive. See ADRs 0060, 0062 (0061 is superseded and intentionally not documented as current).

Every identifier, flag, and signature in the new content was checked against the actual source (not just ADR prose) before being written; several of these agents independently caught places where the *ADR's own prose* was looser than the shipped code (e.g. ADR 0062 describes the guardrail waiver as "tool-exact + Bash-command-substring," but the shipped code in `internal/adapter/modelhook/waiver.go` is exact-match only, citing CWE-863 — the doc documents the real, stricter behavior).

---

## Part 2: Accuracy findings (existing pages)

### Finding 1: Engine dependency-closure enumeration omits `robfig/cron/v3`
**Docs:** `api-stability.md`, `getting-started/deployment-decision.md`, `deployment/embed-engine.md`, `deployment/index.md` (four locations)
**Claim:** The engine's runtime dependency closure is "`doublestar` + `x/sync` + test-only `goleak`" / "exactly three packages."
**Reality:** `engine/go.mod` has a fourth direct, runtime require: `github.com/robfig/cron/v3 v3.0.1`, used by `engine/adapter/cronparse` for scheduled-tasks cron parsing. It's a zero-dependency module, so the "no heavy require cone" claim still holds — only the exact enumeration was wrong.
**Severity:** Moderate
**Status:** Fixed 2026-07-08 — all four locations updated to list `robfig/cron/v3`.

### Finding 2: `cloud-native-kit.md` cites a non-existent flag `--session-lease-k8s-ttl`
**Doc:** `cloud-native-kit.md`
**Reality:** The real flag is `--session-lease-ttl` (default `30s`), shared across all lease backends on both `mecated` and `mecak8s`.
**Severity:** Moderate
**Status:** Fixed 2026-07-08.

### Finding 3: `api-stability.md`'s "recent examples" callout was stale (cited only v0.2.0)
**Reality:** Two releases have shipped since: v0.3.0 (guardrail approve-once seam) and v0.4.0 (`session.Usage.ReasoningTokens`).
**Severity:** Minor
**Status:** Fixed 2026-07-08 — updated to cite v0.3.0/v0.4.0 plus a v0.1.0 Removed example for the "breaking change" case.

### Finding 4: `demo.md` overstated the Go version as an exact patch (`1.26.3`)
**Reality:** `go.mod`/`engine/go.mod` pin `go 1.26` (minor only, no `toolchain` line) — any 1.26.x works.
**Severity:** Minor
**Status:** Fixed 2026-07-08.

### Finding 5: `mecatequi.md` documents a non-existent `--provider` flag
**Reality:** Only `--default-provider` exists (`cmd/mecatequi/flags.go`).
**Severity:** Moderate
**Status:** Fixed 2026-07-08.

### Finding 6: `mecatequi.md`'s `Summary.Usage` JSON shape is missing `reasoning_tokens`
**Reality:** `cmd/mecatequi/run.go`'s `SummaryUsage` gained a sixth field, `ReasoningTokens int json:"reasoning_tokens"`.
**Severity:** Minor
**Status:** Fixed 2026-07-08.

### Finding 7: `grpc-http.md`'s reproduced `Event` proto is missing field 15
**Reality:** `contracts/proto/mecatl/v1/harness.proto`'s `Event` message now has `SchedulePayload schedule = 15;`.
**Severity:** Minor
**Status:** Fixed 2026-07-08.

### Finding 8: `grpc-http.md`'s HTTP approve-endpoint table row was stale and the `/mode` route was missing entirely
**Reality:** The table still showed only the legacy `{ask_id, allow}` body even though the prose two sections later (fixed in round 2) already documents the three-way `verdict`. The `POST /v1/sessions/{id}/mode` route (`internal/adapter/server/http.go`) was also absent from the table.
**Severity:** Minor
**Status:** Fixed 2026-07-08 — table now shows `verdict` and the `/mode` route.

### Finding 9: Scheduled-tasks gRPC/REST/CLI surface was entirely undocumented on `mecated.md`/`grpc-http.md`
**Reality:** `--scheduler`/`--scheduler-tick-interval`/`--scheduler-min-interval`/`--scheduler-max-concurrent-fires` flags, the `mecated schedules` CLI subcommand group, `ScheduleService`, and the `/v1/schedules` REST routes had no mention or cross-reference.
**Severity:** Moderate (coverage gap)
**Status:** Fixed 2026-07-08 — added a flag table + subcommand note to `mecated.md`, an RPC summary + REST cross-reference to `grpc-http.md`, both pointing to the new dedicated scheduled-tasks page for full detail.

### Finding 10: `extension-points/hook-runner.md`'s `HookOutcome` struct and veto framing were incomplete
**Reality:** `governance.HookOutcome` (`engine/governance/hookevent.go`) gained a fourth field, `AskApproval bool` (ADR 0062), which refines a `PreToolUse` block into an interactive approval ask instead of a hard dead end. The doc's flat "only a PreToolUse block is a real veto" framing didn't mention this. A new optional `port.HookApprovalLearner` interface was also undocumented.
**Severity:** Moderate
**Status:** Fixed 2026-07-08 — struct, field description, and both veto-framing sentences updated; `HookApprovalLearner` mentioned.

### Finding 11: `extension-points/index.md`'s composition schematic calls two constructors that don't exist / have the wrong signature
**Claim:** `slogdiag.New(slog.Default())` and `wallclock.New()`.
**Reality:** `slogdiag.New` takes `(io.Writer, bool, port.Level)`, not a `*slog.Logger` — the logger-wrapping constructor is `slogdiag.NewFromLogger(l *slog.Logger)`. `wallclock.New()` doesn't exist; `wallclock.Clock` is a zero-value-ready struct with no constructor. This is the same class of error round 2 fixed in this exact code block.
**Severity:** Moderate
**Status:** Fixed 2026-07-08.

### Finding 12: `extension-points/index.md`'s conformance-suite list was incomplete
**Reality:** Missing `eventlogconformance` and `scheduleconformance` (both exist under `engine/adapter/`).
**Severity:** Minor
**Status:** Fixed 2026-07-08.

### Finding 13: `extension-points/tool-catalog.md` said `NewToolResult`/`NewToolError` are "the two constructors"
**Reality:** A third, `session.NewToolResultWithParts`, backs typed tool results.
**Severity:** Minor
**Status:** Fixed 2026-07-08 — now notes the third constructor and cross-links the new MCP typed-results section.

### Finding 14: `what-you-get/memory.md` documented a fabricated "Forget" tool
**Claim:** The "six memory tools" table listed seven rows, including a model-callable `Forget` tool that "removes an entry by key."
**Reality:** `internal/adapter/memory.Tools(store)` registers exactly three per-project tools (Remember, Recall, SearchMemory) — there is no `Forget` tool anywhere in the codebase (confirmed by repo-wide grep). The underlying `tool.MemoryStore` interface does have a `Forget(key)` *method*, but it's used internally by consolidation (to drop stale entries) and by the gRPC memory-store driver — never exposed as an agent-facing tool. With the fabricated row removed, the table has exactly six entries, matching the "six memory tools" heading and AGENTS.md's own count. `core-tools.md`'s memory-tools table was independently checked and was already correct (six tools, no Forget) — this defect was isolated to `memory.md`.
**Severity:** Critical (a fabricated tool name in developer-facing docs — a reader building against this would look for a tool call that doesn't exist)
**Status:** Fixed 2026-07-08.

---

## Verified correct (spot-checked, no finding)

- **mecated/mecak8s flags and defaults** — grpc/http/metrics addresses, `--client-ca` (not `--tls-ca`), all LLM-resilience timeouts and retry/breaker defaults, session-lease TTL and renew interval, kustomize topology (10 resources), RBAC verbs, PDB, graceful-shutdown timings.
- **mecatequi** — exit-code map, `Summary` fields (apart from Finding 6), all other flags/defaults, git-diff-patch mechanics.
- **The full gRPC RPC tables and HTTP session/team endpoint tables** (apart from Findings 7–9) — every RPC name and field checked against `contracts/proto/mecatl/v1/*.proto`.
- **`embed-engine.md`'s Go code block** — `agent.NewEngine(agent.Deps{...})`, `permpolicy.NewPolicy`, `session.New`, `session.Limits`, `memfs.NewWorkspace`, `mockllm.New`, `Run.Events()`/`.Approve()` — all compile against current source.
- **`llm-provider.md`, `permission-policy.md`, `session-store.md`, `session-lease.md`, `tool-catalog.md` (apart from the two findings above)** — every interface signature, constant, and conformance-suite entry-point checked against `engine/port`, `engine/governance`, `engine/adapter/*`.
- **`agent-definitions.md`** — every `AgentDef` field, byte caps, origin tiers, and (newly written) writable-specialist prose internally consistent with the actual `engine/agent/subagent.go` rejection rules.
- **`core-tools.md`, `hooks.md`, `engine-and-session.md`, `observability.md`** — tool catalog tables, hook exit-code semantics (shell hooks genuinely cannot set `AskApproval` — that's guardrails-only, so `hooks.md`'s simpler "exit 2 = veto" framing is accurate as written), `session.New` signature, and metrics claims all check out.
- **`cloud-native-kit.md`'s phase status** (apart from Finding 2) — all four ADR-0027 phases correctly described as shipped; lease-adapter table accurate.
- **`demo.md` transcripts** — re-ran `go run ./cmd/mecademo` and diffed the output against the doc's transcript byte-for-byte, including event sequence numbers and usage figures.

---

## Process note

Two of the eight parallel audit agents (`what-you-get` and `extension-points` clusters) went silent on their first attempt — completing without ever returning a findings report, even after a direct follow-up ping. `extension-points` succeeded on a second, freshly spawned attempt. `what-you-get` failed silently twice; that cluster's audit was ultimately completed manually (targeted grep/read verification against source, same methodology) rather than via a third agent attempt, which is how Finding 14 (the fabricated tool) was caught.

## Overall assessment

**Accuracy confidence: High**, with one notable exception (Finding 14) that predates this round and slipped through both prior audits — a fabricated tool name is a more serious defect than the version-number/flag drift that made up most of this round's findings. All 14 findings are fixed, all 4 new-content sections are written and source-verified, the branch is rebased onto current `origin/main`, and `task lint`, `task test` (root + engine + engine-standalone), `task site:build`, `task docs` (0 broken links/anchors/orphans), and `go run ./cmd/mecademo` all pass clean.

# mecatl live e2e suite

A **live**, ginkgo-driven end-to-end suite: it spawns `./bin/mecated` against
**OpenRouter** (real model calls, real money — fractions of a cent per run) and
drives full runs over the gRPC `Converse` stream using `cmd/mecatui/client` —
the exact client package mecatui is built on, so the TUI's wire path
(CreateSession → OpenConverse → event translation → approvals) is covered
transitively. There is no teatest-live lane in v1.

Everything here carries the `e2e` build tag: `task build` / `task test` /
`task lint` never compile it, and ginkgo/gomega never enter their build graphs.

## Running it

```sh
export OPENROUTER_API_KEY=$(cat ~/Development/api-keys/ozzrouter.io)  # the conventional key location; export in-shell, never on a logged command line
task e2e            # task build first, then: go test -tags e2e -count=1 -timeout 60m ./e2e/...
```

Timeouts are layered: per-run driver timeouts (cancel + drain → a TIMEOUT
classification in the failure report), per-spec ginkgo `SpecTimeout`s (driver
timeout + 30s; flake retries get a fresh one), and the outer
`go test -timeout 60m`. The per-spec budgets × attempts sum to ~50m worst-case,
so the clean failure path (transcript + AfterSuite teardown + cost ledger)
always fires before go test's panic path — see the arithmetic in
`e2e/suite_test.go`.

Environment knobs (all optional):

| Variable | Default | Meaning |
|---|---|---|
| `MECATL_E2E_TARGET` | (unset → spawn local) | `host:port` of an existing mecated; skips the local spawn |
| `MECATL_E2E_MODEL` | `anthropic/claude-haiku-4.5` | default-lane model for all tool scenarios (see "Prompt phrasing vs the upstream prompt filter" for why the OpenAI-family lane was demoted) |
| `MECATL_E2E_MODEL_SECONDARY` | `openai/gpt-4.1-mini` | second lane (single-turn smoke only); `skip` disables it |
| `MECATL_E2E_SLOT_CHEAP_MODEL` | `google/gemini-2.5-flash` | compaction model-slot spec only: inexpensive distinct model that supports the native cache-breakpoint lane |
| `MECATL_E2E_MAX_RUN_TOKENS` | `50000` | `--max-run-tokens` for the spawned server (a single full-catalog turn is ~5-6k input tokens; a runaway brake, not a cost control — raised 20k→50k for multi-turn + cross-restart headroom against live-model verbosity drift) |
| `MECATL_E2E_MAX_TEAM_TOKENS` | `60000` | `--max-team-tokens` for the spawned server |
| `MECATL_E2E_COMPACTION_WINDOW` | `2000` | compaction spec only: the `--context-window-override` for its own spawn (trigger = 0.8 × the estimated complete request) |
| `MECATL_E2E_COMPACTION_MAX_RUN_TOKENS` | `300000` | compaction + model-slots specs: `--max-run-tokens` for their own small-window spawns. The model-slots case deliberately grows enough deterministic history to force a reducing cascade summary; this is a runaway safety rail, not a cost control. |
| `MECATL_E2E_WORKSPACE` | — | remote target only: absolute workspace root on the server host (required) |
| `MECATL_E2E_METRICS_URL` | — | remote target only: the `/metrics` URL (metrics spec Skips without it) |
| `MECATL_E2E_AUTH_TOKEN` | — | remote target only: bearer token |

## What the local target spawns

`harness.Local` runs `bin/mecated` with fully ephemeral state under
`<repo>/.scratch/e2e-<timestamp>-<rand>/` (never `/tmp` — house rule): fake
`HOME`/`XDG_*`, a git-initialised fixture workspace, JSONL store, memory dir,
user-model dir, soul file, and the checked-in skill fixtures laid out in the
conventional locations (`~/.claude/skills/greet` under the fake HOME;
`<workspace>/.claude/skills/repo-fact`). Flags (all verified against
`cmd/mecated/main.go`):

- loopback TCP on harness-picked free ports (`--grpc-addr`/`--http-addr`/
  `--metrics-addr`; mecated has **no UNIX-socket listen mode** — the TUI's UDS
  hosting is an embedded-server construct, not a daemon flag)
- `--skills-conventional` + `--trust-project` (workspace skill tier is
  trust-gated)
- `--soul-file`, `--memory-dir`, `--user-model-dir`, `--store-dir`
- `--permission-config <root>/permissions.yaml` — CLI-scope allows for
  `Skill`/`Parallel`/`Team` (those resolve to Ask otherwise); everything else
  keeps the production posture and the driver auto-DENIES unexpected asks
- `--max-run-tokens` / `--max-team-tokens` (the budget brakes)
- `--toolhive=false` (hermetic: never adopt the developer's running workloads)

The spawn sets `SysProcAttr.Pdeathsig = SIGTERM` (orphan prevention: a
hard-killed test runner takes mecated with it). `Pdeathsig` exists only on
Linux, so the live harness is Linux-only by design.

The subprocess environment is **minimal and explicit**: `PATH`, the fake
HOME/XDG dirs, and `OPENROUTER_API_KEY` — deliberately *not* `os.Environ()`, so
a developer's `OPENAI_API_KEY`/`ANTHROPIC_API_KEY` can't flip provider
detection. The provider is pinned per session via the `openrouter` provider id
on `CreateSession`.

## Scenarios

One ordered root container (ginkgo randomizes top-level containers, so ordering
+ the canary gate need a single root; that is also why the feature specs live
in `e2e/*_test.go` rather than a separate `e2e/features/` package — a second
package would need its own suite bootstrap and its own server):

1. **provider smoke** — one-word turn, `stop=end_turn`, usage > 0. The canary:
   its failure skips every dependent scenario. Secondary OpenAI-family lane
   included (skippable).
2. **global skill** — `greet` from the fake `~/.claude/skills`; asserts the
   `Skill` tool.call + non-error result, AND that zero interactive approvals
   were needed (the `permissions.yaml` CLI-scope allow is the wiring under
   test — no `ApproveTools` backup).
3. **workspace skill** — `repo-fact` from `<workspace>/.claude/skills`.
4. **parallel subagents** — two `Subagent` calls in one message; asserts ≥2
   distinct child ids + ≥2 `agentId:` trailers (N results, not temporal overlap).
5. **Parallel construct** — one 2-branch `Parallel` call; asserts
   `parallel.start(BranchCount=2)` → branch events → `parallel.end`.
6. **teams** — 3-member roster (lead+2 workers) recording findings; asserts
   `team.start`, **recorded findings** in the snapshots (the goal forces two
   `RecordFinding` calls; an empty snapshot does not pass), `team.end`, and the
   `Team id:` line in the tool result (which also rides the `joinTeamFallback`
   path — a degraded pass is accepted and documented in the spec).
7. **metrics** — scrapes `/metrics` after the delegation scenarios; asserts
   `mecatl_tool_calls_total{role="main"} > 0` and a `role="subagent"` series.
   Local target only (a remote server's pre-existing counters make `>0`
   vacuous); the subagent-series assertion is gated on the subagents spec
   having observed real child activity, to avoid double-reporting.
8. **user memory** — `RememberUser` event + the fact lands in
   `<user-model-dir>/memory-v2.json` (file check is local-target only).
9. **project memory** — `Remember` + `<memory-dir>/memory-v2.json`.
10. **compaction (task survives a real compaction)** — spawns its own mecated
    with `--context-window-override 2000` (env: `MECATL_E2E_COMPACTION_WINDOW`) and
    drives several turns on one session. The first user turn records a distinctive
    deferred task; later fixture Reads grow persisted history before a terse `GO`
    trigger. The automatic check estimates the complete provider request: rendered
    system prompt, ephemeral fragments, messages, typed tool results, and advertised
    tool schemas. Only persisted history is compactible, so the fixed prompt and tool
    surface remains after each pass. The deliberately small window can therefore
    cause repeated compactions; the test does not depend on one exact firing turn.
    The sized `compaction-input.txt` Read still ensures there is substantial history
    to reduce. The structural gate requires at least one `EvCompaction` and reads the
    saved post-compaction snapshot to prove the first user instruction remains
    verbatim. A quarantined behavioral check then verifies that `GO` produces the
    PINEAPPLE Write without treating model compliance as a deterministic harness
    contract. Each attempt gets a fresh workspace with no stale `result.txt` bleed.
    `--max-run-tokens 100000` keeps the
    cumulative-session budget from tripping over 6 reused turns. Haiku lane,
    `FlakeAttempts(2)`, local-target only (own-spawn flag + workspace read).
    Highest-fidelity guard for the role-blind-tail compaction bug
    (`docs/adr/0012-compaction.md`, the back-snap section). A cascade / tier-4 variant
    (forcing the LLM summariser, env-gated `MECATL_E2E_COMPACTION_CASCADE`) is a
    deferred follow-up.
11. **soul** — deterministic: the `soul ENABLED (user provenance...` composition
    fact in the captured stderr; the behavioural `SOUL-OK:` marker is a
    `quarantine`-labelled spec that reports but never fails.
12. **guardrails slot-enable (issue #159)** — spawns its OWN mecated with a
    slot-ONLY guardrails config (`--model-slot guardrail=cheap` +
    `--model-alias cheap=<checker-model>`, NO `--guardrails-model`) and asserts
    (A) the build-once `guardrails: ON` startup line names the RESOLVED slot
    model with `via slot \`guardrail\`` provenance (the must-fix #1: the line
    reports the resolved checker, not an absent gate value; must-fix #2: the
    slot alone enables); (B) a WebSearch tool call — observed by the default
    advisory set — drives a real PreToolUse/PostToolUse verdict round-trip on
    the slot model against the live provider and the run completes cleanly.
    Env: `MECATL_E2E_GUARDRAIL_MODEL` (default `openai/gpt-4.1-mini`).
    `FlakeAttempts(2)`, local-target only (own-spawn slot flags).
13. **approve-after-kill (cloud-native Phase 2)** — raises a real `Write`
    permission ask on the haiku lane, **SIGKILL**s the mecated process WITHOUT
    cleanup, restarts a SECOND mecated over the SAME `--store-dir`, POSTs
    `/v1/sessions/{id}/approve` (`allow_once`) to the second process's HTTP
    listener, and asserts the pending `Write` ran EXACTLY ONCE (real `note.txt`
    with content `survived`) and the resumed run reached `end_turn`. Live
    counterpart of the offline two-Build gate `TestApproveAfterRestartE2E`
    (`internal/app/`). Lane-pinned to haiku because it needs a real tool call
    (the OpenAI lane content-filters tool-bearing requests). Spawns its OWN
    process pair (`harness.NewLocal` + `harness.NewLocalSharingStore`,
    `(*Local).Kill`, `harness.ApproveOverHTTP`); local-target only.

All assertions are event-stream / side-effect assertions — never model prose.
`FlakeAttempts(2)` is on the cheap specs — provider smoke, skills, memory, and
approve-after-kill — plus the live team spec, whose full assertions are rerun once
to absorb provider variance. Other expensive delegation scenarios are not retried.

### Harness primitives for the restart scenario

The approve-after-kill spec needs three seams the suite target does not:

- `harness.NewLocalSharingStore(prior, extraArgs...)` — a SECOND mecated whose
  state lane (`--store-dir`/`--memory-dir`/`--workspace`/…) points at a prior
  `Local`'s tree, with its OWN home/XDG/artifacts + its OWN loopback ports. The
  prior MUST be dead first (two live daemons would race the JSONL store).
- `(*Local).Kill()` — SIGKILL + reap, leaving the state tree intact (the
  "disposable process" death). Distinct from `Close` (SIGTERM-first graceful);
  `Close` after `Kill` is a safe no-op.
- `harness.ApproveOverHTTP(ctx, httpAddr, sessionID, askID, verdict)` +
  `harness.DrainSSE` — a stdlib `net/http` client that POSTs the approve body and
  returns the rehydrate-relay SSE stream. `(*Local).HTTPAddr()` exposes the
  listener.

## Artifacts

Every run writes a JSONL transcript per scenario under
`<scratch-root>/artifacts/<scenario>/transcript-*.jsonl` (prompt, every
translated event with its Go type, ask/approval ledger, timeout markers), and
mecated's combined stdout+stderr is captured to `artifacts/mecated.log`. A
failing spec
attaches a self-diagnosing report (network-vs-model-vs-harness classification,
transcript path, resolved model, usage, stderr tail) via `AddReportEntry`. The
suite ends with a cumulative token/cost estimate (delegation children included).

## Permission posture

The driver answers permission asks by policy: allow-once for the scenario's
`ApproveTools`, **deny** for everything else (recorded in the transcript and
the failure classification). A live scenario must never park on a human.

## Prompt phrasing vs the upstream prompt filter

OpenRouter routes `openai/*` to OpenAI **and Azure** endpoints. With mecatl's
full request shape (big instructions + the tool catalog), certain innocuous
imperative phrasings get DETERMINISTICALLY rejected with
`response incomplete: content_filter` in ~1-2s (an input-side prompt-shield,
not output moderation). Probe-verified on `openai/gpt-4.1-mini`:

- `"Reply with the single word ok. Do not use any tools."` → content_filter, 3/3
- `"Reply with exactly the single word: ok. Do not call any tools."` → clean, 3/3
- `"...then follow its instructions to greet Ozz..."` → content_filter
- `"...durable fact about me: my name is Ozz..."` → content_filter

The same text WITHOUT mecatl's tool catalog passes — it is a joint score over
the whole request. Worse: the filter also hits MODEL-AUTHORED child prompts
(the parent paraphrases Subagent goals into the child's first turn), so no
amount of suite-side prompt rewording makes the OpenAI-family lane reliable.
That is why the **default lane is `anthropic/claude-haiku-4.5`** (Bedrock
endpoints, no such filter observed) and the OpenAI-family lane is the
single-turn secondary smoke. If you edit a prompt and a scenario starts
failing instantly with `content_filter`, phrasing is the first suspect.

Two account-level realities, verified live with this key:

- `openai/gpt-4o-mini` is fully blocked for *tool-bearing* `/responses`
  requests — 404 "no endpoints available matching your guardrail restrictions
  and data policy".
- The filter behaviour above applies to the other OpenAI-family models
  (`gpt-4.1-mini` verified; all route to OpenAI+Azure endpoints).

A possible operator-side fix is ignoring the filtered provider at
https://openrouter.ai/settings/preferences (mecatl deliberately has no
provider-routing knob — `port.LLMRequest` stays provider-neutral).

## Findings protocol

If a scenario fails because the FEATURE is broken live (not the suite), don't
patch the harness around it: capture the artifact, mark the spec
`Skip("FINDING: ...")` with a precise description, and file it for its own dev
pipeline.

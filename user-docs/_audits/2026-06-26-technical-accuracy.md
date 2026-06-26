# Audit: Technical Accuracy Review — 2026-06-26

## Expert persona

**Role:** Technical accuracy reviewer — an engineer who has read the implementation closely and is checking the consumer docs against what the code actually does.

**Attack vector:** Wrong numbers (defaults, timeouts, counts), overclaimed behaviors, stale descriptions superseded by an ADR, missing prerequisites, security claims that could mislead an operator, and demo output that no longer matches actual binary output.

**Model:** Claude Opus 4.8 (claude-opus-4-8)

**Scope:** All files added/changed in the `docs-toolkit-consumer` branch relative to `main`. Ground truth sources in priority order: `CLAUDE.md` (the AGENTS.md coding contract with authoritative implementation notes), ADRs in `docs/adr/`, and source files in `cmd/` and `engine/`.

---

## Cost

| Component | Cost |
|---|---|
| Orchestration (Sonnet, main session) | ~$0.34 |
| Subagent (Opus, audit execution) | ~$2.24 |
| **Total** | **~$2.57** |

---

## Findings

### Finding 1: Demo output is missing the `guardrails: OFF` header line

**Doc:** `getting-started/demo.md` (~line 21, the `## Run it` code block)
**Claim:** The shown terminal output goes straight from `Driving a real agent.Engine: ...` to `[001] turn=0 turn.start`.
**Reality:** `cmd/mecademo/main.go` prints an explicit third header line immediately after: `guardrails: OFF (no checker model configured; bind the \`guardrail\` model slot or set --guardrails-model to enable)`. The transcript in the doc omits this line and no longer matches actual output.
**Severity:** Minor

**Status:** Fixed 2026-06-26 — transcript updated to include the guardrails line. Additionally, running the binary revealed the first-act event sequence had diverged further than the Opus reviewer noted (new event types `session.init`, `user_prompt`, `turn.end`, `approval` shifted all sequence numbers). The full transcript was replaced with captured live output.

---

### Finding 2: Demo transcript omits two additional offline demo acts

**Doc:** `getting-started/demo.md` (transcript ends at the first `result` event; framing says "shows the complete loop")
**Claim:** The offline run ends at `[012] ... result stop=end_turn`.
**Reality:** `cmd/mecademo/main.go` runs two additional acts when offline: a `=== mecatl team demo (offline) ===` act (lead + worker, rounds, consolidated report) and a `=== mecatl background subagent demo (offline) ===` act (background subagent, status events, harness note). A reader running `go run ./cmd/mecademo` sees substantially more output than the doc shows. The framing "shows the complete loop" is narrower than what the binary now does.

Note: The first-act event lines are accurate — token counts, write result, ask reason all verified against source.

**Severity:** Moderate (this is the first-impression "60 seconds" page; output materially differs from reality)

**Status:** Fixed 2026-06-26 — added Act 2 (team demo) and Act 3 (background subagent) sections with captured live output. Updated intro to describe all three scenarios. Event table expanded to cover `session.init`, `user_prompt`, `turn.end`, and `approval` event types.

---

### Finding 3: Agent-loop doc says "four terminal states" but there are three

**Doc:** `what-you-get/agent-loop.md` (~line 164, "Cancellation & terminal states")
**Claim:** "Every run ends in exactly one of **four** terminal states:" — followed by a table with only three rows (`completed`, `cancelled`, `failed`).
**Reality:** `engine/session/session.go` defines exactly three terminal states: `StateCompleted`, `StateFailed`, `StateCancelled`. The word "four" is wrong (likely a leftover from counting the four `completed` stop-reason variants listed in the first table row). The table itself is correct; only the word "four" is wrong.
**Severity:** Minor

**Status:** Fixed 2026-06-26 — changed "four" to "three".

---

### Finding 4: StreamIdleTimeout 120s→180s — NOT present in these docs (known issue resolved)

**Doc:** N/A
**Notes:** The project memory flagged a stale-doc issue where `StreamIdleTimeout` was documented as 120s but the default is 180s. These consumer docs correctly show 180s throughout (`what-you-get/observability.md`). The stale value does not appear here. Noting only to close the loop.
**Severity:** N/A — already correct

**Status:** Resolved (not present)

---

### Finding 5: Extension-points and deployment sub-pages are unwritten stubs

**Doc:** All of `extension-points/` (llm-provider, session-store, session-lease, hook-runner, permission-policy, tool-catalog, agent-definitions), all of `deployment/` (embed-engine, mecated, mecak8s, grpc-http, mecatequi), `api-stability.md`, `cloud-native-kit.md`
**Claim:** Every page is a `:::note[Coming soon] This page is being written.` stub.
**Reality:** The stub pages have no technical content to be wrong, but `intro.md` and `deployment-decision.md` both link to them as if substantive ("see the deployment guide for it", a full seam table with links, etc.). A reader following "What's next" links lands on empty pages.
**Severity:** Minor (no false claims; the navigation oversells what exists)

**Status:** Open (by design — these pages are planned, not forgotten)

---

## Verified correct (spot-checked, no finding)

The following claims were verified against source and are accurate:

- **Resilience defaults** (observability.md): max-attempts 3, per-attempt-timeout 300s, stream-idle 180s, breaker-threshold 5, breaker-cooldown 30s, flight-recorder on by default — all match `cmd/mecated/main.go`.
- **Flight recorder** "8 MiB / 5s window" matches source.
- **metrics-addr default** `127.0.0.1:9090` matches source.
- **Permissions default ruleset table** (permissions.md): Read/Grep/Glob/WebFetch/WebSearch/Subagent → allow; Bash/Edit/Write/SkillDraft/Team → ask — matches floor rules in `internal/app/build.go` exactly, including memory/soul:apply/Inspect floor-allows.
- **Posture ladder + sandbox refusal** (`MECATL_SANDBOX=1`/`IS_SANDBOX=1`) matches `internal/app/posture.go`.
- **Soul 20 KiB cap**, **memory tier-0 index 200 entries / 8 KB**, **user-model default path** all match source.
- **Hook 30s timeout**, **MCP dial 30s**, **`--mcp-resource-tools`/`--mcp-prompts`/`--toolhive` default true** all match source.
- **SubagentStatus wait cap 120s** matches source.
- **mecak8s**: replicas 2, `--headless` default true, `--posture` default `auto`, no `--redis-url` on mecated (only mecak8s), Prometheus/OTel/perf-mcp stripped — all match `cmd/mecak8s/`.
- **`mode: read-write` direct-write** description is consistent with ADR 0041 (current), not the superseded ADR 0040 path.
- **PostToolUse block rewrites result, does not veto** (hooks.md) matches the guardrails implementation.

---

## Overall assessment

**Accuracy confidence: High.** Every numeric default, flag name, and behavioral invariant spot-checked against source matched. The permissions, hooks, observability, memory, and MCP client docs are tight and current. The only real defects are in the front-door pages: the demo transcript is stale (missing the guardrails line and two extra offline acts), and the agent-loop doc has one wrong word ("four" → "three"). The larger gap is completeness — roughly half the doc set is unwritten stubs — but that is planned scope, not an error. Nothing found is dangerous or security-misleading.

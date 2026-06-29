# Audit: Technical Accuracy Review — Round 2 — 2026-06-28

## Expert persona

**Role:** Technical accuracy reviewer — an engineer who has read the implementation closely and is checking the consumer docs against what the code actually does.

**Attack vector:** Wrong Go API signatures in illustrative code blocks, wrong flag names and defaults, overclaimed behaviors, stale ADR references, missing security/operational caveats.

**Model:** Claude Opus 4.8 (claude-opus-4-8)

**Scope:** The 16 pages written in this session (extension-points, deployment, cloud-native-kit, api-stability). The pages from the first session were already audited in `2026-06-26-technical-accuracy.md`.

---

## Cost

| Component | Cost |
|---|---|
| Orchestration (Sonnet, main session) | ~$0.12 |
| Subagent (Opus, audit execution) | ~$4.16 |
| **Total** | **~$4.28** |

---

## Findings

### Finding 1: `llmresilience` wiring code uses a non-existent constructor and option functions

**Doc:** `extension-points/llm-provider.md` (~line 247, "Consumer note" code block)
**Claim:**
```go
decorated := llmresilience.New(myProvider,
    llmresilience.WithStreamIdleTimeout(3*time.Minute),
    llmresilience.WithPerAttemptTimeout(5*time.Minute),
)
engine := agent.NewEngine(agent.Config{...}, agent.Deps{LLM: decorated, ...})
```
**Reality:** The only constructor is `llmresilience.Wrap(inner port.LLMProvider, cfg Config)` — no `New`, no functional options, configured via a `Config` struct. Also `agent.NewEngine` takes a single `agent.Deps` argument, not `(agent.Config, agent.Deps)`.
**Severity:** Moderate (presented as paste-ready consumer wiring; won't compile)

**Status:** Fixed 2026-06-28 — changed to `llmresilience.Wrap(myProvider, llmresilience.Config{...})` and `agent.NewEngine(agent.Deps{...})`.

---

### Finding 2: Composition schematic uses `agent.Config`, wrong field names, and `EventLog` as a Deps field

**Doc:** `extension-points/index.md` (~lines 127–136, composition schematic)
**Claim:** `agent.NewEngine(agent.Config{ LLM, Store, Policy, Hooks, EventLog, Recorder, Diag, Clock })`
**Reality:** No `agent.Config` exists; the single-arg constructor is `agent.NewEngine(agent.Deps{...})`. Field name differences: `Diag` → `Diagnostics`, `Recorder` → `ToolCallRecorder`. `EventLog` is NOT a `Deps` field at all — the engine never imports `port.EventLog`; the service layer (not the engine) appends to it.
**Severity:** Moderate (actively misleads on the EventLog seam — one of the more subtle invariants)

**Status:** Fixed 2026-06-28 — corrected constructor, field names, removed `EventLog` from Deps with an explanatory comment, updated the follow-on commentary to say `agent.Deps`.

---

### Finding 3: `storeconformance.RunSuite` does not exist

**Doc:** `extension-points/index.md` (~line 160)
**Claim:** `storeconformance.RunSuite(t, func() port.SessionStore { return yourstore.New() })`
**Reality:** The exported entry point is `storeconformance.Run(t, func(t *testing.T) port.SessionStore)`. No `RunSuite`, and the factory takes a `*testing.T`.
**Severity:** Moderate (copy-pasted code won't compile)

**Status:** Fixed 2026-06-28 — corrected to `storeconformance.Run(t, func(t *testing.T) port.SessionStore { ... })`.

---

### Finding 4: `--tls-ca` does not exist on mecated/mecak8s — the flag is `--client-ca`

**Doc:** `deployment/grpc-http.md` (~line 396, TLS/mTLS table)
**Claim:** `--tls-ca` — "Path to a CA certificate for mTLS client verification (PEM)"
**Reality:** The server-side mTLS CA flag is `--client-ca` on both `mecated` and `mecak8s`. `--tls-ca` exists only on the `mecatui` client (for verifying the remote server). `deployment/mecated.md` already uses the correct `--client-ca`.
**Severity:** Moderate (a reader copying this flag gets "flag provided but not defined")

**Status:** Fixed 2026-06-28 — changed to `--client-ca`.

---

### Finding 5: Session-lease renewer interval stated as TTL/2; actual default is TTL/3

**Doc:** `extension-points/session-lease.md` (~line 85, sequence diagram note)
**Claim:** "renewer calls Lease.Renew every ~TTL/2"
**Reality:** Default renew interval is TTL/3 (`LeaseRenewInterval = LeaseTTL / 3`). The `mecated.md` and `mecak8s.md` deployment pages both correctly say TTL/3.
**Severity:** Minor

**Status:** Fixed 2026-06-28 — changed to TTL/3.

---

### Finding 6: Composition schematic uses wrong `openai.New` and `permpolicy.New` signatures

**Doc:** `extension-points/index.md` (~lines 110, 115)
**Claim:** `openai.New(cfg.OpenAIKey, openai.WithBaseURL(...))` and `permpolicy.New(evaluator, permStore)`
**Reality:** `openai.New` takes options only — the API key is `openai.WithAPIKey(...)`, not a positional arg. `permpolicy.NewPolicy(rules []governance.Rule, store, ...opts)` takes a rule slice, not a pre-built evaluator; there is no `permpolicy.New`.
**Severity:** Minor (labeled schematic/illustrative, but shapes are wrong enough to mislead)

**Status:** Fixed 2026-06-28 — corrected to `openai.New(openai.WithAPIKey(cfg.OpenAIKey), ...)` and `permpolicy.NewPolicy(rules, permStore)`.

---

### Finding 7: HTTP `/approve` endpoint documented as two-way only; `allow_always` missing

**Doc:** `deployment/grpc-http.md` (~lines 256–260)
**Claim:** Body is `{"ask_id":"...","allow":true}`. "`"allow":false` denies."
**Reality:** The endpoint accepts a three-way `verdict` string: `allow_once`, `allow_always`, or `deny`. The legacy `allow` bool is still accepted but only expresses two of the three outcomes. The gRPC section of the same doc correctly documents all three verdicts.
**Severity:** Minor (not wrong, but incomplete — a reader can't express "allow always" over HTTP)

**Status:** Fixed 2026-06-28 — replaced single-verdict example with three-way `verdict` examples and noted the legacy `allow` bool.

---

## Verified correct (spot-checked, no finding)

A wide set of interface signatures, flags, defaults, and behaviors were checked and found accurate:

- All `engine/port` interface signatures (LLMProvider, SessionStore, PrunableStore, EventLog, SessionLease, HookRunner, PermissionPolicy, PermissionStore, EventSink, ToolCallRecorder, Diagnostics, Clock) — exact match.
- All `Chunk` kind constants, `ProviderCapabilities` fields, `LLMRequest` fields, `Lease` fields.
- All nine `HookPhase` constants, `HookEvent`/`HookOutcome` fields.
- `Catalog`, `MustRegister`/`Register`/`ErrDuplicateTool`, `Tool`/`ToolSpec`, `NewToolResult`/`NewToolError`, MCP `mcp__<server>__<tool>` prefix.
- `AgentDef` — every field, `MaxAgentDescriptionBytes=2000`, `MaxAgentBodyBytes=32*1024`, all four `AgentOrigin` constants, discovery precedence, `--agents-dir`/`--agent-source-url`.
- `governance.Rule`/`Scope` (full iota order)/`Audience`/`Effect`, deny-dominant invariants, permstore cap 256, `permpolicy` constructors and `AllowAllFloorRules`.
- mecated flags and defaults (incl. `--mcp-prompts` default on, `--enable-teams` default true, `--client-ca`, lease TTL 30s, llm-* timeouts 300s/180s, retry/breaker defaults).
- mecak8s defaults (posture `auto`, headless `true`, `0.0.0.0`, `--redis-url`, k8s-namespace default `mecatl`, no metrics/OTel/perf-mcp), graceful shutdown 30s/60s, kustomize 10-resource list, RBAC verbs (no list/watch), PDB minAvailable 1.
- mecatequi: all flags/defaults, `Summary` struct and JSON tags, `SchemaVersion=1`, exit-code map.
- mecated graceful-shutdown timeouts (10s drain, 5s telemetry flush). All driver `--*-url` flags exist.

---

## Overall assessment

**Accuracy confidence: High.** Every port interface, struct field, flag name, default value, and kustomize topology checked out. The defects clustered almost entirely in illustrative Go code blocks on the extension-point pages — constructor names and signatures were paraphrased rather than copied from source. The only prose errors were the `--tls-ca`/`--client-ca` flag mix-up (the most likely to bite a reader) and the TTL/2 vs TTL/3 renew interval. All 7 findings are fixed. The pages are ready for human review.

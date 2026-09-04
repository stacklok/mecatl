# Platform principles

The cross-cutting invariants mecatl is built to keep. Architecture decisions
that violate one of these get pushed back. They are distilled from the
[ADRs](../adr/) and the canonical agent contract in
[`AGENTS.md`](../../AGENTS.md); each names the source that pins it. These are
*platform* principles (cross-cutting); the *behavioural* invariants — the
rules about permissions, compaction, the loop, delegation — live in
`AGENTS.md` under "Things That Will Bite You" and in
[IMPLEMENTATION-NOTES.md](IMPLEMENTATION-NOTES.md), and are pinned by named
tests.

Acceptance plans cite these as `Principle N`; the
[ac-trace](../acceptance/README.md) gate resolves the reference against this
list, and a `TestInvariant_<id>` / `TestADR_NNNN_*` test pins the ones that
carry a runtime obligation.

> **Convenience index, not a source of truth.** The canonical wording of each
> principle lives in the ADR / `AGENTS.md` section it cites — when this page
> and its source ever disagree, the source wins. When you change an invariant
> in `AGENTS.md` or supersede an ADR, update the matching summary here in the
> same change.

1. **Dependencies point inward, machine-enforced.** Domain (`session`,
   `prompt`, `governance`, `tool`), `engine/team`, and `engine/agent` never
   import an adapter, `contracts/gen`, `os`, or a provider SDK. The depguard
   allowlist, the DAG test, and the engine module boundary enforce it three
   ways. See [ADR-0036](../adr/0036-engine-module.md) and `AGENTS.md` "The
   layering rule".

2. **`engine/` is the importable core; tests are offline.** The engine is its
   own Go module, self-contained including tests — nothing under `engine/`
   imports `internal/...`, and no test hits a live model or network (the
   reference adapters `mockllm`/`memfs`/`memstore` + the conformance suites
   stand in). See [ADR-0036](../adr/0036-engine-module.md).

3. **Ports are provider-neutral and stay narrow.** `port.LLMRequest` carries
   no provider-private knobs; provider specifics are adapter-construction
   Options, never new request fields. A guard test tripwires silent widening.
   See `AGENTS.md` "The LLM adapters are stateless".

4. **Permissions resolve deny-dominant; trust is gated.** A deny in any scope
   is absolute; the posture ladder (`strict < trusted < auto < yolo`) folds
   max-tier; an untrusted workspace degrades honestly rather than silently
   widening. See [ADR-0022](../adr/0022-allow-all-posture.md),
   [ADR-0023](../adr/0023-workspace-trust.md), and `AGENTS.md` "Preserve
   these invariants".

5. **The session is an aggregate; history is never unpaired.** Mutate the
   `Session` through its methods; compaction and every run-entry seam
   (Reopen / Interrupt / Recover) guarantee `ValidateToolPairing` — an
   orphaned tool result is a provider 400, so the tree never emits one. See
   [ADR-0012](../adr/0012-compaction.md) and `AGENTS.md` "Compaction must
   NEVER emit unpaired history".

6. **The loop is storage-agnostic; durability lives at the relay.** The loop
   only emits events; the durable `EventLog`, compaction archives, and
   verdict replay are consumed at the composition/relay layer, never from
   `engine/agent`. See [ADR-0027](../adr/0027-cloud-native.md).

7. **Diagnostics flow through the injected port, never ambient slog.**
   `port.Diagnostics` is the single chokepoint; the loop emits exactly its
   documented lines and no more. See [ADR-0020](../adr/0020-diagnostics.md).

8. **Every agent-facing shell runs secret-scrubbed.** The command runners get
   `envscrub.Scrub(os.Environ())`; the `os.Environ()` passthrough is never
   reintroduced. See `AGENTS.md` "Things That Will Bite You" (Finding B).

9. **A model-facing gate ships its model-visible instruction + a test.**
   Any affordance that depends on the model's behaviour ships both the
   prompt-layer instruction and an executable test asserting that instruction
   lands in the built system prompt. See
   [ADR-0070](../adr/0070-model-visible-affordance-gate.md) and `AGENTS.md`
   "A model-facing gate/affordance…".

10. **Docs are gated artifacts.** The configuration reference is generated,
    never hand-edited; the matlatl strict link gate fails a PR on a broken link,
    orphan, or unreachable doc. ADRs are frozen; new decisions are new ADRs. See
    [ADR-0002](../adr/0002-documentation-lifecycle.md) and
    [ADR-0003](../adr/0003-consolidate-design-records-as-adrs.md).

## See also

- [`AGENTS.md`](../../AGENTS.md) — the canonical contract (the behavioural
  invariants live there).
- [IMPLEMENTATION-NOTES.md](IMPLEMENTATION-NOTES.md) — the dense per-subsystem
  reference.
- [Development process](../development-process.md) — the spine these
  principles are cited from.

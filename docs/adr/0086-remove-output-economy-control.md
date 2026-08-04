# ADR 0086 — Remove the output-economy control surface

- Status: Accepted
- Date: 2026-08-03
- Scope: default prompt composition, CLI/config compatibility, and prompt-performance measurement
- Supersedes: [ADR 0041](./0041-output-economy-default-prompt.md)
- Superseded by: [ADR 0089](./0089-cli-clean-break-grammar.md) (decision item 4 — the one-release parse-compat window closed; the shim is deleted)

## Context

ADR 0041 combined two decisions: load-bearing, always-on correctness guidance in
`engine/prompt/builder.go` (`defaultTone`), and an optional `normal`/`terse` control
surface that appended an answer-length cap. ADR 0054 subsequently rebalanced the
always-on wording to protect investigation and reasoning depth.

The optional tier no longer earns its complexity. It adds three flags, a YAML setting,
composition folds, generated configuration, documentation, and a dedicated benchmark,
while changing only prompt wording. The `terse` delta is also the most likely part to
over-steer a model from concise delivery into shallow work. Removing the whole default
tone would be worse: its investigation-depth, minimum-change, read-before-edit,
trust-boundary, and safety clauses prevent known correctness regressions.

Existing deployments may still pass `--output-economy` or retain
`output-economy:` in `settings.yaml`. Rejecting those immediately would turn a prompt
simplification into an avoidable startup outage.

## Decision

Remove output-economy as an active behavior and public control surface:

1. Delete the `terse` tone delta, composition fields/fold, and the dedicated
   output-economy performance scenario.
2. Preserve `defaultTone` byte-for-byte. Keep executable coverage of its
   investigation-depth, minimum-change, safety, read-before-edit, and trust-boundary
   clauses.
3. Remove `--output-economy` from normal help in `mecated`, `mecatui`, and
   `mecatequi`, and remove `output-economy:` from generated settings skeletons and
   reference documentation.
4. For one release, keep hidden parser compatibility. A legacy CLI flag is a no-op
   and emits a deprecation warning telling the operator to remove it. A legacy
   top-level YAML key remains leniently parseable, has no effect, and emits the same
   warning through the existing diagnostics seam. The compatibility captures are
   marked for follow-up removal.
5. Keep provider-neutral request types unchanged. This remains prompt/composition
   cleanup, not a new `port.LLMRequest` option.

## Consequences

- There is one default prompt behavior instead of a hidden style tier; all three
  binaries construct the same tone regardless of legacy inputs.
- Operators get a migration window instead of an immediate unknown-flag or config
  failure. During that window, stale inputs are observable but behavior-free.
- Generated configuration and normal help no longer advertise a setting that does
  nothing.
- The dedicated scripted benchmark is removed. General loop and allocation scenarios
  continue to measure the runtime paths; prompt wording is pinned by focused tests
  rather than a fixed-output mock benchmark that cannot measure model response quality.
- The compatibility-only parser fields are temporary debt and must be removed after
  the announced release window.

## See also

- [ADR 0041](./0041-output-economy-default-prompt.md) — the superseded combined decision.
- [ADR 0054](./0054-reasoning-rebalance-default-prompt.md) — the surviving reasoning-depth rebalance.
- `engine/prompt/builder.go` (`defaultTone`) — the preserved default guidance.
- `internal/adapter/permconfig/permconfig.go` (`parseYAML`) — the settings.yaml decode; the temporary `OutputEconomy` compatibility field cited here was deleted by ADR 0089.
- `docs/architecture.md` — current prompt behavior.

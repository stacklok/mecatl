# mecatl learning configuration format

This is the complete operator-tier `learning:` subtree for the resolved mecatl
operator settings document. The mapping is strict: use only the keys and closed
values shown below.

## Contents

- [Exact schema and defaults](#exact-schema-and-defaults)
- [Mode and activation semantics](#mode-and-activation-semantics)
- [Sensitivity and weighted admission](#sensitivity-and-weighted-admission)
- [Operator and project tiers](#operator-and-project-tiers)
- [Budget profiles](#budget-profiles)
- [Complete examples](#complete-examples)
- [Rollback and tightening blocks](#rollback-and-tightening-blocks)
- [Common errors](#common-errors)

## Exact schema and defaults

```yaml
learning:
  mode: off # off | review | auto
  sensitivity: balanced # conservative | balanced | eager
  skills:
    activation: validated # validated | evaluated
  automatic:
    cooldown: 10m
    window: 1h # 1m..24h
    max_reflections: 8
    max_tokens: 100000
    max_reflections_per_principal: 4
    max_tokens_per_principal: 50000
```

| Key | Valid values and rules | Default |
|---|---|---|
| `mode` | `off`, `review`, or `auto` | `off` |
| `sensitivity` | `conservative`, `balanced`, or `eager` | `balanced` |
| `skills.activation` | `validated` or `evaluated` | `validated` in standard app Auto configuration; specify it explicitly |
| `automatic.cooldown` | nonnegative Go duration string; `0s` disables cooldown | `10m` |
| `automatic.window` | Go duration string from `1m` through `24h`, inclusive | `1h` |
| `automatic.max_reflections` | integer `0..1000000000`, process-wide per window | `8` |
| `automatic.max_tokens` | integer `0..1000000000`, process-wide reserved tokens per window | `100000` |
| `automatic.max_reflections_per_principal` | integer `0..1000000000`, per principal per window | `4` |
| `automatic.max_tokens_per_principal` | integer `0..1000000000`, reserved tokens per principal per window | `50000` |

Zero for **any maximum** disables automatic reflection under that bound; it is
not “unlimited.” Cooldown zero disables only cooldown. Limits and cooldown state
are process-local and reset on restart. In a deployment with N replicas, the
aggregate envelope may approach N times the configured process-wide values.
These controls do not change provider-side quotas and are not cluster-global.
Reserved budget remains consumed after a reflection failure, timeout, or
abstention; queue-full does not consume it.

## Mode and activation semantics

| Mode | Automatic facts/memories | Automatically proposed skills |
|---|---|---|
| `off` | No automatic observer or reflection. Explicit `/reflect` and memory/user-model tools still work. | No automatic materialization. Direct `SkillDraft` remains inactive. |
| `review` | Eligible completions are reflected and valid proposals are durably staged; memory is not changed. | PASS/ABSTAIN proposals may be staged for review; no automatic activation. FAIL rejects. |
| `auto` + `validated` | Stage first, then conservatively promote only eligible, owned, non-conflicting facts. | PASS activates. ABSTAIN or no evaluator may activate only a non-legacy, structurally accepted/exact, evidence-backed, body-only candidate. |
| `auto` + `evaluated` | Same conservative fact behavior. | Only trusted evaluator PASS activates; ABSTAIN/no evaluator stays staged. |

Evaluator FAIL rejects under either activation policy. Evaluator infrastructure
ERROR records a generic rejected result and never activates; it cannot later be
reinterpreted as ABSTAIN. Similar candidates, collisions, unsupported assets or
scripts, untrusted/alternate project roots, and missing publication support stay
staged or reject according to lifecycle validation. `validated` is usable
body-only autonomy, not a bypass around structural, evidence, ownership, or
publication checks. Choose `evaluated` only when a trusted fixture evaluator is
actually configured and PASS-only assurance is intended.

Explicit `/reflect` bypasses automatic sensitivity thresholds, cooldown,
count/token budgets, and recent-completion admission. It still obeys coordinator
capacity, provider/model availability, timeout, ownership/provenance, and
lifecycle limits. It can therefore incur live provider cost even when mode is
off or automatic maxima are zero. Dream/user-model consolidation is an
independent operator schedule; learning mode does not enable or disable it.

## Sensitivity and weighted admission

Automatic weighted admission applies to benign main-session `end_turn`
completions. Thresholds are:

| Sensitivity | Score required |
|---|---:|
| `conservative` | 6 |
| `balanced` | 4 |
| `eager` | 3 |

Base signal weights:

| Signal | Weight |
|---|---:|
| repeated correction | 5 |
| trusted-host contradiction | 5 |
| failure recovery | 4 |
| repeated stable tool sequence | 3 |
| substantial success | 2 |

Modifiers add one each only when at least one base signal exists: at least four
model turns, at least five successful tool results, and at least 12,000 run
tokens. A genuine current principal prompt explicitly asking to remember a fact
or learn a procedure is a hard admission on `end_turn`, `max_turns`,
`max_tool_calls`, or `budget`. It bypasses score and cooldown but still consumes
count/token reservations and coordinator capacity. Historical, assistant, tool,
web, MCP, and repository content cannot hard-trigger.

## Operator and project tiers

The effective operator `learning:` block is selected as a whole; fields are not
merged across operator files. Repeatable `--permission-config` files are read in
CLI order, and the first readable, valid file with a non-null `learning:` block
wins. Unreadable or invalid files are skipped. Later explicit files cannot change
that block. The conventional user file participates only when
`--permissions-conventional` is enabled and no explicit file has already captured
learning. Its path is `$XDG_CONFIG_HOME/mecatl/settings.yaml`, falling back to
`$HOME/.config/mecatl/settings.yaml` when `XDG_CONFIG_HOME` is unset or empty.
Consequently, changing a shadowed later file has no learning effect. Resolve this
from the actual server/service/container startup arguments and environment;
remote clients do not own server learning config, and embedders use code-owned
configuration unless they explicitly wire these files.

The operator file establishes the ceiling. A trusted project-tier
`.mecatl/settings.yaml` may only tighten:

- mode in the order `off < review < auto` (for example, operator `auto` to
  project `review` or `off`);
- sensitivity in the order `conservative < balanced < eager`; and
- activation assurance from operator `validated` to project `evaluated`.

A project cannot raise mode/sensitivity or loosen `evaluated` to `validated`.
Untrusted project learning settings are ignored. A project `automatic:` mapping
is warning-ignored because automatic budgets are operator-only.

Example project tightening an operator's usable Auto policy:

```yaml
# <trusted-workspace>/.mecatl/settings.yaml
learning:
  mode: review
  sensitivity: conservative
  skills:
    activation: evaluated
```

## Budget profiles

Each profile expands only the six `automatic:` controls; none selects
`sensitivity` or changes provider quotas. Choose sensitivity independently and
combine any sensitivity with any profile.

### Default

```yaml
automatic:
  cooldown: 10m
  window: 1h
  max_reflections: 8
  max_tokens: 100000
  max_reflections_per_principal: 4
  max_tokens_per_principal: 50000
```

### Budget-conscious

```yaml
automatic:
  cooldown: 30m
  window: 1h
  max_reflections: 2
  max_tokens: 25000
  max_reflections_per_principal: 1
  max_tokens_per_principal: 12000
```

### Eager

```yaml
automatic:
  cooldown: 2m
  window: 1h
  max_reflections: 16
  max_tokens: 200000
  max_reflections_per_principal: 8
  max_tokens_per_principal: 100000
```

### Custom

Ask for and validate, one value at a time: cooldown, window, process reflection
count, process token reservation, per-principal reflection count, and
per-principal token reservation. Sensitivity remains the independent Q2 choice;
do not ask for or infer it here. Require `window` in `1m..24h`, a nonnegative
cooldown, and maxima in `0..1000000000`. Warn before accepting zero for a maximum
because it disables automatic reflection.

## Complete examples

These examples make both independent selections explicit. Their sensitivity can
be replaced without changing the automatic profile, and their automatic values
can be replaced without changing sensitivity.

### Safe rollout: review first

```yaml
learning:
  mode: review
  sensitivity: balanced
  skills:
    activation: validated
  automatic:
    cooldown: 10m
    window: 1h
    max_reflections: 8
    max_tokens: 100000
    max_reflections_per_principal: 4
    max_tokens_per_principal: 50000
```

### Usable automatic learning

This makes the now-usable body-only automatic skill path explicit. It can
activate structurally safe evidence-backed ABSTAIN/no-evaluator candidates;
FAIL and ERROR still cannot activate.

```yaml
learning:
  mode: auto
  sensitivity: balanced
  skills:
    activation: validated
  automatic:
    cooldown: 10m
    window: 1h
    max_reflections: 8
    max_tokens: 100000
    max_reflections_per_principal: 4
    max_tokens_per_principal: 50000
```

### Evaluated high assurance

Use only with a trusted evaluator configured. Without one, learned skills remain
staged because PASS cannot be produced.

```yaml
learning:
  mode: auto
  sensitivity: conservative
  skills:
    activation: evaluated
  automatic:
    cooldown: 30m
    window: 1h
    max_reflections: 2
    max_tokens: 25000
    max_reflections_per_principal: 1
    max_tokens_per_principal: 12000
```

## Rollback and tightening blocks

A targeted rollback can change only the named field while preserving the rest
of the subtree:

```yaml
# Stop automatic observation; explicit tools and /reflect remain available.
mode: off
```

```yaml
# Return to stage-for-review behavior.
mode: review
```

```yaml
# Keep Auto facts but require evaluator PASS before learned-skill activation.
skills:
  activation: evaluated
```

Restart the server after any change because settings are build-time.

## Common errors

Validate the complete file with `mecated config validate --file
"<resolved-path>"`. To preflight a proposed learning change without modifying the
settings file, place only the exact non-secret `learning:` block in a temporary
repo-local `.scratch/` patch and run `mecated config validate --file
"<resolved-path>" --learning-patch ".scratch/<bounded-name>.yaml"`; remove the
patch immediately afterward. The patch must contain exactly one top-level
`learning:` mapping.

| Error | Result and correction |
|---|---|
| Editing a later explicit file or conventional user file when an earlier explicit file owns `learning:` | No learning effect. Edit only the first readable, valid explicit file with a non-null block. |
| Assuming the XDG user file participates while `--permissions-conventional=false` | It is not loaded. Select an explicit operator file or change startup configuration. |
| Server startup args/environment are unknown | Ownership is unresolved. Ask for the command/unit/container args; provide manual server/embedder instructions rather than editing automatically. |
| Malformed YAML or duplicate/unknown learning key | Configuration cannot be safely interpreted. Repair minimally before changing policy. |
| `mode: on`, capitalized values, or another vocabulary | Invalid; use the exact lowercase closed values. |
| Bare numeric duration such as `window: 60` | Invalid; use a duration string such as `1h`. |
| `window` below `1m` or above `24h` | Invalid; choose an inclusive `1m..24h` value. |
| Negative cooldown or maximum | Invalid. Cooldown must be nonnegative; maxima must be `0..1000000000`. |
| Any maximum set to zero expecting unlimited | Automatic reflection is disabled by that bound. Use a positive limit. |
| `activation: evaluated` without an evaluator | No PASS is available; ABSTAIN/no-evaluator skill proposals stay staged. |
| Project tries `off`→`auto`, conservative→eager, or evaluated→validated | Tighten-only fold refuses the attempted autonomy increase. Change the operator ceiling instead. |
| Project sets `automatic:` | Warning-ignored; place budgets in operator settings. |
| Budget sized as if shared by replicas | Every replica has its own window/cooldown. Multiply the possible envelope by replica count or lower each process limit. |
| Expecting these limits to cap provider or account spend globally | They only bound this process's automatic reflection reservations. Provider and cluster controls are separate. |

[← back to the skill](../SKILL.md)

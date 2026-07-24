# Acceptance-plan template

Fill this in to produce `docs/acceptance/<plan>.md` — mecatl's design +
verification contract for one capability. Copy the skeleton below and
replace every `<…>` placeholder. Sections tagged **[constant]** always
appear; **[common]** unless the plan is tiny; **[optional]** only when the
situation calls for it. Delete the tags and any section you don't use.

**The load-bearing invariants the bundled check + ac-trace enforce:**

1. **Numbered acceptance criteria** — `AC<scenario>.<n>:` labels. They give
   the orchestrator's per-task briefs, `ac-trace`, and `panel-review` a
   stable id (`AC2.3`) to cite. **Prefer labels.**
2. **≥1 citation per scenario** — a markdown link into `../adr/**`,
   `../architecture.md`, `../design/IMPLEMENTATION-NOTES.md`, or
   `../../AGENTS.md`, or at minimum a bare `ADR-NNNN` reference. Citations
   live inline, never in a footnote.
3. **A `verify:` sub-line on every numbered AC** — Go test names, or one
   non-test method (`none` / `inspection` / `demonstration`) with a reason.
   This is the machine-checkable AC→proof link `ac-trace` reads. Set
   `**Status:** draft` in the header. See the `verify:` contract in
   `docs/acceptance/README.md`.

**Citation paths** (relative to `docs/acceptance/`):

| Target | Form |
|---|---|
| ADR | `[ADR-0036](../adr/0036-engine-module.md)` |
| Architecture + section | `` [`architecture.md § The loop`](../architecture.md) `` |
| Implementation notes | `` [`IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) `` |
| AGENTS.md invariant | `` [`AGENTS.md` — the layering rule](../../AGENTS.md) `` |
| Source file | `` [`engine/agent/loop.go`](../../engine/agent/loop.go) `` (two levels up) |

**The boundary.** Stop at *Definition of done* / *Deferred decisions*. Do
**not** write a per-task worker-assignment table — decomposition is
`/plan-orchestrate`'s job. A coarse *Sequencing recommendation* (prose) or an
*Implementation order (waves)* table naming the seam where orchestrate takes
over is fine.

---

```markdown
# <Plan Name> — acceptance plan

**Phase:** <capability / milestone>
**Status:** draft, <YYYY-MM-DD>. <one-line provenance>
<!-- [optional], one per line: -->
**Issue:** [stacklok/mecatl#NN](https://github.com/stacklok/mecatl/issues/NN).
**ADR:** [ADR-NNNN](../adr/NNNN-slug.md) — <what it pins>.
**Accumulator branch:** `acc/<plan-slug>` (off `main`).

<!-- Lead frame: 1–2 unheaded paragraphs. "The smallest set of work that
proves/lets <goal>." What it derisks; what is deferred. -->
The smallest set of work that <proves | lets> <goal>.

The doc is organized scenario-first because acceptance is about what the
running harness can demonstrate, not which packages exist on disk.

## Why these scope cuts                                        <!-- [common] -->
- [ADR-NNNN](../adr/NNNN-slug.md) — <one-line decision>

## In scope — <N> scenarios, in implementation order           <!-- [constant] -->

Scenarios are listed in implementation order. Each is independently demoable;
later scenarios assume earlier ones but don't change their acceptance
criteria. Within each scenario, ACs progress trivial happy path → richer
happy path → edges → cross-cutting.

### Scenario 1 — <title>

<Narrative: what the running harness / a test client does, which layers and
ports act, which invariants hold — inline-cited to an ADR / architecture
section / AGENTS.md invariant. ≥1 citation in this block.>

**Work:**                                                      <!-- [common] -->
<!-- Present-tense, by layer. -->
- engine domain (`session` / `governance` / `tool` / `prompt`): <aggregate /
  value-object / invariant work>
- engine app (`engine/agent`): <loop / dispatch / orchestration work>
- ports (`engine/port`): <new or widened port interfaces>
- adapters (`engine/adapter/*` / `internal/adapter/*`): <driven adapter work>
- composition (`internal/app` / `cmd/*`): <wiring, flags, posture>

**Acceptance:**                                                <!-- [constant] -->
- AC1.1: <trivial happy path — a present-tense assertion of observable
  behaviour, NOT "implement X". Cite an ADR / invariant.>
  - verify: `TestInvariant_<id>`
- AC1.2: <richer happy path.>
  - verify: `TestADR_NNNN_<Name>`
- AC1.3: <edge / negative case.>
  - verify: inspection — <why review, not a unit test, proves it>
- AC1.4: `Test<Plan>_Scenario1_<Name>` passes.  <!-- scenario/named-test AC -->
  - verify: `Test<Plan>_Scenario1_<Name>`

---

### Scenario 2 — <title>

<Narrative, ≥1 citation.>

**Acceptance:**
- AC2.1: <…>
  - verify: `Test<Identifier>_<Name>`
- AC2.2: <…>
  - verify: none — <reason it is deliberately unverified: reserved / unreachable>

## Out of scope                                                <!-- [constant] -->
| Item | Defer-to | ADR / decision |
|---|---|---|
| <deferred item> | <phase / later> | [ADR-NNNN](../adr/NNNN-slug.md) |

## Cross-cutting deliverables                                  <!-- [optional] -->
<!-- Composition wiring, conformance-suite extensions, docs/AGENTS.md
updates — work not owned by a single scenario. -->

## Sequencing recommendation                                   <!-- [common] -->
<!-- Prose: critical orderings between scenarios. Coarse only. -->

## Named tests landing in this plan                            <!-- [optional] -->
<!-- Identifiers embed the rule: TestADR_NNNN_*, TestInvariant_<id>,
Test<Plan>_Scenario<N>_*. Listed in landing order. -->

## Definition of done                                          <!-- [constant] -->
1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — `llms.txt` regenerated and the matlatl strict link gate green.
3. `task api:check` passes (or `task api:update` was run and the
   `engine/CHANGELOG.md` note is present) if the plan touched the engine's
   exported surface.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`).
5. The named tests (`TestADR_NNNN_*`, `TestInvariant_<id>`) are green and
   grep-locatable by their identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. <plan-specific gates>.

## Deferred decisions and known risks                          <!-- [common] -->
- **<decision/risk>.** <one line; which phase resolves it>.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this
plan is satisfied.
```

---

## Reminders while drafting

- **Use the repo's vocabulary verbatim** (`AGENTS.md`,
  `docs/architecture.md`). Don't invent synonyms for the layers, the ports,
  or the invariants.
- **ACs assert behaviour, not tasks.** "A resumed run re-derives its window
  from the live catalog" — not "implement the window resolver".
- **Every scenario earns ≥1 citation.** No ADR/invariant to point at is a
  smell.
- **Respect the layering rule.** Work items that would import an adapter
  from the domain are a mis-design, not a task.
- **Run the check** before declaring done:
  `bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/<plan>.md`.

[← back to the skill](../SKILL.md)

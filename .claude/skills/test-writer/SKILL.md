---
name: test-writer
description: >-
  Writes tests in mecatl (hexagonal / strict DDD, two Go modules) following
  the invariant-first discipline. Picks the right layer, the right naming
  convention, and the right fake pattern (offline reference adapters —
  mockllm / memfs / memstore — plus the shared conformance suites, never a
  live network call or a mock-framework mock of a port). Use when adding
  tests, writing a new package's test surface, porting a failing scenario
  into a regression test, or pinning an ADR rule / AGENTS.md invariant. NOT
  for authoring the acceptance plan (use /to-acceptance-plan).
---

# test-writer

## Purpose

Every mecatl test answers four questions in order: what behavior, invariant, ADR, or
acceptance scenario does it defend? what layer does it live at? what naming convention does it
follow? what fake or fixture does it need? This skill walks you through those four questions
and emits a test stub.

## Prerequisites

- Read [`AGENTS.md`](../../../AGENTS.md) — the layering rule and the
  invariants under "Things That Will Bite You" (the domain model's
  equivalent: each bullet there is a defended invariant, most with a named
  pinning test already).
- Read [`docs/architecture.md`](../../../docs/architecture.md) — the layers,
  the ports, where each kind of test lives.

## Workflow

### Step 1: Identify the invariant or ADR being defended

Name the test after the rule it defends. The first two patterns are what
`ac-trace` gates against:

- `TestInvariant_<id>` — an invariant from `AGENTS.md` ("Things That Will
  Bite You") or `docs/design/IMPLEMENTATION-NOTES.md`, id kebab → snake.
  Example: `TestInvariant_deny_dominant_scope_resolution`.
- `TestADR_NNNN_*` — a rule codified in `docs/adr/NNNN-*.md`. Example:
  `TestADR_0041_DirectWriteSubagent`.
- `Test<Plan>_Scenario<N>_*` — a scenario test a `docs/acceptance/<plan>.md`
  scenario claims (the orphan gate fails a scenario test no landed plan
  tracks). Example: `TestSessionProfiles_Scenario2_NoFSRestart`.
- Descriptive unit names (`TestFoo_Bar`) for everything else — fine for
  ordinary coverage, but an AC's `verify:` line should name one of the
  pinned forms above when the AC defends a rule.

Rule: every test answers "if this fails, which behavior, invariant, ADR, or scenario is now
wrong?" Descriptive unit behavior is enough for Routine and Bounded work; do not manufacture
an ADR or repository-wide invariant merely to name a test. If the answer is "nothing
observable", consider whether the test earns its place.

### Step 2: Pick the layer

Pick the **lowest** layer that actually exercises the rule, and the right
**module**: `engine/` is its own Go module — a `go test ./...` from the
repo root does NOT cross the boundary; engine tests are a second
invocation from `engine/`.

- **Engine domain unit** — pure aggregate / value-object / invariant tests.
  No I/O, no adapters beyond the reference ones. `engine/session/`,
  `engine/governance/`, `engine/tool/`, `engine/prompt/` `*_test.go`.
  Most invariants pin here.
- **Engine app unit** — the agent loop, dispatch, supervisor against the
  **reference adapters**: `engine/adapter/mockllm` (scripted LLM),
  `engine/adapter/memfs` (in-memory workspace), `engine/adapter/memstore`,
  `engine/adapter/permpolicy`. `engine/agent/*_test.go`. Nothing under
  `engine/` imports `internal/...` — integration tests that need a heavy
  adapter live next to that adapter under `internal/adapter/`.
- **Port conformance** — one suite per port family, run against **every**
  adapter of that port so a fake can never drift: `engine/adapter/memconformance`,
  `engine/adapter/storeconformance`, `engine/adapter/fsconformance`,
  `engine/adapter/sourceconformance`, `engine/adapter/leaseconformance`.
  A new adapter plugs into the EXISTING suite; it does not hand-roll its own.
- **Adapter integration** — a heavy adapter against its real dependency,
  offline: `internal/adapter/<name>/*_test.go` (SSE→Chunk paths are tested
  from fixtures; miniredis for the Redis store).
- **End-to-end** — the offline demo path (`cmd/mecademo`) or a full
  `app.Build` loop test under `internal/app/`. The LIVE provider e2e
  (`task e2e`, real money) is never part of the gate.

**Tests are offline, always.** Never hit a live model or network — the
depguard + CI enforce it. If a test seems to need the network, the seam is
wrong: script the `mockllm` or record a fixture.

### Step 3: Pick the fake or fixture

mecatl is hexagonal: the engine owns the port interfaces (`engine/port`);
adapters implement them.

- **Mock-framework mock of a port:** banned for ports. A mocked
  `LLMProvider` or `SessionStore` passes tests that fail against the real
  adapter. Use the reference adapters instead — they ARE the fakes, and
  the conformance suites keep them honest.
- **`mockllm` for the provider:** script chunks/tool calls per turn; never
  the network.
- **`memfs` for the workspace:** in-memory FileSystem/Workspace. (For a
  no-fs profile test, `engine/adapter/nofs`.)
- **`memstore`/`memlease` for persistence seams**, exercised through the
  conformance suites.
- **Fixtures:** deterministic — no `time.Now()`-sensitive assertions in
  domain tests (the `port.Clock` seam exists; `engine/adapter/wallclock`
  is production-only), no randomised ids where an id matters.
- **Mutation checks where the repo already has them:** the oracle-style
  tests (e.g. `command_runner_secret_scrub_test.go`) plant the violation
  and assert red. Follow that pattern for negative invariants.

### Step 4: Emit the test stub

Do not copy a constructor from this document: test helpers and `agent.Deps`
change as the engine evolves. Locate the nearest current test that exercises the
same layer and seam, then adapt its fixture and constructor shape. Confirm every
field and helper against the current package before writing the failing test.
Prefer an existing `newTest*` helper or reference-adapter fixture over creating a
new harness.

### Step 4.5: Make sure the test can actually fail

A test that passes for the wrong reason is worse than no test — it
manufactures false confidence. Two cases need an explicit "watch it fail"
step before you trust a green result:

- **Negative / absence assertions** ("X is NOT forwarded", "no child
  content enters the parent log", "the env is scrubbed"). These pass
  trivially when they inspect the wrong field or surface. Plant the
  violation (make the thing happen), confirm the test goes **red**, then
  revert. Where you can, derive the forbidden set from the run's own
  output rather than a hard-coded literal that drifts.
- **Timing / lifecycle assertions** ("the run cancels", "no goroutine
  leak"). A check with an already-expired context and no assertion can
  only fail by panic. Use a real bound plus a would-block guard, and
  assert the result. `goleak` is available in the engine module for leak
  assertions.

Every test carries at least one assertion that can fail on a real
regression. `_ = err` is not verification.

### Step 5: Verify with the Taskfile

```bash
task test          # both modules + the engine-standalone hygiene proof
cd engine && go test ./agent/ -run TestYourNewTest   # a single engine test
```

Then check whether the implementation contradicts its declared work classification or
introduces an unplanned durable decision. Stop as contract drift rather than silently
upgrading/downgrading it. Only Architectural work with a genuinely new or superseding durable
decision adds an ADR and its `TestADR_NNNN_*` pin; a current invariant may instead belong in
AGENTS.md / IMPLEMENTATION-NOTES.md with `TestInvariant_<id>`. Routine and Bounded rationale
stays in the issue, PR, plan, or ordinary test name. If you touched the engine's exported API:
`task api:update` plus the `engine/CHANGELOG.md` note.

## Anti-patterns

- "I'll mock the `LLMProvider` just for this test." Forbidden —
  `mockllm` scripts the stream; the conformance suites pin adapter parity.
- "This behaviour is proven — my fake asserts it." Only if the same
  conformance suite runs against the real adapter. Fake-only is not proven.
- "I'll hit the real provider to check." Never — offline only; the SSE
  adapters are fixture-tested, and `task e2e` is a separate, manual gate.
- "I'll assert that a guide contains these phrases." Do not pin arbitrary prose or keyword lists. Test links/anchors, parsed executable examples, schemas, and generated-output freshness; leave prose semantics and completeness to human review. Model-visible prompt affordance tests remain required because runtime behavior depends on them.
- "I changed a test because the implementation changed." When tests fail,
  fix the implementation, not the tests.
- "My negative test passes." Did you watch it fail when the violation is
  planted? A green absence-assertion you never saw red is hollow — it
  often checks the wrong field while the value rides another.
- "I'll run `go test ./...` from the repo root to check the engine." The
  module boundary swallows it — engine tests run from `engine/`.

## See also

- `AGENTS.md` — the layering rule, the invariants, the Taskfile contract.
- `docs/architecture.md` — the layers and the dependency rule.
- `docs/adr/` — the frozen decisions your tests may pin.
- `docs/acceptance/README.md` — the `verify:` contract ac-trace gates.

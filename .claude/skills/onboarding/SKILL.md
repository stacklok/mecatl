---
name: onboarding
description: >-
  mecatl's workflow map — the router a contributor (human or agent) hits to
  learn the project's way of turning an idea into a trusted, agent-driven
  implementation. Headlines the development spine (design → orchestrate →
  verify) and names the specialist skill that owns each step, plus the
  lightweight issue-scale track. A router, not an executor — it points at
  the right skill and stops. Use when someone is new to the repo, says
  "onboard me", asks "how do I do X here", "what's the workflow for Y",
  "let's plan X", "how do we build Y", or otherwise asks for the project's
  way of doing things.
---

# onboarding

The **map** for working in mecatl. This skill is a router: it tells you
which step of the development **spine** you are at and which specialist
skill does the work. It does not do the work itself.

The full process — the trust loop, the per-skill invocation policy, the
rationale — lives in
`docs/development-process.md`.
This skill is its runtime front door; keep it a thin pointer, not a copy.

## Before any workflow — read these first

A contributor (human or agent) operating in mecatl without these in head
will fight every workflow below. At minimum:

- `AGENTS.md` — the canonical contract: the Taskfile
  commands, the layering rule, and the invariants under "Things That Will
  Bite You". Anything that violates one gets pushed back. (`CLAUDE.md` is a
  symlink to it.)
- `docs/architecture.md` — how the harness
  works: layers, the loop, ports, the API surface.
- `docs/adr/` — the frozen ADRs touching your area. ADRs
  override informal conventions and are never edited in place.
- `docs/design/IMPLEMENTATION-NOTES.md`
  — the dense per-subsystem reference, when your work touches an existing
  subsystem.
- `docs/usage.md` — how to run it (flags, APIs).
- `docs/development-process.md` — the
  spine itself.

Tell the user which are already in context and offer to load the rest
before starting the workflow they asked about.

## The spine — idea → trusted implementation

Three steps, in order. Each ends at a checkpoint; you type the next command.
This is the default path for any new **capability** — a feature big enough
to want a design contract and a verification gate.

1. **`/to-acceptance-plan`** — synthesise a settled design into
   `docs/acceptance/<plan>.md`: scenario-first, numbered acceptance
   criteria = the verification contract, each with a `verify:` line, every
   scenario cited to an ADR / architecture / invariant. Runs the advisory
   `devils-advocate` pass + a light specialist spot-check.
   → checkpoint: **draft plan handed off.**
2. **`/plan-orchestrate <plan>`** (you trigger it) — decompose the plan
   into tasks and run parallel `tdd-worker` agents onto one accumulator
   branch; the aggregate gate runs on the assembled branch, the plan flips
   to `landed`, `ac-trace --strict` verifies every `verify:` proof
   resolves, `/panel-review` runs inline, and it pushes the accumulator and
   **opens one PR** (plan + code). It spawns a worker swarm, so it never
   auto-fires — you type it.
3. **Human merge** — the single human gate. Review and merge the PR.
   Gaps found at review → back to step 2 as new/updated tasks.

**Skip rules:** for issue-scale work (a bug, a focused feature with an
issue as the spec), skip the spine and use **`/dev-pipeline`** — the
lightweight approach → implement → panel-review → iterate → commit loop.
Never skip the human gate — merging the PR is what makes the agent-driven
build trustworthy.

## Verification, tracked

Acceptance criteria are the contract *and* the tracker. Each carries a
`verify:` line; [`ac-trace`](https://github.com/stacklok/ac-trace)
(`task ac-trace`, `task ac-trace-strict`) checks every named proof resolves.
See `docs/acceptance/README.md`.

## Specialists — named here, invoked when the work calls for them

- **`/dev-pipeline`** — the issue-scale track: architect plans → implementer
  builds → review panel → iterate → commit per iteration.
- **`/panel-review`** — three-axis review (Spec / Standards / Domain with
  the default-on reuse pair) of any diff. Standalone or as the spine's
  final gate.
- **`/test-writer`** — write tests under the invariant-first discipline
  (orchestrate workers call this too).
- **`/cut-release`** — cut a tagged release: bump the reusable-workflow
  version pins, tag `vX.Y.Z`, the Release workflow builds + signs +
  publishes.
- **`/perf-optimization`** — profile-driven allocation/latency work over
  the offline scenario harness (`task bench`, `task perf:scenarios`,
  benchstat A/B).
- **`/perf-mcp-interpretation`** — diagnose a running harness's latency /
  leaks via the perf MCP server.
- **`/mecatl-model-router-config`** — design the model-routing
  configuration (aliases, slots, router taxonomy).

## How to extend this playbook

When a new "how do I do X here" recipe earns its keep — you've explained the
same workflow three times — add it under the right section in the spine's
shape (the command + one line + where it hands off). Keep each entry a
router: name the specialist skill that does the work and stop. Do not
duplicate that skill's logic here; it goes stale.

## What this skill does NOT do

It points; the specialist skills act. Send the user to:

- Write the design contract → `/to-acceptance-plan`
- Decompose + build → `/plan-orchestrate`
- Issue-scale work → `/dev-pipeline`
- Verify a build or any diff → `/panel-review`
- Write tests → `/test-writer`
- Ship a release → `/cut-release`
- Perf work → `/perf-optimization`, `/perf-mcp-interpretation`

If the user is asking for one of those directly, name the skill and stop.

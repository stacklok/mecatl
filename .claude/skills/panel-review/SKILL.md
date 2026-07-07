---
name: panel-review
description: >-
  Multi-agent review panel — three orthogonal axes: Spec (matches the
  issue/PRD?), Standards (matches repo conventions?), Domain (what
  installed specialist reviewer agents say — security, K8s, DevOps,
  duplication, library-reuse, project architects). Fans all three out
  in parallel; three-tier report.

  Use PROACTIVELY after the user finishes implementing, modifying,
  refactoring, fixing, porting, or shipping code — before they ask.
  Implementation should be followed by review.

  Auto-trigger on: "review", "code review", "panel review", "audit",
  "check this", "scrutinise", "look at my changes", "review the diff",
  "review since X", "review against the spec", "did this implement the
  issue", "check against the PRD", "done", "finished", "implemented",
  "wrote", "added", "refactored", "fixed", "shipped", "ready for
  review", "PR ready", "/panel-review".

  NOT for: trivial edits (use /code-review), cloud review (use
  /code-review ultra), drafting code, posting GitHub comments (use
  /pr-review-post), pure-config diffs.
---

# Panel review

A workflow skill. The user has just changed code (or explicitly asked
for a review), and the change needs to be reviewed across three
**orthogonal axes** — spec adherence, project standards, and specialist
domain expertise — with each axis able to disagree with the others
without being silenced.

**Proactive trigger discipline.** This skill is meant to auto-fire
after implementation, not on every chat message. Fire when there's a
plausible code-change checkpoint — a finished feature, a fixed bug, a
landed refactor, an "I think that's done" — but not on conversation,
questions, planning, or tiny one-line edits the user clearly didn't
want reviewed. When in doubt, run a quick `git diff --stat` and ask:
"Want me to run a panel review on this?" rather than firing
unilaterally on an ambiguous message.

Each axis catches a different failure mode:

- **Spec** catches *"implements the wrong thing well"*. Standards and
  Domain both pass; the diff just doesn't do what the issue asked.
- **Standards** catches *"implements the right thing the wrong way for
  this repo"*. Spec passes; the conventions are broken.
- **Domain** catches *"implements the right thing with a hidden
  landmine"*. Spec and Standards pass; a security, K8s, library, or
  duplication issue lurks.

Reporting them separately is deliberate. Synthesised together, one
axis masks another. Inspired by Matt Pocock's two-axis `/review`
skill (Standards + Spec), generalised with a Domain axis powered by
the project's installed specialist reviewer agents.

## Prerequisites

- Working directory is a git repository (or files were explicitly
  named).
- At least one of: a spec source (issue / PRD), project standards
  docs (CLAUDE.md, etc.), or one or more reviewer agents installed
  at `.claude/agents/` or `~/.claude/agents/`. One axis is enough
  to run.
- `gh` CLI installed if reviewing a PR by number or fetching issue
  bodies.

## Step 1 — Pin the fixed point

The fixed point is what we're diffing **against**. Whatever the user
said is the fixed point — a commit SHA, branch name, tag,
`origin/main`, `HEAD~5`, a PR number, an explicit file list. Don't
be opinionated; pass it through.

If the user didn't specify, **ask** before proceeding:

> Review against what — a branch (`main`?), a commit, "since I
> started this branch", or a PR number?

Do not silently auto-detect. The whole review hangs on this and a
wrong guess wastes the panel's effort.

Once pinned, capture once and reuse:

- **Diff command:** `git diff <fp>...HEAD` (three-dot — compares
  against the merge-base, which is what "what did this branch
  change?" means).
- **Commit list:** `git log <fp>..HEAD --oneline` (used to find
  issue/spec references).
- **File list:** `git diff <fp>...HEAD --name-only`.

Print to the user:

> Reviewing against **<fp>**: N files, M insertions, L deletions,
> K commits.
> <file list, capped at 20 with "+N more">

If the diff is **empty**, ask for explicit file paths or a PR
number.

If the diff is **> 50 files or > 3000 lines**, ask the user whether
to split into smaller reviews or proceed (large panels generate
noisier output).

## Step 2 — Read project context

In one parallel batch, read what later steps need:

- `CLAUDE.md` (and any parent CLAUDE.md up to the git root)
- `.claude/rules/*.md` if present
- `docs/design/principles.md`, `docs/design/architecture.md` if
  present
- The first ~5 ADR filenames under `docs/adr/` (titles only)
- `SECURITY.md` if present
- `CONTRIBUTING.md` if present
- `CONTEXT.md` / `CONTEXT-MAP.md` if present (Matt-Pocock-style
  domain dictionaries — also useful as standards sources)

## Step 3 — Detect the spec source

The Spec axis needs to know what was *asked for*. Look in this
order:

1. **Issue references in commit messages.** Parse
   `git log <fp>..HEAD --format=%B` for `#123`, `Closes #45`,
   `Fixes GH-67`, `gitlab !89`, `JIRA-100`, `LIN-1234`. Fetch the
   issue body via `gh issue view <N>` (or the project's
   `docs/agents/issue-tracker.md` workflow if present).
2. **A path the user passed as an argument** — "review against
   spec at docs/specs/foo.md".
3. **PRD / spec files** under conventional locations matching the
   branch name or feature: `docs/specs/<name>.md`,
   `docs/prd/<name>.md`, `specs/<name>.md`, `.scratch/<name>.md`,
   `docs/acceptance/<name>.md`.
4. **If nothing is found**, ask the user:

   > I don't see a spec or issue reference for this branch. Path
   > to a spec, or skip the Spec axis?

If the Spec axis is skipped, note it in the final output as "Spec
axis: no source available — skipped" with the reason. **Do not
fabricate a spec.**

## Step 4 — Inventory standards sources

The Standards axis needs to know what *conventions* this repo
documents. Collect filenames (the Standards subagent will read
them):

- `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`
- `CONTEXT.md`, `CONTEXT-MAP.md`, per-directory `CONTEXT.md` files
- `docs/adr/*.md` (architectural decisions ARE standards)
- `STYLE.md`, `STANDARDS.md`, `STYLEGUIDE.md` at repo root or under
  `docs/`
- `.claude/rules/*.md`
- `docs/design/principles.md` if present

**Explicit skip rule** (inherited from Matt's design): do **not**
have the Standards subagent re-check anything *tooling* already
enforces. Note the presence of:

- `.editorconfig`
- `eslint.config.*` / `.eslintrc.*`
- `biome.json` / `biome.jsonc`
- `prettier.config.*` / `.prettierrc.*`
- `tsconfig.json`
- `.golangci.yml` / `.golangci.yaml`
- `pyproject.toml` `[tool.ruff]` / `[tool.black]` / `[tool.mypy]`
- `rustfmt.toml`, `clippy.toml`

Tell the subagent these run on every commit and to skip anything
they cover. Re-flagging tool-enforced rules wastes tokens.

If no standards docs are found, note "Standards axis: no project
standards docs found — axis returned an empty report" in output;
don't skip the axis silently.

## Step 5 — Inventory the available domain panel

List `.claude/agents/*.md` and `~/.claude/agents/*.md`. Parse each
file's `description` field (frontmatter).

Build a table:

| Agent | Scope (first line of description) | Defer-to |
|---|---|---|

This is the **domain panel** of available specialist reviewers.
Don't fabricate agents that aren't installed — if a dimension has
no matching agent, note the gap rather than skip the dimension
silently.

## Step 6 — Classify the diff (Domain axis)

For each file in the diff, identify the domain dimensions that
apply. The mapping below is a default; **project-specific agents
take precedence over generic ones** when their scope matches.

| Dimension | File / content signals | Default agent |
|---|---|---|
| **Security (cross-cutting)** | HTTP handlers, route registration, `Authorization`, `crypto.`, `tls`, `fetch(` / `http.Get` with user input (SSRF), deserialisation, SQL/template assembly, JWT, OAuth, password handling | `secure-code-reviewer` |
| **Architecture (cross-language)** | New module / package; new public API; cross-module imports; renamed exported type; new interface; new top-level directory; >3 files touched across distinct modules; new layering boundary; or any non-trivial `*.go` / `*.ts` / `*.tsx` / `*.py` / `*.rs` / `*.java` change with structural shape | `software-architect` |
| **K8s in-cluster** | `*.yaml` / `*.yml` with `kind:`, `Chart.yaml`, `values.yaml`, `kustomization.yaml`, `templates/*.yaml` | `kubernetes-deployment-expert` |
| **K8s controller / CRD** | `api/v*alpha*` / `api/v*beta*` / `api/v1`, `controllers/`, `internal/controller/`, imports of `sigs.k8s.io/controller-runtime`, `controller-gen` markers | `kubernetes-operator-expert` |
| **DevOps / CI / IaC** | `.github/workflows/*`, `.gitlab-ci.yml`, `*.tf`, `*.tfvars`, `Dockerfile`, `*.dockerfile`, `cloudbuild.yaml`, `Jenkinsfile`, `Pulumi.yaml`, `cdk.json`, `Taskfile.yml`, `Makefile` | `devops-expert` |
| **Duplication** | Three+ touched files with similar shape; new files that look like copies of existing | `code-duplication-reviewer` |
| **Library reuse / dep audit** | `go.mod`, `package.json`, `requirements.txt`, `pyproject.toml`, new top-level dependency, hand-rolled utility shapes | `library-reuse-reviewer` |
| **Reinvention / over-build** | Any non-trivial code diff (default-on, NOT signal-gated — see below) | `library-reuse-reviewer` + `code-duplication-reviewer` |
| **Project-specific surfaces** | (varies — read each available agent's `description` frontmatter to learn its scope) | Any project-level agent in `.claude/agents/` that names a domain not covered above — typically architects for a specific framework, protocol, API surface, or UI workspace |

Classification rules:

- **Always include `secure-code-reviewer`** for diffs touching
  application code with external trust boundaries.
- **`software-architect`** is the default architecture reviewer for
  any non-trivial code diff — cross-language design plus the
  language-architecture role when no language-specific architect
  is installed.
- **Project-specific architects** in the repo's `.claude/agents/`
  compose with `software-architect` rather than replacing it. Both
  can run on the same diff: the project-specific one carries
  domain-loaded invariants and ADR knowledge, `software-architect`
  carries the cross-cutting design lens.
- **`code-duplication-reviewer`** and **`library-reuse-reviewer`
  are DEFAULT-ON for any non-trivial code diff** (any diff touching
  application logic — not pure-docs / pure-config). Do NOT gate
  them on "a new dependency was added" or "actual duplication
  shape is already visible". Their entire job is to FIND the
  duplication and the reinvented stdlib that ISN'T obvious from
  the diff surface; gating them on the signal already being
  visible skips them on exactly the diffs where they add the most
  value. When in doubt, include both — they default to silence
  when they find nothing. The reuse pair is cheap insurance
  against the most common over-build failure mode an agent
  introduces (reinvented stdlib, speculative abstraction,
  unrequested layer). See the reuse-ladder brief in Step 8.
- **Pure-docs diffs** skip the Domain axis (Spec axis may still
  run).
- **Pure-test diffs**: a single Domain reviewer
  (project's test-review agent if present, else language
  architect).

## Step 7 — Announce the three-axis plan

```
Reviewing against <fp>: N files, M insertions, L deletions, K commits.

Spec axis:       checking against #123 ("Add /preview endpoint")
Standards axis:  reading CLAUDE.md, .claude/rules/, docs/adr/
                 skipping tooling: golangci-lint, biome, prettier
Domain axis (running in parallel):
  - secure-code-reviewer        — auth/HTTP/SSRF surface in api/handlers/
  - kubernetes-deployment-expert — deploy/staging/ manifests
  - devops-expert               — .github/workflows/release.yml changes
  - library-reuse-reviewer      — default-on: reinvented stdlib / over-build
  - code-duplication-reviewer   — default-on: duplication across the diff

Skipping (no diff in scope):
  - kubernetes-operator-expert

Gaps (dimension detected, no matching agent installed):
  - (none)
```

Wait for user pushback only if the panel is large (5+ agents
across the Domain axis) or they asked for a dry-run.

## Step 8 — Fan out (PARALLEL)

In a **single assistant turn**, issue all of:

- 1 × `Agent` call for the **Spec** axis (general-purpose subagent
  with brief).
- 1 × `Agent` call for the **Standards** axis (general-purpose
  subagent with brief).
- N × `Agent` calls for the **Domain** panel (named specialist
  agents).

They run concurrently, separate contexts, no order dependencies.

### Spec subagent

- `subagent_type`: `general-purpose`
- `description`: "Spec adherence check for <scope>"
- `prompt`:

  > Diff to review: `git diff <fp>...HEAD`
  > Commit list: <list>
  > Spec source: <path or inline contents of the issue/PRD>
  >
  > Read the spec carefully, then read the diff. Report:
  >
  > 1. **Missing** — requirements the spec asked for that the diff
  >    doesn't implement, or only partially implements.
  > 2. **Scope creep** — behaviour added by the diff that the spec
  >    didn't ask for.
  > 3. **Wrong** — requirements that look implemented but where
  >    the implementation appears incorrect against the spec.
  >
  > Quote the specific spec line / requirement for each finding.
  > Under 400 words. Default to silence when uncertain — only flag
  > concrete mismatches.
  >
  > Root-cause discipline: when a finding names a symptom, note
  > whether the diff fixes the root cause or only the path the
  > ticket names — a sibling caller may still be broken.

If no spec source was found in Step 3, **skip this call** and note
in final output.

### Standards subagent

- `subagent_type`: `general-purpose`
- `description`: "Standards conformance check for <scope>"
- `prompt`:

  > Diff to review: `git diff <fp>...HEAD`
  > Standards source files (read these): <list from Step 4>
  > Tooling-enforced configs to SKIP (tooling already runs on
  > every commit; do not re-flag what they cover): <list>
  >
  > Read the standards docs, then the diff. Report — per file /
  > hunk where relevant — every place the diff violates a
  > documented standard. Cite the standard (file + the rule).
  > Distinguish hard violations from judgement calls. Under 400
  > words. Default to silence when uncertain.

### Domain agents

For each specialist in the panel:

- `subagent_type`: the agent's `name` field.
- `description`: "<agent-name> review of <scope>".
- `prompt`:
  - The exact diff scope (`git diff <fp>...HEAD -- <paths>`).
  - A one-line statement of what the agent should focus on,
    derived from its own description.
  - The project-context items the agent's body needs.
  - Reminder: "Respect your calibration discipline — flag only
    findings that affect correctness, security, or stated
    requirements. Default action when uncertain is silence."

**Reuse pair — extra brief (library-reuse-reviewer +
code-duplication-reviewer).** When fanning out the default-on reuse
pair, append the reuse-ladder brief from
[references/reuse-ladder.md](references/reuse-ladder.md) to each of
their prompts so they review systematically rather than ad-hoc. The
brief gives them the 7-rung reuse ladder (Does this need to exist?
→ Already in codebase? → Stdlib? → Native platform? → Installed
dep? → One line? → Minimum), the `delete:`/`stdlib:`/`native:`/
`yagni:`/`shrink:` tag vocabulary, the `net: -<N> lines possible.`
score, and root-cause discipline (grep every caller, fix the shared
function once). Read the reference file and paste the brief block
into each reuse agent's prompt.

Set `run_in_background: false` (default) so synthesis blocks on
completion.

If the environment or the user has expressed a model preference,
honour it on each Agent call.

## Step 9 — Three-tier output

Do **NOT** merge findings across axes. They are deliberately
orthogonal — one passing while another fails is exactly the
information you want to preserve. Cross-axis merging is the
failure mode Matt's design exists to prevent.

For the **Spec** and **Standards** axes, present the subagent's
findings verbatim or lightly cleaned. Don't rerank.

For the **Domain** axis only, synthesise across the panel:

### Synthesis (Domain axis only)

1. **Combined table** — every finding from every domain agent,
   preserving its source's severity label.
2. **Dedup** — two findings are duplicates when they share location
   AND underlying issue. Merge with `Sources: [A, B]` and tag as
   **cross-confirmed** (highest-confidence — multiple specialists
   independently reached the same conclusion).
3. **Prioritise** — Critical → High → Medium → Low → Info;
   within severity, cross-confirmed before single-source; within
   that, by file path.
4. **Tag for action class:**
   - **Ship-blockers** — Critical/High affecting correctness,
     security, compliance. Must address before merge.
   - **Mechanical fixes** — clear path, no judgement. Candidate
     for /simplify.
   - **Judgement calls** — architectural trade-offs. Discuss.
   - **Polish** — Low/Info; optional.

### Final output structure

Render the report following the template in
[references/output-template.md](references/output-template.md).
The prose rules above (don't merge across axes; synthesis within
Domain only) govern; the template is the shape.

## Step 10 — Offer follow-up

Ask one short question:

- "Apply mechanical fixes now?" → suggest /simplify
- "Drill into a specific finding?" → user names one
- "Post these as inline PR comments?" → suggest /pr-review-post
  if installed
- "Address the Spec misalignment first?" → user redirects work
- "Ship as-is" → user accepts the report

Don't loop on synthesis. The panel ran once; the report stands.

## What this skill does NOT do

- Doesn't modify code. Synthesis is a report.
- **Doesn't merge findings across axes** — Spec, Standards, and
  Domain are orthogonal by design; one masking another is the
  failure mode this skill exists to prevent.
- Doesn't fan out to every available Domain agent regardless of
  diff — irrelevant reviewers waste tokens and add noise. (The
  reuse pair is the exception: default-on for non-trivial code
  diffs, because over-engineering is the most common agent
  failure mode and the signal is rarely visible on the diff
  surface.)
- Doesn't duplicate the built-in `/code-review` skill's
  single-pass logic.
- Doesn't override the user's choice of agents.
- Doesn't auto-detect the fixed point — always pin it explicitly
  (asking if needed).
- Doesn't fabricate a spec if none exists. Skips the Spec axis
  with a noted reason instead.
- Doesn't re-check rules that project tooling (linters, formatters,
  type-checkers) already enforces.
- Doesn't fire on every chat message just because trigger keywords
  appear. Plausible code-change checkpoint required: a finished
  feature, a fixed bug, a landed refactor. Pure-question messages,
  planning conversations, and trivial one-line edits don't trigger.
  When ambiguous, ask the user before fanning out the panel.

## Error handling

| Symptom | Action |
|---|---|
| No fixed point provided | Ask before proceeding; don't auto-detect. |
| No spec source found | Skip Spec axis; note in output. |
| No standards docs found | Standards subagent returns empty; note in output. |
| No domain agents installed | Run Spec + Standards axes only; note in output. |
| `git diff` returns nothing | Ask for explicit scope (files / PR / branch). |
| `gh pr diff N` / `gh issue view N` fails | Tell the user `gh` isn't authenticated or the resource doesn't exist. |
| An agent times out / errors | Note the gap in synthesis; don't fail the whole panel. |
| Two domain agents conflict | Surface both views as a judgement-call entry. |
| Spec and Domain disagree | Present both; neither is wrong — they're asking different questions. |
| Diff exceeds practical size (>50 files / >3000 lines) | Ask the user whether to split or proceed. |

## Design notes

The three-axis structure builds on Matt Pocock's two-axis
`/review` skill (Standards + Spec, parallel subagents,
side-by-side reporting, no cross-axis merging). The Domain axis
generalises the idea by replacing one of the general-purpose
subagents with a panel of named specialist reviewer agents
selected from the project's installed `.claude/agents/`. The
**don't merge across axes** principle is preserved verbatim —
synthesis happens within the Domain axis only.

The reuse pair (`library-reuse-reviewer` +
`code-duplication-reviewer`) is DEFAULT-ON for non-trivial code
diffs: over-engineering review runs on every diff and *finds* the
reinvention rather than gating on an already-visible signal. The
`net: -<N> lines possible.` score gives the reuse axis a concrete
countable metric instead of generic prose.

## See also

- [references/reuse-ladder.md](references/reuse-ladder.md) — the
  7-rung reuse-ladder brief and `delete:`/`stdlib:`/`native:`/
  `yagni:`/`shrink:` tag vocabulary, appended to the reuse-pair
  agent prompts in Step 8.
- [references/output-template.md](references/output-template.md) —
  the rendered shape of the Step 9 three-tier report.
- Matt Pocock's two-axis `/review` skill —
  https://github.com/mattpocock/skills/blob/main/skills/in-progress/review/SKILL.md
- Claude Code best practices, "Add an adversarial review step" —
  https://code.claude.com/docs/en/best-practices
- Sub-agents reference, "Run parallel research" pattern —
  https://code.claude.com/docs/en/sub-agents
- The bundled `/code-review` skill — single-pass review without
  fan-out
- The bundled `/code-review ultra` — cloud-side multi-agent
  review (Anthropic's hosted panel)

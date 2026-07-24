# Documentation conventions

This folder no longer holds the per-feature design records — those are now numbered
**ADRs** in [`docs/adr/`](../adr/README.md) (consolidated by
[ADR 0003](../adr/0003-consolidate-design-records-as-adrs.md)). What lives here:

- [PRODUCTION-READINESS.md](./PRODUCTION-READINESS.md) — the **single status tracker**
  (what is shipped / in-progress / deferred).
- [IMPLEMENTATION-NOTES.md](./IMPLEMENTATION-NOTES.md) — the dense, living per-subsystem
  implementation reference.
- [principles.md](./principles.md) — the **platform principles** acceptance plans cite
  as `Principle N` (the ac-trace grounding list).
- this file — the **documentation & citation conventions** the `docs/lint` gate enforces.

One source of truth per fact: current behaviour in
[`docs/architecture.md`](../architecture.md), mutable status in
[PRODUCTION-READINESS.md](./PRODUCTION-READINESS.md), the *why* in the
[ADRs](../adr/README.md). New decisions are new ADRs (copy
[`template.md`](../adr/template.md)); supersede with a new ADR, never an in-place rewrite.

## The citation convention

These docs cite code a lot. A citation is a load-bearing claim that a file (and
sometimes a symbol) exists where the doc says it does. The engine carve (core
moved from `internal/` to `engine/`) showed how that drifts silently: a doc kept
citing `internal/agent/loop.go` long after the file became `engine/agent/loop.go`, <!-- lint:not-a-citation: illustrative stale path in narrative -->
and nothing complained. `docs/lint` (`CheckCitations`) turns a dead citation into
a CI failure so the docs stay honest. This file is the convention the test
enforces.

## How to cite

- Cite a file as a repo-root-relative path inside backticks: `` `engine/agent/loop.go` ``.
  The path must contain at least one slash and must be a path that resolves from
  the repo root. (In practice that means it starts at a real top-level directory
  such as `engine/`, `internal/`, `contracts/`, `cmd/`, or `docs/`, but the test
  checks resolution, not the directory name: an abbreviated path like
  `grpcdriver/server.go` does NOT resolve from the root, so it is flagged on
  purpose, not exempted.) The slash plus the resolution is what makes it a locator
  the test can verify.
- To cite a symbol, use the explicit pairing `` `pkg/file.go` (`SymbolName`) ``:
  a verifiable file span, a single space, then the symbol in its own backticks
  wrapped in parentheses. The test verifies the symbol is present in the file
  (word-boundary match, so `Save` does not match `SaveRequest`). Nothing else is
  read as a symbol claim; arbitrary prose like "the recordPrompt helper" is never
  parsed.
- Line numbers are non-load-bearing. Write `` `engine/agent/loop.go:677` `` if it
  helps a reader, but the test strips the `:NN` (and `:NN-MM`, `:NN,MM`) suffix
  and never verifies it. Lines drift constantly; the file path and the symbol are
  the durable anchors.

## What the test does and does not check

- It verifies file existence for slash-bearing repo-root-relative paths, and
  symbol presence for the `(Symbol)` form. That is all.
- A bare basename is a back-reference, not a citation, and is left alone: spans
  like `` `Save` ``, `` `service.go:868` ``, `` `task test` ``, or `` `omitempty` ``
  have no slash, so the test ignores them. Use the short form freely in prose once
  the full path has been cited nearby; it reads better and the test will not chase
  it.
- A known-extension path is one ending in `.go`, `.proto`, `.yaml`/`.yml`, or
  `.md`. Other extensions (`.sh`, `.json`, ...) are not treated as code citations.
- When a citation goes missing, the failure names the doc and the dead span, and
  for a moved file it suggests the new location if exactly one file with the same
  basename exists elsewhere (the engine-carve catch).
- The test verifies that the citations a doc DOES make resolve; it does NOT
  verify completeness. A resource that exists in code but has no inventory row in
  `CLOUD-NATIVE.md` (or any other claim a doc simply fails to make) is invisible
  to this test. Catching a missing row stays the job of the per-phase re-audit
  (`CLOUD-NATIVE.md`, Phase plan), not of `CheckCitations`. Don't let the green
  docs test lull the re-audit into lapsing.

## Illustrative paths that are not citations

Sometimes a doc quotes a slash-path that is not a repo file: a skill logical
asset name (`references/api.md`), an example value, a path in another project.
Those should not read as code citations, but the test cannot guess intent. Mark
the line with an inline HTML comment so the exemption is explicit and local. The
canonical form carries a REQUIRED reason:

```
... in the skill namespace (`references/api.md`). <!-- lint:not-a-citation: skill asset name, not a repo file -->
```

Always write the reason: an unexplained exemption is exactly the thing that rots,
because the next reader cannot tell whether it is deliberate or a forgotten dodge.
The bare form (`<!-- lint:not-a-citation -->`, no reason) still technically
matches, so a legacy marker keeps working, but do not write new ones. The token
match is anchored, so a near-miss typo (for example a pluralized
`lint:not-a-citations`) does NOT suppress the check: the citation is still flagged
(fail-safe), and you will see the red test rather than a silently-widened hatch.

The marker exempts every citation on its line. Reach for it only when a span is
genuinely not a repo file. An abbreviated-but-real path (for example
`grpcdriver/server.go` for `internal/adapter/grpcdriver/server.go`) is NOT an
illustrative path: expand it to the full repo-root-relative form rather than
hiding it behind the marker. The test flags the abbreviation on purpose, and the
basename suggestion points the way.

## Scope and widening it

The live guard runs over `docs/design/*.md` **and** the architecture guide
(`docs/architecture.md` + `docs/architecture/*.md`, the living citation-heavy
reference), globbed at run time so a new file in either is covered automatically.
`CLAUDE.md`, the top-level `README`, and the rest of `docs/*.md` are out of scope for
now. To widen it, add a glob to the `patterns` slice in `TestRealDesignDocsCitations`
(`docs/lint/citations_test.go`); the checker itself is path-agnostic.

## The records

The per-feature design records are now ADRs — see the **[ADR index](../adr/README.md)**.

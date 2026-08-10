# Handover: `llms.txt` is stale on `main` (CI "Docs (matlatl)" job failing)

## Symptom

The `Docs (matlatl)` CI check fails on `main`'s own tip (`e1969019`, "Merge pull
request #418 from stacklok/invalid-utf8") with:

```
::error file=llms.txt::llms.txt is out of date — run 'task docs:llms' and commit the result.
```

This is **not** specific to any one PR — it was independently observed failing
identically on PR #408 (`396-sse-missing-on-run`, unrelated router work) after
rebasing onto the current `main` tip, and confirmed to also fail on `main`
itself via `gh api repos/stacklok/mecatl/commits/e1969019/check-runs`.

A CI-side regeneration diff (captured via the failing job's own comparison)
showed the committed `llms.txt` undercounts the live corpus: `169→170`
document(s), `1830→1842` heading(s), `1509→1521` resolved reference(s).

## Root cause

`AGENTS.md` states the rule plainly:

> **Changed any Markdown? Run `task generate` (or `task docs`) before
> committing — always.** `llms.txt` is generated and goes stale the instant
> docs change; never hand-edit it.

PR #418 ("invalid-utf8", merged as `e1969019`) edited
`docs/design/IMPLEMENTATION-NOTES.md` across multiple commits
(`de4b87b8`, `097d37a0`, `4f86a2ff`, plus the merge commit itself — +102
lines net across the merge) and touched `AGENTS.md` (+1 line), but never ran
`task docs:llms` / `task generate` before committing. The immediately
preceding commit (`cbf200c6`, ADR 0100) DID regenerate `llms.txt` correctly in
the same commit as its doc changes — so the drift was introduced specifically
by #418's commits landing without the regen step.

Since `IMPLEMENTATION-NOTES.md` is a single large living doc (per
`docs/design/README.md` citation rules), a heading-count-only diff
(1830→1842, +12) is consistent with those edits alone. The document-count
delta (169→170) wasn't isolated during this diagnosis — worth double-checking
after regeneration whether it's a real new doc or a matlatl link-graph
reachability change (e.g. a doc that became newly reachable through a link
added in one of those commits).

## Why this session couldn't just fix it

`task docs:llms` runs:

```
go run github.com/stacklok/matlatl/cmd/matlatl@v0.0.7 index . --llms --title "mecatl" > llms.txt
```

This requires resolving the `matlatl` Go module via `sum.golang.org` / a git
fetch. In the sandboxed dev environment this was diagnosed from, that fetch
failed outright (404 from the checksum DB, then a git fallback that needs
interactive credentials) — there is no network egress for arbitrary git/HTTP
fetches. CI itself does NOT hit this problem: `ci.yml`'s `docs` job uses the
`stacklok/matlatl@f3d9023e # v0.0.7` composite GitHub Action (org-wide Actions
access, no `go run` module resolution), builds a binary, and diffs its
`index --llms` output against the committed `llms.txt` — which is how the
real staleness was confirmed as a genuine content diff, not a fetch/auth
flake.

**The fix must be done in an environment with real network access** — either
a maintainer's machine, or by driving it through CI (e.g. a throwaway branch
whose only change is the regenerated `llms.txt`, opened as a PR so the same
`docs` job's diff step turns green).

## The fix

From a network-connected checkout on top of current `main`:

```sh
task docs:llms   # regenerates llms.txt via the matlatl binary
git diff --stat llms.txt   # sanity-check the diff looks like doc-graph churn, not garbage
git add llms.txt
git commit -m "docs: regenerate stale llms.txt (drifted after PR #418)"
```

Then open a PR (or push directly to `main` per this repo's normal direct-commit
workflow — `llms.txt` alone is a low-risk, mechanically-generated file) and
confirm the `Docs (matlatl)` check goes green.

## Scope note

This fix is **independent of PR #408** (`396-sse-missing-on-run`, the
routing-reason work) — that PR's own `Docs (matlatl)` failure is just this
same pre-existing `main` staleness inherited via rebase, not something the PR
introduced. Do not block that PR's merge on this handover.

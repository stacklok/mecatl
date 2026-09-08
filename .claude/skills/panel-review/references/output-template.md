# Panel review — output template

The rendered shape of the Step 9 four-axis report and stable final summary.
The prose rules (do not merge across axes; synthesize within Domain only) live
in SKILL.md Step 9; this fenced example stays here so SKILL.md remains concise.

```
# Panel review — <scope>

**Fixed point:** <fp>
**Diff:** N files, M insertions, L deletions, K commits

---

## Spec — does this implement what was asked?

**Source:** #123 "Add /preview endpoint" (fetched via `gh issue view 123`)

- **[blocker · Missing]** Spec requires rate-limiting on `/preview`; diff
  doesn't implement it. (Spec lines 8–11: "the endpoint must cap
  to 60 req/min per IP".)
- **[important · Scope creep]** Diff adds a `/preview/v2` endpoint not in the
  spec.
- **[blocker · Wrong]** Spec calls for image-only previews; diff also fetches
  HTML. (Spec line 14: "image URLs only".)

(Or: "Spec axis skipped — no source available." with reason.)

---

## Standards — does this follow project conventions?

**Sources read:** CLAUDE.md, .claude/rules/code-style.md,
.claude/rules/<module>.md, docs/adr/NNNN, docs/adr/MMMM
**Tooling-skipped:** linter, formatter, type-checker

- **[blocker · Violation]** `<path/to/file>:42` uses an error-wrapping form
  forbidden by `.claude/rules/code-style.md § Errors`.
- **[advisory · Judgement]** New file `<path/to/new-file>` introduces a
  package-level singleton — `docs/design/principles.md` forbids
  global state. Was this discussed?

(Or: "Standards axis: no project standards docs found — axis
returned an empty report.")

---

## Test adequacy — do the tests independently prove the contract?

- **[blocker]** AC1.2 promises restart behavior, but no test crosses a persisted
  reload boundary; the current unit test exercises only the in-memory path.
- **[important]** The deny-path assertion never plants the forbidden value, so
  it stays green if the production filter is removed.

(Or: "Test adequacy axis: not applicable — no executable behavior or test
contract in this diff.")

---

## Domain — what do the specialist reviewers say?

**Panel:** secure-code-reviewer, kubernetes-deployment-expert,
devops-expert, library-reuse-reviewer, code-duplication-reviewer

### Ship-blockers (2)
- **[blocker · Critical]** SSRF in `/api/<endpoint>` —
  `<path/to/handler>:42`
  Sources: secure-code-reviewer
  …

### Cross-confirmed (1)
- **[blocker · High]** Missing `runAsNonRoot` and `PodSecurityContext` —
  flagged by both kubernetes-deployment-expert and
  secure-code-reviewer. Highest confidence.

### Mechanical fixes (3)
…

### Judgement calls (1)
- **[important · Medium]** New top-level dep `github.com/X/Y` — `go.mod`
  Sources: library-reuse-reviewer
  Rationale: stdlib `slog` covers logging; new dep is single-
  vendor. Discuss before adding.

### Polish (4)
…

### Gaps
- (none)

---

## Summary

- **Spec axis:** 3 findings (2 blocker, 1 important)
- **Standards axis:** 2 findings (1 blocker, 1 advisory)
- **Test adequacy axis:** 2 findings (1 blocker, 1 important)
- **Domain axis:** 2 blocker, 3 important, 5 advisory; 1 cross-confirmed

**Most important single issue:** Critical SSRF in
`<path/to/handler>:42` (Domain).

Each axis is orthogonal — Spec, Standards, Test adequacy, and Domain findings
don't mask each other. Verify each axis independently before shipping.

Apply mechanical fixes now?

PANEL: ship_blockers=6 important=5 advisory=6 reviewer_failures=0
```

[← back to the skill](../SKILL.md)

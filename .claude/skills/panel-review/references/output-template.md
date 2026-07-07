# Panel review — output template

The rendered shape of the Step 9 three-tier report. The prose rules
(Don't merge across axes; synthesis within Domain only; the
synthesis numbered list) live in SKILL.md Step 9 — this is just the
fenced example block, kept here so SKILL.md stays under the 500-line
guideline.

```
# Panel review — <scope>

**Fixed point:** <fp>
**Diff:** N files, M insertions, L deletions, K commits

---

## Spec — does this implement what was asked?

**Source:** #123 "Add /preview endpoint" (fetched via `gh issue view 123`)

- **[Missing]** Spec requires rate-limiting on `/preview`; diff
  doesn't implement it. (Spec lines 8–11: "the endpoint must cap
  to 60 req/min per IP".)
- **[Scope creep]** Diff adds a `/preview/v2` endpoint not in the
  spec.
- **[Wrong]** Spec calls for image-only previews; diff also fetches
  HTML. (Spec line 14: "image URLs only".)

(Or: "Spec axis skipped — no source available." with reason.)

---

## Standards — does this follow project conventions?

**Sources read:** CLAUDE.md, .claude/rules/code-style.md,
.claude/rules/<module>.md, docs/adr/NNNN, docs/adr/MMMM
**Tooling-skipped:** linter, formatter, type-checker

- **[Violation]** `<path/to/file>:42` uses an error-wrapping form
  forbidden by `.claude/rules/code-style.md § Errors`.
- **[Judgement]** New file `<path/to/new-file>` introduces a
  package-level singleton — `docs/design/principles.md` forbids
  global state. Was this discussed?

(Or: "Standards axis: no project standards docs found — axis
returned an empty report.")

---

## Domain — what do the specialist reviewers say?

**Panel:** secure-code-reviewer, kubernetes-deployment-expert,
devops-expert, library-reuse-reviewer, code-duplication-reviewer

### Ship-blockers (2)
- **[Critical]** SSRF in `/api/<endpoint>` —
  `<path/to/handler>:42`
  Sources: secure-code-reviewer
  …

### Cross-confirmed (1)
- **[High]** Missing `runAsNonRoot` and `PodSecurityContext` —
  flagged by both kubernetes-deployment-expert and
  secure-code-reviewer. Highest confidence.

### Mechanical fixes (3)
…

### Judgement calls (1)
- **[Medium]** New top-level dep `github.com/X/Y` — `go.mod`
  Sources: library-reuse-reviewer
  Rationale: stdlib `slog` covers logging; new dep is single-
  vendor. Discuss before adding.

### Polish (4)
…

### Gaps
- (none)

---

## Summary

- **Spec axis:** 3 findings (1 missing, 1 scope creep, 1 wrong)
- **Standards axis:** 2 findings (1 violation, 1 judgement)
- **Domain axis:** 2 ship-blockers, 3 mechanical, 1 judgement,
  4 polish; 1 cross-confirmed

**Most important single issue:** Critical SSRF in
`<path/to/handler>:42` (Domain).

Each axis is orthogonal — Spec, Standards, and Domain findings
don't mask each other. Verify each axis independently before
shipping.
```

[← back to the skill](../SKILL.md)

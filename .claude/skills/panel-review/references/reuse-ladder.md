# Reuse-ladder brief (Domain axis)

The prompt brief appended to the `library-reuse-reviewer` and
`code-duplication-reviewer` agent calls in Step 8 — the 7-rung
ladder and the `delete:`/`stdlib:`/`native:`/`yagni:`/`shrink:`
tag vocabulary.

## Why a structured brief

Without it, the reuse agents review ad-hoc: they find the
obvious reinvention and miss the speculative abstraction or the
in-codebase duplicate. The ladder forces a systematic pass: for
each added hunk, stop at the first rung that holds. The tags give
the synthesis step a uniform vocabulary to dedup across the pair
(a `stdlib:` finding from `library-reuse-reviewer` and a `shrink:`
finding from `code-duplication-reviewer` on the same hunk are
cross-confirmed).

## The brief (append to each reuse agent's prompt)

> Review the diff through the reuse ladder, stopping at the first
> rung that holds for each added hunk:
> 1. Does this need to exist at all? (YAGNI — `delete:`)
> 2. Does it already exist in THIS codebase? (reuse, don't rewrite)
> 3. Does the standard library do it? (`stdlib:`)
> 4. Does a native platform feature cover it? (`native:`)
> 5. Does an already-installed dependency solve it? (`native:`)
> 6. Can it be one line? (`shrink:`)
> 7. Only then: the minimum that works.
>
> Tag each finding: `delete:` / `stdlib:` / `native:` / `yagni:` /
> `shrink:`. One line per finding: location, what to cut, what
> replaces it. End with `net: -<N> lines possible.` If nothing to
> cut, say `Lean already.` and stop. Do NOT flag trust-boundary
> validation, error handling, security, or accessibility — those are
> never on the chopping block. A single smoke test / assert-based
> self-check is the minimum, not bloat; never flag it for deletion.
>
> Root-cause discipline: when a finding names a symptom, grep every
> caller of the function and recommend fixing the shared function
> once — one guard there is a smaller diff than one per caller.

## Discipline

The ladder runs AFTER understanding the problem, not instead of it:
tell the reuse agents to read the code the change touches and trace
the real flow before picking a rung. A small diff you don't
understand is a second bug, not laziness.

## Why default-on (not signal-gated)

Over-engineering is the most common failure mode an agent
introduces (reinvented stdlib, speculative abstraction, unrequested
layer). The signal is rarely visible on the diff surface — the
whole point of the reuse agents is to FIND it. Gating them on "a
new dependency was added" or "actual duplication shape is already
visible" skips them on exactly the diffs where they add the most
value. They default to silence when they find nothing, so the cost
of a false-positive inclusion is low (one quiet agent).

[← back to the skill](../SKILL.md)

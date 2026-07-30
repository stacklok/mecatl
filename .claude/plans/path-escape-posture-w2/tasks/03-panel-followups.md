---
id: 03-panel-followups
title: Wave-1 panel-review follow-ups (fingerprint asymmetry, vetRelaxedParent, classifier cache)
blocked_by: []
status: done
branch: "plan-path-escape-posture-w2/03-panel-followups"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture-w2
---

# Task brief

Fold in the three Wave-1 panel-review follow-ups logged in the plan's deferred
decisions. Each is a small, independent cleanup; do them together as one task.

Read `.claude/agents/tdd-worker.md` first.

1. **Edit-ledger `fingerprint` pseudo-fs asymmetry (Medium, latent).**
   `osfs.Workspace.fingerprint` (`internal/adapter/osfs/osfs.go`) reads via the
   inner `w.fs.Read`, bypassing `escapeWorkspace.refusePseudoFS`. Not live today
   (the escape policy hard-denies pseudo-fs for Edit before the tool body runs),
   but a latent asymmetry: if the pseudo-fs decision is ever narrowed, the Edit
   ledger would read unguarded. Decide the routing so the pseudo-fs
   defense-in-depth is uniform across every tool-body read site. Keep it minimal
   and layering-clean (fingerprint is an inner-osfs method; the wrapper is
   composition). Add a guard test pinning that an Edit-ledger read of a
   pseudo-fs path cannot bypass the guard.

2. **`vetRelaxedParent` → delegate to `osfs.Canonicalize` (Low).** Within osfs
   the ancestor-walk is expressed three times (`resolveInRoot`, `Canonicalize`,
   `vetRelaxedParent`) with three return contracts. Make `vetRelaxedParent`
   canonicalize-then-compare via the already-extracted `Canonicalize` rather than
   re-walk, so a future semantic change can't drift the containment check. The
   existing Scenario 2/3 osfs tests must stay green.

3. **`escapePolicy` per-root classifier cache (Medium, resource).** `p.clfs` is
   an unbounded map keyed by session root + a mutex on the hot permission path.
   Decide: if the distinct-root cardinality is genuinely bounded by the
   deployment, document it as a row in `docs/adr/0027-cloud-native.md` List 1
   (resource inventory); otherwise bound it (an LRU, or move the classifier onto
   the session-scoped `escapeWorkspace` which already builds one). Pick the
   smaller honest option.

## Acceptance criteria

- AC-W2-F1: an Edit-ledger read of a pseudo-fs path cannot bypass the pseudo-fs
  guard (the asymmetry is closed or explicitly reasoned-safe with a test).
  - verify: `TestPathEscapePosture_EditLedgerPseudoFSGuarded`
- AC-W2-F2: `vetRelaxedParent` delegates to `osfs.Canonicalize` (no third
  hand-rolled ancestor walk); existing osfs containment tests stay green.
  - verify: `TestPathEscapePosture_VetRelaxedParentSharesCanonicalize` (or
    inspection if the collapse is structural)
- AC-W2-F3: the `escapePolicy` classifier cache is either bounded or documented
  as a bounded-cardinality resource in `docs/adr/0027-cloud-native.md` List 1.
  - verify: inspection — a List 1 row names it, or a bound is enforced by test

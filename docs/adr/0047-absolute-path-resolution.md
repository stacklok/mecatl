# ADR 0047 — Absolute path resolution inside the workspace root

- Status: Accepted
- Date: 2026-06-23
- Scope: `internal/adapter/osfs` (and `internal/adapter/acp` for parity) — the
  path resolution of the five filesystem tools (Read/Write/Edit/Glob/Grep via the
  `tool.Workspace` port).
- Supersedes: none
- Superseded by: [ADR 0108](./0108-on-demand-logical-skill-assets.md) — ONLY the skill read-root carve-out; the canonicalize-then-reject absolute-path decision remains authoritative

## Context

The five filesystem tools historically accepted ONLY session-relative paths: any
absolute path was rejected up front by `osfs.rootRelative` with
`ErrPathEscape: %q is absolute`, and the ACP `fsWorkspace.absPath` mirrored that
with its own "is absolute (paths must be session-relative)" rejection. The single
carve-out was the WithReadRoots skills path: `Read`/`Stat` alone accepted an
absolute path under an activated skill's base directory.

That contract wasted model turns. A model that had discovered a file via an
absolute path (from `Glob` output, a `Read` of an in-root skill, a tool result,
or an error message quoting an absolute path) could not reuse that absolute path
to read or edit the file — it had to mentally re-relativize it against the
workspace root first, and would often emit the absolute form, hit a hard
rejection, and retry. The rejection reason ("is absolute") was also misleading:
the path was not dangerous, it was merely the same physical file a relative path
would reach, addressed by its absolute alias.

The security boundary the up-front reject was defending — never let an untrusted
path reach `*os.Root` in a form that could escape the workspace — is already
preserved by `*os.Root`'s own containment for in-root paths (it refuses both
lexical `..` escapes and symlink traversal that would leave the root), and by the
canonicalize-then-reject step the ACP `confineSymlinks` already performed for its
editor delegation. The up-front absolute reject was a stricter-than-necessary
restatement of that boundary, not the boundary itself.

## Decision

Accept an absolute path in all five filesystem tools **iff it canonicalizes
inside the workspace root**. Canonicalize-then-reject across the board:

1. **`osfs.resolveInRoot`** (new) canonicalizes an absolute path: resolve the
   deepest EXISTING ancestor's symlinks (so a not-yet-existing leaf being Written
   is still vetted through its real parent), re-append the unresolved tail, and
   compare the resulting real path against the (already `EvalSymlinks`-resolved
   at construction) workspace root. Accept iff `realPath == root` or is under it;
   on accept reduce to the slash-separated root-relative form. Out-of-root →
   `ErrPathEscape`; a non-`ErrNotExist` stat error on an ancestor fails safe.
   This mirrors `internal/adapter/acp/fsworkspace.go:confineSymlinks` exactly in
   shape.

2. **`osfs.resolvePath`** (replaces `rootRelative`, now a method on
   `*FileSystem`) routes a relative path through the lexical clean and an
   absolute path through `resolveInRoot`. `Write` and the `resolveRead`
   (Read/Stat) path use it. `resolveRead` layers the WithReadRoots skills
   carve-out on top: on a `resolvePath` error it falls through to
   `allowedReadRoot`, so in-root acceptance is ADDITIONAL to the skills roots.

3. **The Edit read-ledger normalizes its key** (`osfs.Workspace.ledgerKey`): a
   file read by absolute path and checked by relative path (or vice versa) shares
   one ledger entry, so Edit's read-before-edit-and-unchanged invariant holds
   across the two path forms. The key is the canonical root-relative form; an
   out-of-root skills-base read (no root-relative form) falls back to the cleaned
   slash form.

4. **ACP parity** (`fsWorkspace.absPath` and `fsWorkspace.ledgerKey`): instead of
   rejecting all absolutes, accept an absolute path iff `confineSymlinks`
   confirms it resolves inside `Root()` — the same canonicalize-then-reject osfs
   applies, so the ACP and osfs workspaces treat absolute in-root paths
   identically. The ACP Edit read-ledger normalizes its key through `absPath`
   too (the canonical absolute buffer address — `absPath` canonicalizes BOTH
   relative and absolute in-root inputs to the SAME `<root>/<rel>` form), so a
   read-by-absolute and an edit-by-relative share one ledger entry exactly as in
   osfs; an escaping path `absPath` rejects falls back to the cleaned slash form
   (mirroring osfs's `ledgerKey` fallback). ACP and osfs are now fully at parity
   on absolute-path handling INCLUDING the ledger.

5. **Glob/Grep: NO behavioral change.** Patterns are not paths; a leading `/` in
   a pattern is stripped as before (`normalizeGlobPattern`). Patterns are never
   routed through `resolvePath`.

**Boundary preserved:** the os.Root symlink containment for in-root paths is
unchanged — an accepted absolute path is reduced to its root-relative form and
flows through the SAME `*os.Root` as a relative path. A symlink inside the
workspace whose target resolves OUTSIDE the workspace is rejected by
`resolveInRoot` at resolution time (defense-in-depth, before `os.Root`), whether
addressed relatively or absolutely. `ErrPathEscape` (sentinel unchanged) still
covers every escape; the message drops "is absolute" (now "escapes workspace
root" — more honest: the path is rejected for escaping, not for being absolute).

**memfs is unchanged** — it has no real root to canonicalize against, and its
existing lexical absolute reject stays (the fsconformance suite's
`/etc/passwd`-style escape cases still pass on both adapters against a fresh
tempdir root).

## Consequences

Easier: models waste fewer turns re-relativizing paths; an absolute path learned
from any in-band source (Glob, a tool result, an error) is directly reusable for
read/edit. The Edit ledger is robust to mixed path forms — a read by absolute and
an edit by relative (or the reverse) no longer falsely report "not read". The
osfs and ACP workspaces now agree on absolute-path handling.

Harder / costs: the resolution path for absolutes does real filesystem work
(`Lstat`/`EvalSymlinks` per ancestor) — negligible for the tool-call rate, but it
is more than a lexical check, and a pathologically deep not-yet-existing tail
walks the ancestor chain. The "is absolute" message contract that one test
pinned byte-for-byte is gone (updated); any external consumer matching that exact
string would need to match `ErrPathEscape` by sentinel (which is unchanged and
the supported way). We are committed to keeping `resolveInRoot` and ACP
`confineSymlinks` in shape-parity — they are documented as mirrors.

## See also

- `docs/usage.md` — the "Path forms" note under the skills section.
- `internal/adapter/osfs/osfs.go` package comment — the living FS-path contract.
- [ADR 0002](./0002-documentation-lifecycle.md) — the ADR lifecycle convention.

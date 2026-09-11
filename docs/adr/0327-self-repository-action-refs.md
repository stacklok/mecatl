# ADR 0327 — Self-repository refs for the mecatequi sibling actions

- Status: Accepted
- Date: 2026-09-11
- Scope: how `mecatequi-reusable.yml` references its first-party sibling composite actions, and the release-process cost that reference imposed
- Supersedes: [ADR 0028](./0028-mecatequi.md) — its **hardcoded-ref pinning** decision only. Every other 0028 decision stands: the mecatequi binary contract, the three-job split-privilege token boundary, and the reusable `workflow_call` workflow as the recommended adoption path are unchanged.
- Superseded by: none

## Context

ADR 0028 decided that `mecatequi-reusable.yml` must reference its three sibling
composite actions by a **hardcoded literal tag**:

```yaml
uses: stacklok/mecatl/.github/actions/mecatequi@v0.0.34
```

Two constraints forced it. A workspace-relative `./` path inside a `workflow_call`
workflow resolves against the **caller's** checkout, which does not contain these
actions, so the reference had to be a full repository path. And `uses:` does not
evaluate `${{ }}`, so the ref could not be derived at run time. The file therefore
had to name the very tag being created — a self-reference.

0028 recorded the cost honestly: every release had to bump those pins in the same
tagged commit, or a consumer on `@vNEW` would silently run the `vOLD` actions. A
CI gate made it mechanical.

Two things changed.

**The cost turned out to be larger than a bump.** Because the pins live in
`.github/workflows/`, the self-reference is what forced a release to carry a commit
touching a workflow file. When the release was automated, that became a hard block:
a GitHub App installation token cannot write under `.github/workflows/` without the
`workflows: write` permission, so the release bot failed with
`403 Resource not accessible by integration` on `POST /git/trees`. Granting that
permission would have given the release App standing power to rewrite CI across
every repository it is installed on — a materially larger privilege than the
`contents: write` it needs, and one that can execute arbitrary code in CI.

**The platform constraint was lifted.** In July 2026 GitHub shipped
self-repository syntax for exactly this problem, and its own rationale names the
situation 0028 was in: referencing an action in your own repository previously meant
either relying on `./` and a checkout, or hardcoding a version — "a maintenance
burden" that "quietly defeated commit SHA pinning".

## Decision

Reference the sibling composite actions with `$/`:

```yaml
uses: $/.github/actions/mecatequi
```

`$/` resolves to **this** repository at the exact ref the workflow is running from,
with no checkout. A consumer invoking
`stacklok/mecatl/.github/workflows/mecatequi-reusable.yml@v0.0.35` therefore gets
the `v0.0.35` actions automatically. That is precisely the property the literal pin
existed to guarantee, now supplied by the platform instead of by a release step and
a CI gate.

Retire the pin machinery entirely: the `lint:reusable-pins` target, its checker
script, and the release-time bump script are deleted rather than kept as dead
belt-and-braces. A gate that can no longer fail is misleading.

Keep `./` out. A workspace-relative path still resolves in the caller's checkout and
would still be wrong; the comment at the reference site says so, because the
distinction is invisible until a consumer's run fails.

Third-party actions are unaffected and stay SHA-pinned with a `# vX.Y.Z` comment
per the house set. `$/` applies only to first-party same-repo actions, where the
supply-chain argument for a SHA never held: these live in this repository and are
released with it.

## Consequences

A release no longer needs to modify a workflow file, so the release App needs only
`contents: write` and `pull-requests: write`. The release bot cannot rewrite CI.
This is the load-bearing consequence: it is what makes a fully automated,
PR-based release possible without escalating the App's privileges.

Version skew stops being a failure mode rather than becoming an unchecked one. The
old gate existed because a human could forget the bump; there is now nothing to
forget, and the deleted gate is not a lost safeguard.

The reference is now resolved by a GitHub platform feature rather than by a literal
in the file. That is a new external dependency, and a young one — shipped July 2026.
If its behaviour changes or it is withdrawn, the fallback is to reinstate literal
pins and the gate, which this ADR's supersession record makes recoverable. Verified
before adoption against a caller using the **full repository path**, the way an
external consumer invokes it, not merely a same-repo call.

One historical record is left stale by this decision, deliberately and without
editing it: [ADR 0319](./0319-release-archives-and-homebrew-tap.md)'s rejected
alternative "a tag-only release with no pre-tag commit — not available" was true
when written and is now false. ADRs are point-in-time records; this entry supersedes
the claim rather than rewriting it.

## See also

- [ADR 0028](./0028-mecatequi.md) — mecatequi's binary contract, token boundary, and the superseded pinning decision.
- [ADR 0319](./0319-release-archives-and-homebrew-tap.md) — release archives and the Homebrew tap; its rejected-alternatives entry on pre-tag commits is superseded here.
- [The mecatequi CI guide](https://mecatl.dev/docs/building/deployment/mecatequi) — how consumers adopt the reusable workflow.
- [ADR 0002](./0002-documentation-lifecycle.md) — the documentation lifecycle this record follows.

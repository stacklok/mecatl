# ADR 0032 — First-class worktree binding for a session

- Status: Accepted
- Date: 2026-06-18
- Scope: session workspace binding, per-session engine routing, worktree discovery RPC, the mecatui `/worktrees` overlay
- Supersedes: none
- Superseded by: none

## Context

mecatl is a coding harness, but before this change it was too easy for a
session/agent to be stuck on the wrong checkout when the operator wanted work
done in a **specific existing git worktree** (issue #102). The session workspace
was fixed at launch — mecatui hardcoded the launch workspace into every
`CreateSession` — so the file tools (`Read`/`Edit`/`Write`/`Grep`/`Glob`)
remained scoped to the launch root even when the operator intended a sibling
worktree. `Bash` could reach a sibling via `git -C`, but `Bash` is no substitute
for `Edit`/`Write` (the edit invariants, read-ledger, and diff semantics all
stay tied to the session workspace).

Two hard constraints shaped the decision:

1. **It must also work in no-FS and cloud-native environments** (see
   [ADR 0027](./0027-cloud-native.md) and the cloud-native harness kit
   definition). A cloud `mecated` has no local filesystem and no notion of "the
   worktree's project authority"; a remote driver may supply capabilities from a
   service. So the discovery and switching interfaces must be clear, elegant,
   and separated — composition-injected ports, nil-safe — not hardcoded
   shell-outs in the server or domain.
2. **mecatui is a full local coding harness**, so the feature is ENABLED there.
   The operator workflow (list worktrees, pick one, start a new session rooted
   there) lives in the TUI.

What already worked (verified, not re-implemented): the main permission policy
ALREADY re-resolves `.mecatl/settings.yaml` per-session against the session
workspace root (`permpolicy.Policy.Evaluate` → `resolver.Resolve(ctx, ws)` keys
on `ws.Root()`), so a session created with `workspace=B` already gets B's project
permission rules for the main agent. `CreateSessionRequest.workspace` already
accepts any absolute path, and `WorkspaceFactory` already builds an osfs
Workspace from any root.

The gaps: (a) no discovery RPC; (b) mecatui had no switch affordance; (c) the
shared engine's CHILD permission resolver is pinned to the launch root, so
children of a worktree-B session would read the launch repo's project rules; (d)
a worktree session's per-session engine lived only in process memory and was lost
on restart.

## Decision

1. **A `ListWorktrees(workspace)` discovery RPC** mirrors `ListCommands`, backed
   by a NEW composition-injected, nil-safe `WorktreeLister` port in the server
   package (like `CommandLister`). The osfs-backed implementation shells out to
   `git worktree list --porcelain` with the SAME scrubbed+neutralised git env as
   `gitSnapshot` (`envscrub` then `gitenv`), trust-gated (`cfg.TrustProject`),
   fail-soft (any git fault returns an empty list, never an error). A no-FS/cloud
   deployment leaves it nil ⇒ empty list + `ServerCapabilities.worktrees = false`
   so a client hides the overlay honestly.

2. **A non-launch workspace routes through the per-session engine factory.**
   `Config.DefaultWorkspace` (the launch root; empty for a child/member/cloud
   service) is added to the server. `needPerSession` and `needsRehydration` are
   widened so a session whose `workspace != "" && workspace != DefaultWorkspace`
   enters the per-session factory (which ALREADY re-pins the CHILD permission
   resolver to the session root via `childPermResolverFor(cfg, workspace)`) and
   rehydrates to the SAME engine after a restart. When `DefaultWorkspace == ""`
   the new arm never fires (a non-empty workspace can't differ from ""), so the
   cloud/no-root posture is byte-identical. This keeps ONE resolver
   (`cfg.permResolver`, built once in `Build`) and re-pins per session — the
   repo's existing discipline — rather than threading the session workspace into
   the shared engine's already-built child deps.

3. **Trust stays OPERATOR-tier at server launch; per-worktree re-resolution is
   OUT of scope.** Worktrees share the base repo's `.git` (the identity anchor
   `workspacetrust` hashes is the repo, not the working tree), and the
   per-worktree `.mecatl/settings.yaml` is ALREADY re-resolved per-session by the
   main policy. Trust is monotonic-positive admission (it never revokes a Deny or
   a configured Ask), so inheriting the operator's launch grant for a sibling
   worktree of the same repo is not a safety regression. mecatui (the only place
   the feature is ENABLED) launches from a trusted workspace and the operator
   explicitly opens a sibling; mecated (headless/cloud) has no per-worktree
   prompt. The acceptance criterion "Trust/project-tier resolution remains rooted
   at the selected workspace" is met by the EXISTING per-session main-policy
   re-resolution + the child-resolver re-pin.

4. **The `SessionCreator` ui interface is widened with a NEW method,
   `CreateSessionInWorkspace`, not by changing `CreateSession`'s arity.** The
   `/models` restart and the connect path are workspace-INVARIANT (they rebind
   the SAME launch workspace); only the new `/worktrees` switch passes a different
   root. Two methods means zero churn to existing goldens and connect tests.

5. **Workspace switch is operator-driven via `/worktrees` only; it creates a NEW
   session (never mutates a live one).** The model has no tool to switch
   workspaces. The switch reuses the PRECISE `/models` restart-now precedent
   (close-old → create-new) and the `restartFailedMsg` recoverable-failure path
   (no new msg type). osfs path confinement is unchanged — the worktree path is
   just another `root`; no external-path capability is added to `Edit`/`Write`.

6. **No `port.LLMRequest` field, no proto enum.** `Worktree` and the
   `ListWorktrees*` messages are plain proto messages + a unary RPC; `worktrees`
   is a `bool` `ServerCapabilities` field. Additive: an older server yields false
   and the client hides the overlay.

## Consequences

- A worktree session occupies a `MaxSessionEngines` slot — honest, since it has a
  genuinely different engine assembly (the re-pinned child resolver). The cap
  bounds it.
- The `WorktreeLister` shell-out is the ONE new `os/exec` git invocation; it is
  read-only, trust-gated, scrubbed-env, and fail-soft — the same posture as
  `gitSnapshot` and the forker's `git worktree add`. It is NOT MCP (no stdio).
- Trust does NOT re-resolve per worktree. An operator who trusts repo A and opens
  a sibling worktree B inherits A's trust grant for B. This is documented and
  accepted; a future per-worktree trust re-prompt (mecatui-side) is a follow-up,
  not blocked by this design.
- The `/worktrees` overlay is a SECOND selecting overlay (after `/models`); its
  confirm step offers only restart-now / undo (no "switch-next" — there is no
  pendingNext workspace concept).

## See also

- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md): the disposable-process
  arc this feature's restart-rehydration builds on.
- [ADR 0023 — Workspace Trust](./0023-workspace-trust.md): the trust model whose
  operator-tier scope this decision keeps.
- [ADR 0030 — Model selection heuristics](./0030-model-selection-heuristics.md):
  the per-session-engine precedent this reuses (the mode→model rebuild).
- `docs/architecture/parallelism.md`: the worktree-binding section (the living
  "how it works").
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md): the shipped-status tracker row.

# ADR 0237 — Listener-scoped workspace authority

- Status: Proposed
- Date: 2026-08-25
- Scope: session workspace selection at the server boundary and deployment composition
- Supersedes: none
- Superseded by: none

## Context

A `CreateSession` caller currently supplies a workspace path. For a default
filesystem session, the server requires only that it be non-empty before the
workspace factory opens that root. Authentication and caller ownership identify
who made the request; neither authorizes that principal to select an arbitrary
host or container filesystem path.

That is appropriate for the embedded and loopback developer workflow: the
local operator both runs the server and chooses a checkout or sibling worktree.
It is not appropriate when the same API is exposed through a non-loopback
listener. In that deployment, accepting a path string combines a remote
principal with the server's ambient filesystem authority.

## Decision

Workspace selection is a deployment/composition policy, not an inference made
from a request or from the server package's socket state.

- A loopback-only or embedded deployment may retain client-selectable absolute
  workspaces. A deployment deliberately reachable through a proxy despite a
  loopback bind must explicitly select server-assigned authority; bind topology
  is a default, not evidence that an API caller is local.
- A deployment with a network-facing API configures server-assigned workspace
  authority. Its filesystem session requests must carry an empty workspace; the
  service rejects every non-empty client value with `InvalidArgument` before
  path cleaning, filesystem access, trust evaluation, or environment creation,
  rather than comparing, canonicalising, or silently substituting it. The
  service assigns the configured deployment root.
- A filesystem-bearing network deployment must fail before listener startup
  when it has no configured authoritative workspace. A no-FS deployment is the
  exception: it has no filesystem root to configure. `mecak8s` maps the wire's
  empty (omitted/default) profile to no-FS and rejects every non-no-FS profile
  unless a future operator-enabled mounted-workspace deployment defines it.
- `mecated` chooses the policy from its listener topology: its default
  loopback-only deployment remains client-selectable, while any non-loopback,
  wildcard, or mixed API-listener configuration is server-assigned. `mecak8s`
  is always server-assigned and defaults new sessions to the existing no-FS
  profile.
- Server-assigned authority applies to persisted-session run entry and
  rehydration, scheduled-fire creation, legacy adoption, and
  composition-created environment overrides as well as direct API creation.
  Stored filesystem roots are equal only when both are non-empty, absolute,
  clean paths with identical cleaned strings. Relative paths, traversal
  spellings, symlink aliases, and a root stale after configuration
  changes fail closed before filesystem access. This stored-state comparison
  does not relax the direct-request rule: a non-empty client workspace remains
  rejected without cleaning or comparison.
- A remote `mecatui` rejects an explicit workspace locally before resolving or
  transmitting a cwd. The Service remains the enforcement boundary for stale
  and non-mecatui clients.

The wire workspace field remains a client request field for compatibility; an
empty value in a server-assigned deployment is an intentional request for the
operator-selected root, not a fallback inferred from the client process.

## Consequences

- Remote callers cannot turn an authenticated API call into a mount-like
  request for `/`, `/etc`, or a service-account secret path.
- A remote `mecatui` client sends no local cwd by default. Explicitly passing a
  workspace to a network target produces a clear rejection rather than a
  misleading success.
- Existing local developer workflows, including operator-chosen sibling
  worktrees, retain their current workspace selection semantics.
- This is a single-root deployment policy, not a multi-workspace authorization
  design. Remote multi-workspace use requires opaque scoped grants or handles;
  accepting path strings is not an interim authorization mechanism.

## See also

- [ADR 0032 — First-class worktree binding for a session](./0032-worktree-binding.md)
- [ADR 0095 — Root-aware project trust](./0095-root-aware-project-trust.md)
- [Architecture guide](../architecture.md)
- [Deployment and server hardening](../architecture/deployment-and-hardening.md)

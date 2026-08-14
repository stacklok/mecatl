# ADR 0108 — Read skill assets on demand by logical name

- Status: Accepted
- Date: 2026-08-14
- Scope: skill activation, bundled assets, filesystem exposure, and remote skill sources
- Supersedes: [ADR 0005](./0005-driver-seams.md) and [ADR 0047](./0047-absolute-path-resolution.md) — ONLY their skill-asset materialization/read-root carve-out; all other driver and absolute-path decisions remain authoritative
- Superseded by: none

## Context

ADR 0005 kept `tool.SkillSource` path-free at the port but converted its assets back
into host paths at composition: filesystem skills were served in place, remote assets
were materialized into a temporary cache, and both became workspace read roots. That
made the delivery mechanism observable to the model as a base directory, coupled skill
loading to workspace construction, and created process-lifetime cache and cleanup state.
It also made activation responsible for transferring an entire remote bundle even when
the instructions needed only one textual reference.

The justification was that Bash needs real files and cannot execute bytes from a virtual
filesystem. That is true of shell execution, but it does not imply that activating an
instruction bundle should materialize or make executable every bundled payload. Most
skill assets are textual references that the model can consume directly. Automatically
placing driver-controlled bytes on disk, preserving executable bits, and advertising a
path conflates instruction disclosure with execution capability.

## Decision

Keep skill assets path-free end to end. Calling `Skill` with `{name}` returns the
instruction body and a bounded inventory of logical asset names. When an instruction
needs a textual asset, the model calls the same tool again with `{name, asset}`. The tool
validates the logical name against the advertised inventory, reads only that asset from
`SkillSource`, enforces a bounded payload, rejects invalid UTF-8 and NUL-containing
content, and returns the text in the tool result.

Do not materialize assets, grant skill directories as workspace read roots, advertise a
base directory, preserve an executable bit on disk, or implicitly execute bundled
content. Filesystem and remote sources have the same model-facing behavior. Slash-command
skill expansion may include the logical inventory, but asset retrieval still requires a
`Skill` tool call.

A skill that genuinely requires a shell-readable or executable file must say so and use
an explicit, permission-governed workflow to create or obtain that file in the session
workspace. The existence of Bash does not turn every textual reference into an implicit
filesystem or execution grant.

## Consequences

Remote activation transfers the body and asset inventory, then only assets the model
selects. There is no temporary asset cache, cleanup callback, skill-derived read root, or
source-specific base-directory rendering. Skill assets also work in no-filesystem
sessions because retrieval is through the read-only `Skill` tool rather than `Read`.

Bundled binary data and scripts are not implicitly available for `Read` or Bash, and the
`Executable` descriptor remains advisory source metadata rather than a materialization
instruction. Authors must write textual references so the model calls `Skill` again with
the logical `asset`; workflows needing executable files must provide an explicit tool or
workspace-creation step and pass normal permission checks.

## See also

- [Extension points](../architecture/extensibility.md)
- [Observability and persistence](../architecture/observability.md)
- [Skills, soul, and user model](../usage/skills-soul-usermodel.md)
- [Remote content-source configuration](../usage/configuration.md)
- [Cloud resource inventory](./0027-cloud-native.md)

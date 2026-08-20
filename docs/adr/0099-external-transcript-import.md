# ADR 0099 — External transcript import (`mecated import`)

- Status: Accepted
- Date: 2026-08-06
- Scope: `cmd/mecated import` and `internal/adapter/agentimport`
- Supersedes: none
- Superseded by: none

## Context

Developers arrive at Mecatl from other agent tools — Codex and Claude Code — with
session transcripts and skill bundles already on disk. To resume that work inside
Mecatl they need a way to ingest those local artifacts as a Mecatl session without
re-running the conversation by hand. The import is an operator-invoked, fully offline
migration command: it starts no listener and contacts no provider, so it must not
depend on a model credential or a running daemon.

The hard constraint is provider neutrality. A Codex or Claude Code transcript carries
provider-private records — encrypted reasoning blobs, system/developer instructions,
and provider-specific tool-call/tool-result wire shapes. Another provider cannot
safely replay those records: the reasoning is opaque, the tool IDs and content
schemas do not map, and replaying an alien tool call could synthesize behavior the
importing provider never authorized. Importing them verbatim would produce a history
that 400s or, worse, silently misbehaves on resume.

A second constraint is the trust boundary around Agent Skills. A `SKILL.md` file
steers the model exactly like `AGENTS.md`/`CLAUDE.md`, so importing skill bundles is
the same trust class as pointing `mecated serve --skills-dir` at a directory. The
import must not widen that boundary silently.

Finally, the command writes into the operator's workspace and the model-loaded
`.mecatl/skills` directory, so it must not overwrite anything that is already there,
must not follow symlinks, must not copy `.git`, and must keep every copied name
confined to the destination tree (a skill directory named `..` must not escape).

## Decision

`mecated import` ingests Codex and Claude Code sessions as **lossy provider-neutral
text-only history**. Only user and assistant text enters the conversation;
system/developer/reasoning/tool-call records are deliberately dropped because
provider-private records cannot be safely replayed by another provider. The
imported session is idle, uses Mecatl's default permission mode, and binds to the
provider/model selected when it is resumed.

Workspace and skill copying is **opt-in and non-overwriting**:

- `--copy-files` copies regular files from the source workspace into `--workspace`,
  skipping `.git`, symlinks, and special files, and refusing any destination path
  that already exists. It requires an explicit `--workspace` (no implicit
  overwrite of the cwd).
- `--skills` copies conventional Agent Skills bundles into
  `<workspace>/.mecatl/skills`; `--skills-dir` adds explicit sources. Cross-source
  and destination name collisions are detected **before** any bundle is written.
  Skill names are sanitized (rejecting `.`/`..` and any name containing a path
  separator) so a malicious skill directory cannot escape the destination tree — the
  same `safeAgentMemoryDir` confinement discipline used for project agent memory.

The `--skills` and `--skills-dir` flag help text and the deployment/usage docs call
out the trust boundary: an imported `SKILL.md` steers the model like
`AGENTS.md`/`CLAUDE.md`, so only import from trusted sources.

The implementation lives in `internal/adapter/agentimport` (transcript parsing +
workspace/skill copying) and `cmd/mecated/import.go` (the offline command). It is a
pure local-file operation: it reads the transcript, writes the local JSONL store
used by `mecated serve --store-dir`, and copies files on disk.

## Consequences

- Easier: a developer can migrate an existing Codex/Claude Code session into a
  resumable Mecatl session in one offline command, with no provider credential and
  no daemon running.
- Harder: the imported history is lossy by design. Reasoning context, tool
  interactions, and any provider-private state are gone, so a resumed session starts
  from text only and must re-derive tool/environment state from the conversation. We
  accept this rather than ship a history that lies about what a different provider
  can replay.
- We are committed to keeping the import confined: the skill-name sanitization, the
  destination-inside-source guard, the symlink/special-file skipping, and the
  no-overwrite preflight must stay load-bearing — they are the only thing between a
  hostile source tree and the operator's workspace.
- The trust boundary on skills is documented, not enforced: an operator who imports
  an untrusted skill bundle gets model-steering content in `.mecatl/skills`, the
  same as pointing `--skills-dir` at an untrusted directory. This is intentional —
  import is an operator action, and the operator is the trust principal.

## See also

- [ADR 0002](./0002-documentation-lifecycle.md) — the documentation lifecycle
  convention this ADR follows.
- `docs/usage/mecated.md` — the import command flags and usage.
- `user-docs/building/deployment/mecated.md` — the operator-facing import guide.

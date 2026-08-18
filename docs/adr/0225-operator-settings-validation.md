# ADR 0225 — Operator settings validation command

- Status: Accepted
- Date: 2026-08-17
- Scope: mecated operator settings validation and learning-policy preflight
- Supersedes:
- Superseded by:

## Context

The learning-configuration skill needs to validate an existing operator settings file
and preflight a proposed `learning:` replacement without changing the real file. A
private Go script embedded in that skill made a generally useful operator capability
depend on a source checkout and duplicated command-line, bounded-read, and YAML safety
policy outside `mecated`.

A generic YAML merge would be too broad: aliases, duplicate keys, multiple documents,
or unrelated replacement semantics could turn a narrow policy preflight into an
unsafe configuration transformer. Validation must not expose secret-shaped settings
values or create the target as a side effect.

## Decision

Provide `mecated config validate [--file PATH] [--learning-patch PATCH]` as an offline,
read-only operator command. Without `--file`, use the same conventional XDG settings
path as `config init`. Bounded-read regular files through a final-component
no-follow, nonblocking descriptor open, reject symlinks and irregular files before
reading, and validate the complete document with the repository's `permconfig` parser.

Make `--learning-patch` purpose-specific. Require one YAML document containing exactly
one top-level `learning:` mapping, with no aliases or duplicate keys. Replace or insert
only that node in memory, marshal, and validate the complete result. Permit a missing
base only when patch intent is explicit, report it as a valid prospective new file,
and never write either input or print configuration values.

Remove the skill-private validator. The skill may create only a temporary, non-secret
learning-only patch under the repository-local `.scratch/`, must quote both paths, and
must remove the patch after every outcome. Editing the actual settings file remains a
separate explicitly confirmed action followed by validation of the real file.

## Consequences

Operators can use the same supported binary in local and remote deployments, and the
skill no longer carries executable validation code. The narrow patch operation avoids
committing to generic deep-merge semantics and supports safe first-creation preflight.

Preflight may reformat the in-memory YAML representation, but no rendered document is
written or displayed. Operators who prohibit even a non-secret scratch patch must use
a manual block and validate the actual file after they apply it.

## See also

- [Architecture](../architecture.md)
- [Configuration usage](../usage/configuration.md)
- [Configuration reference](../configuration-reference.md)
- [ADR 0002](./0002-documentation-lifecycle.md)

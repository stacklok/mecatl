# ADR 0113 — Operator-configured MCP authentication profiles

- Status: Accepted
- Date: 2026-08-15
- Scope: global streaming-HTTP MCP server authentication and local login
- Supersedes: None
- Superseded by: None

## Context

Global MCP servers previously had only the repeatable `--mcp-server` flag and its
`MCP_<NAME>_TOKEN` convention. The OAuth controller and loopback runtime existed, but
shipping them without one operator-owned profile schema would have encouraged different
secret, egress, and persistence policies in each command root. Daemons must also never
open a browser merely because a server challenges for OAuth.

## Decision

Use the strict operator-tier `mcp.servers` settings subtree for named global servers.
Each whole profile selects `none`, `static_bearer`, or `oauth`; YAML and argv contain
secret references, not secret values. One `internal/cliconfig` loader merges those
profiles with legacy CLI entries and constructs credential sources for `mecated`,
`mecatequi`, and `mecak8s`. The MCP manager/controllers close before those sources.

Normal serving, ACP, and batch roots install no OAuth presenter. The only shipped
interactive entry point is `mecated mcp login SERVER [--no-browser] [--permission-config
PATH ...]`. The repeatable permission-config option selects trusted operator settings only;
it never carries OAuth values. The command selects a
profile case-insensitively, requires OAuth with a mutable local encrypted store, and
constructs the loopback runtime only after configuration and storage validation.
Environment-backed credentials are externally provisioned and require a process restart.
ACP cannot provide OAuth profiles or install/drive authorization; after operator authorization,
ACP sessions may invoke the shared global OAuth-backed tools under ordinary permissions. OAuth is
not added to per-session MCP, inline agent definitions, or ToolHive
discovery. Dynamic client registration remains unsupported.

## Consequences

All three roots resolve identical global profiles and preserve the legacy bearer path.
Login-required diagnostics can name the server and a safe remedy without exposing URLs,
secret references, or adapter errors. Kubernetes can consume read-only environment
credentials without giving a pod a browser or mutable local store.

OAuth remains deliberately narrow. Broad production readiness is still blocked on the
upstream SDK metadata-profile gates identified by ADR 0219; this decision does not claim
general OAuth interoperability or ACP-supplied OAuth profile/authorization support.

## See also

- [Extensibility architecture](../architecture/extensibility.md)
- [Configuration guide](../usage/configuration.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
- [ADR 0112](./0112-mcp-oauth-loopback-runtime.md)

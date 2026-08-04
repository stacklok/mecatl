# ADR 0088 — Explicit daemon.yaml (listener topology config)

- Status: Accepted
- Date: 2026-08-03
- Scope: `cmd/mecated` CLI surface — `config daemon init` / `config daemon validate` subcommands, the daemon.yaml v1 schema (in `internal/adapter/daemonconfig`), and the `serve --config PATH` load path
- Supersedes: —

## Context

`mecated` carried ~80 flags. The operator-facing serve-time topology — gRPC/HTTP/metrics listen addresses, TLS cert/key/CA paths, rate-limit/burst — was expressible only as repeated CLI flags on every invocation. A long-lived deployment wants a small, versioned file for that slice, and operators want to scaffold and validate it without starting the server.

Two other config surfaces already exist and must NOT be conflated:

- **`settings.yaml`** (via `mecated config init`, owned by `permconfig`/`configgen`) is the operator POLICY/TRUST file — permissions, posture, guardrails, model taxonomy. It is auto-discovered per the conventional XDG/project hierarchy.
- **Auth secrets** (`MECATL_AUTH_TOKEN`, `--auth-token`, provider keys) are env/CLI-only. A token value in a world-readable YAML file is a downgrade.

The core `--config PATH` daemon config support (strict versioned v1 schema, precedence `defaults < file < explicit CLI`, no auto-load, rejected in ACP) landed in `internal/adapter/daemonconfig` (the `serve --config` load/merge path). This ADR records the UX/docs half (task B): the `config daemon init`/`validate` subcommands, the documented conventional path, and the decision that fixes the surface's shape.

## Decision

1. **Separate, explicit-only file.** daemon.yaml is a DISTINCT file from settings.yaml. It carries ONLY the API-edge topology slice — the v1 fields: `version`, `grpc_addr`, `http_addr`, `metrics_addr`, `tls_cert`, `tls_key`, `client_ca`, `rate_limit`, `rate_burst` (the first API-edge field slice). It is loaded ONLY when an operator supplies `mecated serve --config PATH`; there is **NO conventional auto-load** — a daemon.yaml at the conventional path is inert unless `--config` names it.

2. **settings.yaml remains policy/trust; auth secrets remain env/auth sources.** daemon.yaml does NOT carry permissions, posture, guardrails, models, or any auth TOKEN VALUE. The API bearer token stays `MECATL_AUTH_TOKEN` / `--auth-token`. A non-loopback bind still requires auth/TLS (the same trust model; daemon.yaml changes topology, not trust).

3. **cmd/mecated owns serve-time fields (no app.Config widening).** The daemon config slice never widens `app.Config` or the engine; it folds into the cmd-mecated `config` struct's existing serve-time fields (listen addresses, TLS, rate-limit) via `mergeDaemonConfig`, so composition stays byte-identical. The schema lives in `internal/adapter/daemonconfig` (adapter-side helper, imported only by `cmd/mecated`).

4. **Strict versioned schema.** `daemonconfig.Load` parses with `KnownFields(true)`: an unknown top-level key is a parse error (not a silently-ignored typo), a missing/unsupported `version` is rejected, and a multi-document file is refused. Pointer fields distinguish absent (`nil`) from an explicit zero/empty, so the precedence is exact.

5. **Precedence: defaults < file < explicit CLI.** A field ABSENT from the file keeps the built-in default; a field PRESENT overrides the default; an explicit CLI flag overrides the file value, INCLUDING an explicit empty/zero (so `--metrics-addr ""` disables metrics even if the file sets `metrics_addr`). `cliExplicit` (populated via `fs.Visit`) tracks which migrated flags were set on the CLI.

6. **`config daemon` extends the `config` namespace; no top-level `daemon` command.** The subcommands are `mecated config daemon init [--print] [--force]` (scaffold a minimal, commented v1 skeleton at the conventional `<XDG_CONFIG_HOME>/mecatl/daemon.yaml`; does NOT cause loading; `--print` writes only stdout; default refuses overwrite, `--force` explicit) and `mecated config daemon validate [--file PATH]` (strict parse + the effective semantic validation possible without starting/binding — rate-limit/burst bounds; default file is the conventional path for convenience; success names the file/version and reminds how to use it; never prints secrets/raw content). `config init` keeps ownership of settings.yaml; help distinguishes settings policy vs daemon topology. Command resolution fails closed for a missing/unknown `config daemon` subcommand — it never reaches `run()`/listeners.

7. **Reuse, not a second configgen framework.** `config daemon init` embeds the committed skeleton (`//go:embed daemon.skeleton.yaml`); `config daemon validate` reuses `daemonconfig.Load` + `daemonconfig.Validate` (the SAME schema/runtime bound the serve path applies). The skeleton is small and runnable — version plus loopback defaults and concise commented TLS/rate examples. There is no second generic configgen framework and no go/ast in the shipped binary.

## Validation ownership boundary

`daemonconfig.Validate` (called by both `config daemon validate` and the serve
path) covers ONLY source-independent schema/rate bounds: negative or non-finite
`rate_limit`, negative `rate_burst`. These are the checks shared identically
between the offline `config daemon validate` UX and the serve-time pre-bind
validation.

Merged effective cross-field security checks — specifically:

- The perf-MCP loopback gate (`--perf-mcp` refusing a non-loopback or empty
  `--metrics-addr`)
- TLS cert/key pair existence (one supplied without the other), and client-CA
  requiring cert+key

— remain in `cmd/mecated` (`validateEffectiveConfig` and `buildTLSConfig`),
because they require the final merged effective config (CLI flags + daemon.yaml
file values, per the `defaults < file < explicit CLI` precedence). These checks
live AFTER the merge step and BEFORE any listener binds, so both CLI-only and
file-supplied values are covered. Offline `config daemon validate` deliberately
does NOT replicate them: it validates the file itself, not the effective
runtime merge.

This boundary is intentional. A future v2 schema that adds new inter-field
constraints would add them to `daemonconfig.Validate` ONLY when they are
source-independent (the file alone suffices to reject them); any constraint
that depends on CLI values stays in `cmd/mecated`.



## Consequences

- Operators can now scaffold (`config daemon init`) and lint (`config daemon validate`) a daemon topology file offline, then start with `mecated serve --config daemon.yaml` — one explicit flag instead of a long flag line, with a strict schema that rejects typos.
- The conventional path (`<XDG_CONFIG_HOME>/mecatl/daemon.yaml`) is documented but NOT auto-loaded: a file placed there is inert until `--config` names it. This is the deliberate boundary against the "wrong file silently binds a network listener" surprise.
- The two config files stay cleanly separated: settings.yaml (policy/trust, auto-discovered) and daemon.yaml (topology, explicit-only). An operator editing one cannot accidentally change the other's domain.
- Auth stays env/CLI-only: no `token:`/`password:` key exists in the v1 schema, and the skeleton carries no secret values. A non-loopback bind without auth/TLS still logs the same prominent WARNING (daemon.yaml changes topology, not the trust model).
- A new v1 field is a schema change (the strict parser is the gate); a future v2 supersedes v1 rather than widening it in place. OTLP and other serve-time knobs are deferred out of v1.

## See also

- [ADR 0002 — Documentation lifecycle](./0002-documentation-lifecycle.md)
- `docs/usage/mecated.md` — the daemon config reference (the 8 v1 fields + precedence)
- `docs/architecture.md` — the daemon config surface in the mecated composition-root section
- `internal/adapter/daemonconfig` — the schema, skeleton, and `Validate`
- `internal/adapter/daemonconfig` — also the core `--config` load/merge path (the API half this UX/docs half builds on)

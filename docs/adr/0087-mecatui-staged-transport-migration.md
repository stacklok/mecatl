# ADR 0087 — Staged mecatui transport migration

- Status: Superseded
- Date: 2026-08-03
- Scope: `cmd/mecatui` CLI surface, transport resolution, and mode-specific flag applicability
- Supersedes: —
- Superseded by: [ADR 0089](./0089-cli-clean-break-grammar.md) (the staged migration collapsed into a pre-1.0 clean break: the `local` subcommand, the `--server` flag, the AUTO probe, and the legacy warnings are deleted — bare `mecatui` embeds, `connect ADDRESS` dials)

## Context

`mecatui` reaches a `mecated` server in two ways: an explicit `--server ADDRESS`
dials a remote, and the default (no `--server`) AUTO mode probes the loopback
default `127.0.0.1:8080` and, if nothing answers, hosts an embedded server
in-process over a UNIX socket. The AUTO probe is a guess: on a machine where a
unrelated process happens to bind `127.0.0.1:8080`, `mecatui` silently attaches to
it (or, when the probe fails, silently embeds). The operator cannot say "always
embed" or "always dial this address" without the implicit probe/embed fallback
running first.

Three `--subagent-ask-reviewer*` flags were also accepted by `mecatui` "for
symmetry with mecated" but were inert there: `mecatui` runs interactive (a human
sits at the approval modal), so a child ask surfaces to the modal and never
reaches the headless reviewer. Accepting flags that do nothing is dishonest — an
operator who sets them believes they have an effect.

## Decision

Migrate the transport surface in stages, keeping the legacy path byte-for-byte
until a later phase removes it.

### Phase 1 (this ADR)

1. Add two PURE canonical commands:
   - `mecatui local [flags]` ALWAYS hosts the embedded server and NEVER probes
     loopback.
   - `mecatui connect ADDRESS [flags]` ALWAYS dials ADDRESS and NEVER probes or
     embeds. ADDRESS must immediately follow `connect`; a missing or flag-first
     ADDRESS is a usage error.
   An unknown leading command fails closed before transport resolution.

2. Preserve the legacy bare/leading-flag invocation byte-for-byte: bare `mecatui`
   still AUTO-probes then embeds, and `mecatui --server ADDRESS` still dials remote.
   Emit a pre-TUI deprecation warning with the exact canonical replacement. No
   behaviour flip yet — the legacy path is removed in a later phase.

3. The transport mode is resolved by a PURE argv resolver
   (`cmd/mecatui/command.go` `resolveTransportMode`) that does not touch `os.Args`
   or any package-global state, mirroring the repaired `cmd/mecated/command.go`
   seam. The mode threads explicitly into `parseTransportFlags` and
   `resolveTransport`.

4. Reject explicit embedded-only flags BY NAME in `connect` mode; reject explicit
   remote-only flags (`--server`, `--auth-token`, `--tls`, `--tls-ca`, `--insecure`)
   in `local` mode. Shared workspace/session/UI flags remain valid in both. The
   legacy mode retains today's acceptance behaviour (no by-name rejection). A
   complete applicability metadata table (`cmd/mecatui/helpmeta.go`
   `flagApplicabilityByFlag`) is validated against the REAL production FlagSet
   (no synthetic subset); a default-unknown entry fails closed (rejects), never
   silently includes.

5. Remove the three inert `--subagent-ask-reviewer*` mecatui flags, their config
   fields, the `app.Config` mappings, the policy-file read, and their tests. They
   are now honest unknown-flag errors pointing operators at headless `mecated
   --headless --subagent-ask-reviewer …` + `mecatui connect`. The `--model-slot
   ask-reviewer=…` model slot stays (it is a mecated-side concern).

6. Preserve the trust prompt and the no-provider/posture validation ONLY for
   paths that may embed: `local` always; `legacy` only when no `--server` is set
   (the AUTO path); `connect` and legacy `--server` skip them as today. Canonical
   `local` works offline with `--mock`.

7. Add concise mode-specific help derived from the real FlagSet and the
   applicability metadata, reusing `internal/flaghelp/flaghelp.go` formatting
   (no second `flag.PrintDefaults` implementation). Top-level help is
   command-oriented. The deprecated `--output-economy` compatibility flag stays
   hidden from all help.

### Later phases (NOT in this change)

- Final removal of the legacy AUTO-probe and the `--server` flag.
- A TUI/daemon config-file surface (out of scope).

## Consequences

- Operators get an honest, explicit transport choice (`local` / `connect`) with
  no implicit probe, and the legacy path keeps working for one release with a
  deprecation warning.
- The by-name applicability check is a new failure mode for explicit modes: a
  `--mock` passed to `connect` (or an `--auth-token` passed to `local`) now
  errors instead of silently no-op'ing. The metadata-completeness invariant keeps
  the applicability table from drifting as flags are added.
- Removing the ask-reviewer flags is a breaking change for any invocation that
  passed them, but they were inert; the error now points at the real path.
- The pure resolver + explicit-mode threading make the transport path testable
  without a network or a live model (no probe, no embed in `connect` tests).

## See also

- `cmd/mecatui/command.go` — the pure transport-mode resolver.
- `cmd/mecatui/helpmeta.go` — the applicability metadata + progressive help.
- `cmd/mecated/command.go` — the repaired command-resolution seam this mirrors.
- [ADR 0020](./0020-diagnostics.md) — diagnostics discipline (no package-global
  slog in `internal/`).
- `docs/tui.md` and `docs/usage/mecated.md` — the user-facing transport docs.

# ADR 0089 — CLI clean break: one canonical spelling per action

- Status: Accepted
- Date: 2026-08-04
- Scope: `cmd/mecated` + `cmd/mecatui` CLI grammar, CLI flag surface, settings.yaml parsing, progressive help
- Supersedes: [ADR 0087](./0087-mecatui-staged-transport-migration.md) (the staged framing — the "later phase" it deferred lands here, with the `local` subcommand it introduced dropped, not graduated); [ADR 0086](./0086-remove-output-economy-control.md) (decision item 4 only — the one-release parse-compat window)

## Context

Pre-1.0, mecatl had accumulated a second spelling for nearly every entry action,
each kept alive by a compatibility shim:

- `mecatui` had FOUR transport spellings: bare (probe loopback, then embed),
  `--server ADDRESS` (dial), and the canonical `local` / `connect ADDRESS`
  subcommands ADR 0087 introduced *alongside* them, promising to remove the legacy
  pair "in a later phase". The AUTO probe is a guess: on a machine where an
  unrelated process binds `127.0.0.1:8080`, bare `mecatui` silently attaches to it
  (or, when the probe fails, silently embeds).
- `mecated` ran its network daemon on the bare invocation, and `--acp` selected
  the ACP stdio mode as a flag, with `serve` / `acp` subcommands as the newer
  spelling. A bare `mecated` starting a server is a script footgun (a typo'd
  leading token silently serves) and makes the help surface lie about what the
  binary is.
- ADR 0086 removed output-economy as an active behavior and, for one release,
  kept a hidden parse-compat shim: `--output-economy` parsed as a no-op with a
  deprecation WARN in all three binaries, and a top-level `output-economy:`
  settings.yaml key parsed leniently with the same WARN. The shim was deliberately
  marked "follow-up removal".

A staged migration only pays off while a release boundary stands between the
stages. Before 1.0 there is no such boundary to protect: every shim is permanent
complexity in service of a window that never materially exists — dead flags, dead
schema fields, hidden-flag exclusion maps in the progressive-help metadata, probe
code, and warning emitters whose only purpose is to soften a break no released
artifact depends on. These are one decision applied to three surfaces: pre-1.0,
break cleanly; one canonical spelling per action; delete every second spelling.

## Decision

Delete every second spelling. No compatibility shims, no deprecation warnings, no
aliases — a stale invocation fails fast with an honest error.

1. **mecatui transport.** Bare `mecatui [flags]` is deterministic LOCAL: it hosts
   the embedded `mecated` in-process over a private UNIX socket and NEVER probes
   loopback (the AUTO probe is deleted). `mecatui connect ADDRESS [flags]` dials a
   running `mecated` at ADDRESS and never embeds; ADDRESS must immediately follow
   `connect`. The `local` subcommand ADR 0087 added is DELETED (bare is the local
   spelling), the `--server` flag is DELETED (`connect ADDRESS` is the dial
   spelling), and every legacy warning is deleted. An unknown leading command
   fails closed before transport resolution. Mode-specific flag applicability
   (embedded-only flags rejected in `connect` mode, remote-only flags rejected
   bare) is unchanged from ADR 0087.
2. **mecated commands.** Bare `mecated` is a usage error (`errBareInvocation`):
   it prints the command help to stderr and exits 2. The network daemon requires `mecated
   serve`; ACP stdio requires `mecated acp`. The `--acp` flag is DELETED (an
   unknown-flag error) and every legacy warning is deleted.
3. **Output-economy shim.** `--output-economy` is unregistered in `mecated`,
   `mecatui`, and `mecatequi`; a legacy invocation fails at flag-parse time with
   the standard stdlib unknown-flag error (`flag provided but not defined`). The
   `hiddenFlags`/`hiddenMecatuiFlags` exclusion maps in the progressive-help
   metadata are deleted (they existed only for this flag); the help-all renderers
   use the zero-exclusion form of the shared cliconfig formatter, and the metadata
   completeness invariants no longer carve out hidden flags.
   `permconfig.Config.OutputEconomy` and the resolver's `operatorOutputEconomy`
   capture are deleted. The top-level settings.yaml decode stays deliberately
   lenient (plain `yaml.Unmarshal`), with ONE targeted rejection: a top-level
   `output-economy:` key is detected via a `yaml.Node` pre-pass and rejected with
   a named error (`output-economy: unknown key (the output-economy setting was
   removed; delete it from your settings.yaml)`) that rides the existing
   invalid-file WARN+skip path at every tier.
4. **Regression oracles stay.** `internal/app/output_economy_test.go` pins the
   absent tone delta, `internal/configgen` keeps the generated-artifacts absence
   check, and the CLI-grammar tests in `cmd/mecated` / `cmd/mecatui` pin the
   bare/connect/serve/acp resolutions.

## Consequences

- A stale deployment with `--output-economy`, `--server`, or `--acp` in its
  command line now fails fast at startup with an honest unknown-flag error
  instead of silently doing nothing; a bare `mecated` prints help and exits 2
  instead of starting a server.
- A stale settings.yaml carrying `output-economy:` gets a WARN naming the exact
  key to delete; the file's other rules are skipped for that load, as with any
  invalid file.
- `mecatui` can no longer silently attach to an unrelated process that happens
  to bind `127.0.0.1:8080` — the probe guess is gone; the transport is exactly
  what the invocation says.
- The progressive-help metadata invariant simplifies: every registered flag has
  metadata, no hidden-flag carve-out.
- There is no migration window. That is the point — and the cost: any out-of-tree
  script or doc snippet using a second spelling breaks loudly at upgrade, not
  softly over a release. Pre-1.0, we accept that trade once rather than paying
  shim maintenance indefinitely.
- No prompt, provider, or wire surface changes; this is CLI/config cleanup only.

## See also

- [ADR 0087](./0087-mecatui-staged-transport-migration.md) — the staged framing this supersedes (its `connect` subcommand and applicability checks survive; its `local` subcommand, `--server` flag, and legacy warnings do not).
- [ADR 0086](./0086-remove-output-economy-control.md) — the output-economy removal this follows up.
- [ADR 0041](./0041-output-economy-default-prompt.md) — the original, twice-superseded output-economy decision.
- `cmd/mecatui/command.go` (`resolveTransportMode`) — the pure transport resolver.
- `cmd/mecated/command.go` (`errBareInvocation`) — the bare-invocation usage error.
- `internal/adapter/permconfig/permconfig.go` (`parseYAML`) — the targeted unknown-key rejection.
- `docs/tui.md`, `docs/usage/mecated.md` — the user-facing transport/command docs.

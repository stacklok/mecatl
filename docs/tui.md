# mecatui — the terminal UI for mecatl

`mecatui` is a flashy, themeable terminal UI for the mecatl harness. It is a
**gRPC client**: it creates a session, opens the bidi `Converse` stream, renders
the streamed events (glamour markdown for assistant text, themed lipgloss cards
for user prompts and tool I/O), shows a thinking spinner and a status/usage
footer, and resolves permission prompts inline by sending `ResumeApproval` back
on the same stream.

The server it talks to is either one `mecatui` **hosts itself in-process** over
a private UNIX socket (the default) or an **external** `mecated` it dials via
`mecatui connect ADDRESS` — so a single binary "just works" with no daemon to
start and no TCP port. See [Run](#run).

It is built on the Charm v2 stack (Bubble Tea / Lip Gloss / Bubbles / Glamour).

### Local status lines

Mecatui reads `status_customization:` only from the client-owned
`$XDG_CONFIG_HOME/mecatui/settings.yaml`. It composes a UI-agnostic
`statusline.Source`: the UI submits display-safe `Input` snapshots and listens for
latest `Result` semantic spans, while the source owns template evaluation or the
optional local direct executable, refresh, cancellation, and fallback. The source
returns no terminal rendering; the UI applies the active theme, preserves its
mandatory safety/navigation and activity lanes, then clips and aligns the result.

A configuration selects exactly one source: responsive `templates` or a
`command` with an absolute `executable` and literal `args`. Templates receive an
automatically StatusML-escaped projection; a command receives the same raw input
as JSON on stdin. Input protocol v2 exposes the ordinary fixed handle as `Session.Handle`;
it replaces v1's `Session.Digest`, and no digest compatibility alias is emitted. It is run
directly (there is no shell or source configuration
form); `/bin/sh` is available only when explicitly selected as the executable with
literal arguments. It uses a fixed safe baseline environment; the optional
`passthrough_env` list may add explicitly named user-global variables but never
ambient environment values; reserved baseline and source-owned terminal-dimension names
are rejected during settings validation. It uses local-only CWD selection, a one-second deadline, and a combined
4 KiB stdout/stderr limit. StatusML accepts semantic theme tokens and validated
HTTP(S) link metadata, never raw ANSI or OSC. Before parsing, only leading and
trailing ASCII whitespace is trimmed, allowing ordinary `print` output while
preserving internal text.

See [Status line customization](https://github.com/stacklok/mecatl/blob/main/user-docs/mecatui/status-line.md)
for the complete settings schema, input reference, StatusML grammar, safety limits,
and copyable template and executable examples.

The render packages (`ui`, `theme`) and the `client` package stay a pure client —
they never import any `engine/...` or `internal/...` package and render solely from the proto
`Event` envelope. Hosting the embedded server is confined to the `cmd/mecatui`
main and its `embed` subpackage (which build the same server `mecated` does, via
`internal/app`).

## Build

```sh
task build          # → bin/mecated, bin/mecademo, bin/mecatui
```

## Run

The simplest path needs no separate server — just launch the TUI with an LLM key:

```sh
# Embedded server (default): mecatui privately configures its server root from cwd
# (or embedded-only --workspace) and never sends that path through CreateSession.
ANTHROPIC_API_KEY=sk-ant-... bin/mecatui # Anthropic (Claude)
OPENAI_API_KEY=sk-...      bin/mecatui # OpenAI
OPENROUTER_API_KEY=sk-or-... bin/mecatui # OpenRouter

# Offline, no network — uses the canned mock provider:
bin/mecatui --mock
```

Bare `mecatui` always hosts an **embedded** server itself (a UNIX socket in
`$XDG_RUNTIME_DIR` or the OS temporary directory, torn down on exit) — it
**never probes** loopback for an already-running `mecated`. On macOS,
an overlong runtime path falls back to a private directory directly under `/tmp`
to stay within Darwin's UNIX-socket path limit. The embedded provider is **auto-detected**
from the environment — `ANTHROPIC_API_KEY` enables the `anthropic` provider (the
native Messages API), `OPENAI_API_KEY` the `openai` provider, `OPENROUTER_API_KEY`
the `openrouter` provider (set several, and you pick between their models in the
**`/models`** picker — see the Overlays section). With **no** key at all, startup
**fails with guidance** — pass `--mock` explicitly for the offline mock provider.
When more than one is keyed, run `/models` to choose; the choice is persisted per
workspace.

Embedded mecatui also supports experimental ChatGPT Codex subscription inference
through the distinct `openai-codex` provider. Put the manual OAuth snapshot in
`auth.yaml`, then launch with the provider explicit:

```sh
bin/mecatui --workspace "$PWD" \
  --auth-file ~/.config/mecatl/auth.yaml \
  --default-provider openai-codex
```

The token is not public API credit and the private backend is not a supported
third-party contract. mecatui reads one immutable snapshot before hosting its
embedded server: after replacing an expired/rejected token, quit and relaunch.
There is no login or refresh. See the [exact schema and plaintext same-UID Bash
boundary](usage/mecated.md#openai-codex-subscription-manual-token-experimental).
`mecatui connect` never reads the local file; configure the external `mecated`
instead.

To use a specific **external** server instead, use the `connect` subcommand:

```sh
# External loopback server: omitted --tls is plaintext only for loopback.
bin/mecated serve &                                    # server owns its configured root
bin/mecatui connect 127.0.0.1:8080

# Remote targets use verified TLS automatically; --tls=false is the explicit
# plaintext downgrade and is appropriate only for controlled non-bearer testing.
bin/mecatui connect mecated.example.internal:443
```

`--workspace` is embedded/operator configuration only. In embedded mode it defaults
to the current directory, is resolved locally, and configures the hosted server's
private deployment default. `mecatui connect` never accepts or transmits a workspace
or cwd—even to loopback—because all CreateSession calls use the same path-free contract.
The external `mecated` chooses its configured default; `mecak8s` normally binds no-FS.
Alternate worktrees are selected only from `/worktrees`: the server lists an owned
source session and issues an opaque, source-scoped selector for ClearSession/ForkSession.
Selectors expire on server restart, so mecatui relists; a failed relist or switch keeps
the current session.

### Transport commands

Run `mecatui --help`, `mecatui -h`, or `mecatui help` for the concise top-level command index. `mecatui help sessions`, `mecatui help connect`, `mecatui help debug`, and `mecatui help login` alias their corresponding command-specific help; direct `sessions --help`, `connect --help`, `debug --help`, and `login --help` also work. Use bare `mecatui --help-flags` for common embedded-mode flags and bare `mecatui --help-all` (or the corresponding `sessions` or `connect` form) for the exhaustive flag reference.

The transport is exactly what the invocation says — there is no implicit probe
or fallback:

- **Bare `mecatui [flags]`** — always host an embedded `mecated` in-process over
  a private UNIX socket; **never probe** loopback, **never dial**. The socket
  lives in a same-local-user private directory; it is a local trust boundary,
  not a bearer/OIDC authentication surface. Its sessions are therefore
  intentionally ownerless. All embedded-server flags (`--mock`,
  `--trust-project`, provider knobs, …) apply, and the remote-only flags
  (`--auth-token`, `--tls`, `--tls-ca`, `--insecure`) are **rejected** here.

- **`mecatui sessions [flags]`** — use the same embedded transport and flags as
  bare `mecatui`, but open the stored-session inventory before creating anything.
  Press `enter` to Continue an eligible chat or Inspect a read-only run, `n` to
  create a new chat with the launch workspace/mode/model defaults, or `esc` to
  quit without a session. Inspection `esc` returns to the startup inventory.

- **`mecatui debug TARGET [flags]`** — create a separate durable no-filesystem
  analysis session permanently bound to that stored target. `TARGET` may be an exact full opaque
  ID—including the exact ID printed when mecatui exits—or the displayed 12-column short handle:
  safe `[A-Za-z0-9._-]` bytes are literal except that a leading `-` becomes `%2D`; every other
  UTF-8 byte is uppercase `%HH`, and only complete atoms that fit are shown. The handle has no
  leading `#` marker. A syntactically valid short target consults the complete caller-visible
  inventory. Exact full-ID equality wins; otherwise one unique projected match resolves.
  Ambiguous projections create nothing and direct you to copy the full exact ID from `/session`
  and pass it as `TARGET` to the same command. If inventory fails or no handle matches, `TARGET`
  is sent unchanged and the server's ordinary exact-ID authorization/not-found path decides.
  The invocation is the consent gesture: diagnostic evidence may contain prompts, outputs, tool
  arguments/results, file paths, and secrets and will be sent to the selected model. It then
  submits a default diagnostic prompt automatically. The target is never resumed, leased,
  mutated, or used as the debugger's conversation.

- **`mecatui connect ADDRESS [sessions | debug TARGET] [flags]`** — always dial a running `mecated` at
  `ADDRESS` (host:port); **never probe** loopback and **never embed** — the
  target must already be serving. Embedded-server flags (`--mock`,
  `--trust-project`, provider keys, …) are **rejected** here — only shared
  session/UI flags and remote flags (`--auth-token`, `--anonymous`, `--tls`, …) apply.
  An explicit static token wins; `--anonymous` bypasses saved OIDC credentials; otherwise
  a saved enrollment is used. A clean missing enrollment attempts a credential-free dial
  and lets the server decide whether caller authentication is required. Registry corruption
  never falls back to anonymous. Remote credential-free connections retain verified TLS by
  default; `--tls=false` remains a separate explicit plaintext choice. Login remains
  persistent OIDC enrollment, never anonymous login.

  ```sh
  bin/mecated serve &                                 # server owns its configured root
  bin/mecatui connect 127.0.0.1:8080
  bin/mecatui connect 127.0.0.1:8080 sessions
  bin/mecatui connect 127.0.0.1:8080 debug SESSION_ID
  bin/mecatui connect mecated.internal:443 --tls --auth-token "$MECATL_AUTH_TOKEN"
  ```

  `--workspace` is rejected in every connect form; configure the server root on
  the server host.

- **`mecatui login ADDRESS`** — performs the remote server's public OIDC
  Authorization Code + PKCE login, then records target metadata and an encrypted,
  target-bound credential. It requires `--issuer`, `--client-id`, and `--audience`.
  It defaults to public issuer addresses trusted by the system roots; `--tls-ca` is
  optional there and REPLACES those roots. `--private-issuer` requires `--tls-ca` and
  admits private issuer addresses only. This CA verifies the issuer endpoints and is not
  the optional server CA supplied to `connect`. It exits without starting a session. `--no-browser` prints the
  authorization URL instead of opening a browser and then waits for the fixed
  `http://127.0.0.1:18473/oauth/callback` callback (headless/SSH use). For SSH, open
  that URL on the operator workstation and forward the fixed callback port to the host
  running `mecatui login`:

  ```sh
  ssh -N -L 18473:127.0.0.1:18473 user@login-host
  ```

  This is Authorization Code + PKCE, not device flow. `connect` does **not** implicitly
  open a browser: an unenrolled target returns guidance to run this command.

  A rejected callback reports a closed validation rule that failed — for example
  `callback state did not match the authorization request` — rather than echoing
  hostile callback values. Provider-returned OAuth `error` and `error_description`
  are the narrow exception: each is printable-subset filtered and bounded. No code,
  state, token, or callback path appears in either form.

  RFC 9207 `iss` follows section 2.4: a present `iss` must match the expected
  issuer, and an absent one is refused only when the authorization server's
  discovery document sets `authorization_response_iss_parameter_supported`. A
  provider that does not implement RFC 9207 therefore still works, while one that
  promised an `iss` cannot have it stripped. Wrong-state and other unauthenticated
  fixed-route probes are unlimited and do not burn state; the separate MCP OAuth
  random-path callback retains its bounded sixteen matching-route attempts.

- **`mecatui logout ADDRESS`** — removes the saved target and its target-bound
  credential without starting a session. The command is idempotent. It conditionally
  deletes credentials before metadata under a per-target transaction lock, so a
  concurrent token rotation retains the registry entry and reports an incomplete
  logout rather than making the credential unreachable. It releases the lock before
  spending one operation-wide fifteen-second provider budget on discovery and every
  best-effort RFC 7009 revocation attempt; an
  unavailable issuer does not block local removal, so provider-side termination is not
  guaranteed. Existing credential-only orphans cannot be pruned because the store has
  no enumeration operation.

- **`mecatui llm login [--skip-browser]`** — runs the separate ToolHive LLM gateway
  OIDC flow and exits without connecting to `mecated`. `--skip-browser` prints its
  authorization URL and waits for the callback. It is not remote-server login.

### OIDC-connected server

`mecatui login ADDRESS` is the enrollment path for a remote `mecated`/`mecak8s`
caller-identity deployment. The issuer, public client, audience, redirect URI, and
scopes are bound to the canonical `host:port` target. Login validates discovery,
PKCE, and the resulting token. A public issuer uses system trust roots; private HTTPS
requires an explicit issuer CA bundle path. The registry saves an explicit path/reference
only—not CA contents—for issuer discovery, token, JWKS, refresh, and revocation;
`connect --tls-ca` independently verifies the gRPC server. The connection registry
contains public metadata only; credentials are encrypted on disk using a canonical-
root-scoped key held by the OS keyring. Under a root lock, an old unsuffixed keyring key
is copied only when the encrypted namespace contains an actual credential record; merely
opening an empty namespace does not trigger migration. Legacy credentials enrolled with a zero-padded target port need
one login after upgrade because target canonicalization changes their credential key.

```sh
# A local port-forward is loopback, so it is the one plaintext bearer exception.
kubectl port-forward -n mecatl service/mecak8s-agent 8080:8080 &
export MECATL_AUTH_TOKEN="$(your-oidc-cli print-access-token)"
bin/mecatui connect 127.0.0.1:8080 --auth-token "$MECATL_AUTH_TOKEN"
```

For a non-loopback endpoint, omitted `--tls` verifies TLS automatically; `--tls`
and `--tls-ca` also select verified TLS. `--tls=false` is the explicit plaintext
downgrade, and a bearer is refused on that remote plaintext transport. Use
`connect --tls-ca` when the server uses a private CA. The remote server owns
placement: mecatui sends no local cwd, and `--workspace` is rejected in every connect
form rather than being treated as a path inside an agent pod. See [Security & transport](usage/mecated.md#security--transport-auth-tls-rate-limiting) for the attribution model and its non-tenancy limits.

On later `connect`, a saved target supplies a managed dynamic bearer source: each RPC
asks for a currently validated access token. Application token demand, rather than RPC
success, gates proactive refresh, and refresh/enrollment/logout share one per-target
interprocess transaction with CAS-persisted rotation. Only an exact structured OAuth
`invalid_grant` code deletes a rejected credential; matching provider prose does not.
A static `--auth-token` is unmanaged and is never obtained or refreshed by mecatui.
Tokens do not enter UI state, logs, or command arguments. If authentication fails, the
recoverable `/connect` overlay names whether the target is unenrolled, the session
expired, the local credential is unusable, cleanup should be retried, or the server
rejected the bearer. It never opens a browser itself. A rejected bearer requires
issuer/audience/CA remediation rather than another login; cleanup retries without a
browser. After same-target re-auth, mecatui asks the server's ownership-enforced
session/transcript boundary to prove the new caller owns the prior session. Only a
safe terminal turn boundary is resumed; a missing, mismatched, active, awaiting, or
ambiguous candidate starts a fresh session and no in-flight prompt is replayed. The
closed recovery action preserves that candidate and the current server CA path only for
the same target; neither crosses a target switch. `/connect` lists saved
targets and requires confirmation. Selecting one restarts into a new remote session;
selecting **Sign in to a new target** returns to the CLI login flow first. No session
or conversation crosses a target switch.

The Kind remote flow is available after fixture setup with the documented host
aliases and public CA. It is a live qualification path, not part of ordinary
offline `task test` coverage; see `deploy/mecak8s-vmcp/README.md`.

`ADDRESS` must immediately follow `connect`; a missing or flag-first `ADDRESS` is
a usage error, with one carve-out: `mecatui connect --help` renders the connect
help instead of the missing-ADDRESS usage error. Put `sessions` after the address
for the remote startup browser, or `debug SESSION_ID` for a remote dedicated debugger:
`mecatui connect ADDRESS sessions [flags]` and
`mecatui connect ADDRESS debug SESSION_ID [flags]`.
An unknown leading command fails closed.

The startup browser never creates a throwaway session. It completes the model-list
reconcile before enabling `n`, so a removed saved provider cannot race a new-chat
create. Empty inventories, list failures, and unavailable transcript loading still
leave `n` and `esc` available. `-p`/`--prompt`, `--prompt-file`, `--resume`, and
`--resume-latest` conflict with either `sessions` launch form and are rejected with
a usage error; choose one startup intent explicitly.

> **Removed flags:** the three `--subagent-ask-reviewer*` flags were inert under
> `mecatui` (it runs interactive — a child ask surfaces to the approval modal, not
> the headless reviewer) and have been removed. They are now unknown-flag errors.
> To use the headless ask reviewer, run a headless `mecated serve --headless
> --subagent-ask-reviewer …` and point `mecatui connect` at it. The `--model-slot
> ask-reviewer=…` model slot is unaffected.

### Debug a stored session

```sh
mecatui debug 01JOPAQUETARG
mecatui connect 127.0.0.1:8080 debug 01JOPAQUETARG
mecatui debug 01JOPAQUETARGET
mecatui connect 127.0.0.1:8080 debug 01JOPAQUETARGET
mecatui connect 127.0.0.1:8080 debug 01JOPAQUETARG --debug-mcp github
```

`--debug-mcp NAME` is repeatable and selects only already-configured server-global
streaming-HTTP MCP servers. The debugger may draft a GitHub-like issue without calling the
server. After reviewing it, send a new current prompt such as `Publish this issue now`;
each mutating call opens an approval card even under yolo/configured allow. Allow once sends
one request. A later mutation asks again; Allow always is deliberately not learned.

These commands accept either an exact full opaque session ID or the displayed 12-column short
handle as `TARGET`. The handle renders safe `[A-Za-z0-9._-]` bytes literally except that a leading
`-` becomes `%2D`; every other UTF-8 byte is an uppercase `%HH` atom, and rendering stops before an
atom that would exceed 12 ASCII columns. The displayed literal has no leading `#` and is itself the
debug argument. A syntactically valid short target consults the complete caller-visible inventory.
Exact full-ID equality wins; otherwise one unique projected match resolves. On ambiguity, open
`/session`, copy the exact full ID, and pass it as `TARGET` through the same command. If inventory
lookup fails or no projection matches, mecatui sends `TARGET` unchanged and reports the ordinary
server exact-ID authorization/not-found result. These commands do not attach to or continue the
target.
They authorize it, create a separate durable no-filesystem debug session, print a privacy
disclosure, and submit one first genuine user turn. That turn is ordered as the diagnosis
objective, the required status/transcript/pagination workflow, the expected report sections,
and finally the same sanitized current-client/server report produced by bare `/diagnostics`.
The report is clearly delimited debugger runtime context, never target evidence. A custom
`--prompt` replaces only the objective; the runtime block remains. Remote report lookup uses
the authenticated server-info path, and a safely classified failure leaves unavailable
fields without blocking diagnosis or exposing the raw error. The disclosure is load-bearing: target prompts, model output,
tool arguments/results, paths, and secrets can be sent to the selected model. Running the
command is the consent gesture.

The debug model always has `InspectSession`, permanently bound by the server to
the command's target. It can request bounded `status`, `transcript`, `activity`,
`performance`, `network`, `related`, `delegation`, `history`, and `manifest` views, but
cannot supply or change the target ID. `related` returns opaque handles for currently
retained descendants admitted by the server's ownership posture and evidence-backed
incarnation-specific tombstone/status rows; use a returned
handle to inspect a child transcript. `history` separates current, compaction-archive, and
event-reconstructed histories. Status names latest-run counters, cumulative snapshot usage,
and lifetime EventLog counters separately. Snapshot
transcript is the authoritative history. Activity, performance, and network depend on EventLog
availability and report scan/page completeness explicitly. Network contains only sanitized
failed/interesting resilience-attempt decisions and classifications; no raw error, URL,
header, body, prompt, tool argument, or credential is retained. Successful-attempt timing and
per-phase DNS/TCP/TLS timing are not measured. All returned evidence is fenced as untrusted. The target is
never resumed, reopened, recovered, leased, mutated, approved, cancelled, or steered by
the debugger.

The debug conversation persists independently and can rehydrate after server restart with
the same exact lineage and narrow catalog. Invalid lineage/no-fs metadata, a missing debug
factory, or an unavailable target fails closed. The ordinary padded header places amber/bold
`DEBUG target <handle>` immediately after `mecatui` in every phase. At narrow widths it
sheds model/mode/server detail before that complete target identity rather than clipping it;
`/session` displays the safely quoted exact target ID and copies it with `t`. The
`DEBUG <handle>` terminal title uses the same handle. The TUI hides `/clear`, `/sessions`, `/models`,
`/effort`, and `/worktrees`, and blocks the mode/effort shortcuts because those controls
can replace the launch binding. Schedule and learning controls remain available because
changing those independent settings does not rebind the debug target; harmless inspection
and presentation controls remain too.

### Continue a chat at startup

`--resume SESSION_ID` adopts an existing owned main chat before Bubble Tea starts.
`--resume-latest` instead chooses the newest eligible owned main chat whose authoritative
snapshot transcript is available; when none exists, it starts a fresh session instead of
failing — the "continue where I left off, otherwise begin" launch. Both flags work with
the embedded server and with `mecatui connect`, and they are mutually exclusive:

```sh
mecatui --resume 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 --resume-latest
```

Adoption does not create a temporary session: mecatui loads and displays the stored
transcript, placement label, mode, model, and capabilities, then targets the same opaque ID.
`--mode` describes only a newly created session; an adopted chat keeps its stored values. Scheduled runs, child runs, unknown legacy rows, chats
awaiting approval, active chats, and rows without a complete authoritative transcript
are not eligible. Exact `--resume` reports why its row cannot be continued; `--resume-latest`
skips ineligible or unreadable rows and tries the next one. A genuine inventory-list
failure still surfaces rather than being masked as "start new".

The read-only startup lookup does not reopen, recover, abandon, or acquire a lease.
Those checks remain atomic at the ordinary run-entry funnel when the first new prompt
is sent. If that attachment fails, the stored transcript remains visible and no
fallback session is created: press `r` to retry the preserved prompt or `esc` to go
Back and edit it. Combining a resume selector with `--prompt` or `--prompt-file`
adopts the transcript first and then submits the seed exactly once as the next turn.

On an ordinary clean exit, after the terminal has left the alternate screen and cleanup
has completed, mecatui writes exactly one handoff line to **stderr**:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
```

The value after `=` is a JSON string, not display text: a JSON decoder recovers the
byte-exact valid-UTF-8 ID even when it contains spaces, quotes, or line separators. The ID
is the final active chat after any startup continuation, `/sessions` continuation, model
carryover, effort fork, or worktree switch—not necessarily the ID created at startup. The
line is absent when no session was established, startup or the TUI failed, or a signal
interrupted/forced exit. stdout is unchanged. This makes the normal workflow: copy the ID
inside `/session` with `c` while the TUI is open, or retain this stderr line and pass its
decoded value to `--resume` later.

### Seeding an initial prompt

`-p`/`--prompt` (or `--prompt-file` for a longer body) launches the session with
a seed prompt auto-submitted as the FIRST turn — the equivalent of typing the
prompt and pressing enter the moment the session is ready. The TUI then stays
interactive for follow-ups; this is NOT a print-and-exit one-shot.

```sh
mecatui -p "Summarize the failing tests in this repo"
```

`--prompt-file` reads a file at startup (fail-fast on an unreadable path) and is
joined AFTER the `--prompt` literal, separated by a blank line, so you can combine
a short directive with a longer brief. The seed fires ONCE: a `/models` restart or
`/clear` rebinds the session but never re-submits the seed. A `/`-prefixed seed
(e.g. `-p /clear`) is intercepted by the input's built-in slash-command handler.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--workspace` | cwd | **embedded only:** trusted operator configuration for the hosted server's default root; never sent in CreateSession and rejected by every `connect` form |
| `--mode` | `default` | permission posture for a new session: `default` \| `plan` \| `accept-edits`; an adopted chat keeps its stored mode |
| `--resume` | – | continue the owned main chat with this exact opaque session ID; loads its authoritative transcript without creating a throwaway session; mutually exclusive with `--resume-latest` |
| `--resume-latest` | off | continue the newest eligible owned main chat with an available authoritative transcript; excludes active, awaiting, scheduled, child, and unknown sessions; when none is eligible, start a new chat; mutually exclusive with `--resume` |
| `-p` / `--prompt` | – | seed prompt auto-submitted once the first session is ready (the CLI task to launch with). The TUI stays interactive for follow-ups; this is NOT a one-shot. Both `--prompt` and `--prompt-file` may be given (literal first, joined by a blank line). Fires ONCE — a `/models` restart or `/clear` never re-submits it |
| `--prompt-file` | – | path to a file whose contents are the seed prompt body. Read at startup (fail-fast on unreadable). Joined after `--prompt` when both are given. Same once-only semantics as `--prompt` |
| `--theme` | `aztec` | theme name (also `MECATUI_THEME`); giving either pins the theme and disables the light/dark auto-detect below |
| `--theme-dir` | – | extra directory of `*.json` themes to load |
| `--auth-token` | – | bearer token for an **external** server (or `MECATL_AUTH_TOKEN`) |
| `--tls` | target-aware | verified TLS for an external server when given; omitted selects verified TLS for non-loopback/unparseable targets and plaintext for loopback; `--tls=false` explicitly permits remote plaintext |
| `--tls-ca` | – | path to a PEM CA bundle for verified external-server TLS (also implies TLS) |
| `--insecure` | off | encrypted TLS without certificate verification (controlled testing only; implies TLS; a bearer is refused over it for a non-loopback target) |
| `--list-themes` | – | print available themes and exit |
| `--version` | – | print the build identity and exit before normal startup |
| `--inline` / `--no-alt-screen` | off | render inline in the terminal's normal buffer instead of the alternate screen, preserving native scrollback/search (no mouse capture; see `--no-mouse` below) |
| `--no-mouse` | off | keep the alt screen but disable mouse capture and in-app mouse gestures, preserving the terminal's **native** click-drag selection; keyboard prompt selection still works (or `MECATUI_NO_MOUSE=1`; see the selection section) |
| `--terminal-title` | `on` | dynamic terminal window/tab title: `on` shows `<session title> <handle> — <status word> mecatui` (the title is the first prompt, the fixed handle identifies the session, and the status word reflects the phase); `off` collapses to the bare `mecatui` (escape hatch for terminals/multiplexers where a set title does more harm than good). Accepts `on`/`off`/`true`/`false`/`1`/`0` (or `MECATUI_NO_TERMINAL_TITLE=1`; see the terminal title section) |
| `--no-banner` | off | disable the first-run welcome **splash** (mascot + gradient wordmark); the plain prompt hint + affordance list still show. Auto-forced on under `--quiet` or a non-interactive stdin |
| `--model` | – (provider default) | model id for the **embedded** server; empty = the server-configured `--default-model` (when set), else the provider-appropriate built-in (anthropic → `claude-sonnet-4-6`, openai → `gpt-5`, openrouter → `openai/gpt-5`; openai-codex → first entitled live model). Overridden per session by the `/models` picker |
| `--default-provider` | – | **embedded** server: deployment-wide default provider id (e.g. `openai`, `openrouter`, `anthropic`, experimental `openai-codex`); overrides automatic preference for zero-selector sessions, while a client-side selection still wins. An unknown/unavailable provider **fails startup** |
| `--default-model` | – | **embedded** server: deployment-wide default model for the default provider; sits below client-side defaults and above the per-provider built-in. A model not catalogued for the default provider **fails startup** |
| `--context-window-override` | `0` | **embedded** server: global context-window token override for both compaction and the footer denominator. `0` keeps exact operator `models.context_windows` → live metadata → models.dev catalog → 128K fallback resolution. Rejected in `connect` mode; configure the external `mecated` instead |
| `--subagent-model` | – (inherits `--model`) | **embedded** server: global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model (the `CLAUDE_CODE_SUBAGENT_MODEL` analogue); the Parallel judge stays on the session model. Same provider as the session; an unresolvable id **fails startup** |
| `--anthropic-base-url` | – | native Anthropic API base URL override for the **embedded** server (compatible/proxy endpoints; key from `ANTHROPIC_API_KEY`) |
| `--openai-base-url` | – | OpenAI base URL override for the **embedded** server |
| `--openrouter-base-url` | – | OpenRouter base URL override for the **embedded** server (default `https://openrouter.ai/api/v1`) |
| `--auth-file` | – (auto) | **embedded** server: path to the YAML credentials file (`providers.<name>.api_key`, or the experimental `providers.openai-codex.oauth` snapshot); overrides `$XDG_CONFIG_HOME/mecatl/auth.yaml`. Environment wins for API-key providers; Codex has no env alias. See [the exact schema](usage/mecated.md#credentials-file-authyaml) |
| `--mock` | off | **embedded** server: use the offline mock provider (no network) |
| `--no-bash` | off | **embedded** server: disable the Bash tool (shell-less) |
| `--memory-dir` | – (auto) | **embedded** server: per-project memory store dir; empty = a default under `$XDG_DATA_HOME/mecatui/memory` |
| `--no-memory` | off | **embedded** server: disable cross-session memory (Remember/Recall) |
| `--store-dir` | – (auto) | **embedded** server: durable JSONL session/event store dir; empty = a per-workspace default under `$XDG_STATE_HOME/mecatui/sessions`, so sessions survive restart. **Privacy:** stores the raw conversation (prompts, model output, tool args/results) in **plaintext**; the dir is created mode `0700` (owner-only) |
| `--no-store` | off | **embedded** server: disable the durable session store (use an in-memory store, persisting nothing to disk) |
| `--child-retention` / `--child-retention-max-per-family` | `168h` / `500` | **embedded only:** local child age/count policy; `0` disables each limit |
| `--main-retention` / `--main-retention-max-total` | `0` / `0` | **embedded only:** destructive local main policy, off by default; enabling either also requires explicit acknowledgement |
| `--schedule-fire-retention` / `--schedule-fire-retention-max-total` | `168h` / `0` | **embedded only:** local scheduled-fire age/count policy; `0` disables each limit |
| `--retention-sweep-cadence` | `1h` | **embedded only:** local repeat cadence; `0` disables repeats while preserving the compatibility startup sweep |
| `--acknowledge-main-retention` | off | **embedded only:** explicit consent after reviewing the logged destructive main planner summary |
| `--commands-dir` | – (auto) | **embedded** server: slash-command template dir; empty = the conventional `.mecatl/commands`, `.claude/commands` |
| `--no-commands` | off | **embedded** server: disable slash-command expansion |
| `--skills-dir` | – (auto) | **embedded** server: skill-unit dir (`<name>/SKILL.md`); empty = the conventional dirs (e.g. `.claude/skills`) |
| `--no-skills` | off | **embedded** server: disable skill discovery (the Skill tool) |
| `--soul-file` | – (auto) | **embedded** server: user-scoped, agent-READ-ONLY persona/soul file injected as turn-0 context; empty = the conventional `~/.config/mecatl/soul.md` (fail-soft if absent) |
| `--no-soul` | off | **embedded** server: disable the user-scoped persona/soul fragment |
| `--approve-soul` | off | **embedded** server: (re)write the soul **drift baseline** (`<soul-path>.sha256`) to the current soul's hash, accepting the file as-is |
| `--soul-strict` | off | **embedded** server: refuse a **drifted** soul — contribute no soul fragment when its hash differs from the baseline (default is warn-and-load); pair with `--approve-soul` to accept an edit |
| `--user-model-dir` | – (auto) | **embedded** server: dir for the cross-project user-model store (RememberUser/RecallUser/SearchUserModel + the turn-0 `<user-model>` block); empty = the conventional `~/.config/mecatl/usermodel` |
| `--no-user-model` | off | **embedded** server: disable the user model entirely (tools + turn-0 block) |
| `--user-model-review` | off | **embedded** server: deprecated alias for `learning.mode: auto`; eligible completions are reflected into staged proposals, then only conservative policy-eligible facts are promoted (spends tokens, hence off) |
| `--user-model-review-interval` | 1 | **embedded** server: session-count debounce for `--user-model-review` (1 = every session) |
| `--trust-project` | off | **embedded** server: honour a discovered project's permission **ALLOW** rules **and** its project soul (`.mecatl/soul.md`). Default OFF, unified with `mecated` — deny/ask are always honoured regardless. Only pass it for a repo you trust |
| `--yolo` | off | **embedded** server: OPERATOR POSTURE (dangerous) — suppress permission prompts for the built-in mutate-ask floor, for ephemeral/sandboxed use only. A configured deny/ask in any scope still applies. Refused as root unless `MECATL_SANDBOX=1` (or `IS_SANDBOX=1`) |
| `--quiet` | off | discard the embedded server's operational diagnostics instead of writing them to `$XDG_STATE_HOME/mecatl/mecatui.log` (see the diagnostics note below) |
| `--perf` | off | **embedded** server: expose the sensitive admin surface (`/metrics`, `/debug/pprof`, `/debug/vars`, `/debug/flightrecorder`) and wire domain metrics. Empty `--perf-addr` uses the instance's owner-private UNIX `admin.sock`. UNAUTHENTICATED |
| `--perf-addr` | – | **embedded** server: explicit TCP address for `--perf`; only loopback is accepted. Empty uses the private per-instance UNIX socket, except `--perf-mcp` uses ephemeral `127.0.0.1` TCP because streaming HTTP needs a URL. `127.0.0.1:0` explicitly requests ephemeral TCP |
| `--perf-goroutine-warn-threshold` | 0 (off) | **embedded** server: arm the live goroutine-leak watchdog — Warn whenever the goroutine count exceeds this; the `/metrics` goroutine series is exported regardless. Only consulted with `--perf` |
| `--perf-mcp` | off | **embedded** server: mount the read-only streaming-HTTP perf MCP server at `/mcp`. With no `--perf-addr`, the resolved ephemeral loopback URL is logged. No stdio MCP; raw admin data is not injected into a debug/model session |

The embedded server has no auth/TLS — it is a private, user-owned UNIX socket
(the same single-user loopback trust model `mecated` uses for `127.0.0.1`, with a
tighter blast radius). The `--auth-token` / `--tls*` flags apply only in `connect` mode; loopback is
unauthenticated plaintext by default, matching mecated's trust model. The embedded server discovers **ToolHive-managed MCP
servers** by default (the `default` group, with their resource/prompt meta-tools) —
fail-soft: with no Podman/Docker runtime reachable it degrades to zero servers, so
it's inert on a machine without ToolHive workloads. The remaining heavier opt-ins —
static MCP server endpoints and the writable SkillDraft quarantine — stay **off**;
for those, run a full `mecated serve` and `connect` to it.

**Embedded-server diagnostics go to a file, never stderr.** When mecatui hosts the
embedded server, its operational diagnostics (and the `--perf` surface's log lines)
are written to `$XDG_STATE_HOME/mecatl/mecatui.log` (fallback
`~/.local/state/mecatl/mecatui.log`) — a stderr line would corrupt the Bubble Tea
alt-screen. `--quiet` discards them instead. A client-only run (`mecatui connect`) logs
nothing of its own.

The file's floor is `info`. ALL FIVE paths that end a turn TERMINALLY — a permanent
non-retryable provider error, a mid-stream stream failure (or cancellation), a
**mid-stream idle stall** (the "thinking, then nothing" shape: no chunk for
`--llm-stream-idle-timeout`), a rejection by the open circuit breaker, and exhausted
establishment attempts — log at `info` (issue
#319), each carrying the `model` so a busy daemon's lines can be told apart, so a
died-mid-turn diagnosis needs nothing beyond this file. The resilience
layer's retry-counting detail sits at `debug` and is therefore not written — and there is
currently no flag, env, or config key that lowers the floor to reach it; it answers
a different question anyway (a slow or retrying provider, not a dead turn).

### Shutdown

Exiting mecatui (double Ctrl+C on an empty prompt, or an OS `SIGINT`/`SIGTERM`)
runs a **bounded** graceful shutdown — it cannot hang indefinitely on an in-flight
scheduled fire, a stuck MCP server, or a wedged gRPC stream (issue #388). The first
signal quits Bubble Tea and starts cleanup; a **second** signal during cleanup forces
an immediate hard exit (`os.Exit(130)`). Signal-driven exits print no final-session handoff.
After an ordinary clean keyboard exit, mecatui first restores the normal screen and finishes
cleanup, then emits the JSON-safe `mecatui: final-session-id=<JSON string>` line documented
under [Continue a chat at startup](#continue-a-chat-at-startup).

Cleanup is bounded at each layer (mirroring the `mecak8s` bounded-shutdown
precedent), worst case ≈ 45s:

| Step | Bound |
|---|---|
| embedded gRPC `GracefulStop` → hard `Stop()` fallback | 30s |
| composition teardown (scheduler stop, service close, MCP) | 10s |
| scheduler leadership/epoch joins | 5s each |
| per-session engine close | 10s |
| MCP manager close | 5s |
| whole post-quit cleanup (the hard outer cap) | 45s |

A scheduled fire in progress is **cancelled**, and its session snapshot is persisted
as `cancelled` — recoverable on the next run via the normal session-resume path
(`Interrupt`), never left permanently `running`/`pending`. A fire parked on a
permission ask keeps its durable awaiting snapshot, so it stays resumable
cross-process. No goroutine, session/scheduler lease, socket, or MCP connection is
left owned after the bounded shutdown completes.

**Environment variables.** A handful of envs tune the client beyond the flags above
(most have a flag equivalent in the table; the rendering-capability pairs are env-only):

| Env | Effect |
|---|---|
| `MECATUI_THEME` | theme name (same as `--theme`) |
| `MECATUI_NO_MOUSE` | disable mouse capture and in-app mouse gestures (same as `--no-mouse`) while preserving native terminal selection; keyboard prompt selection still works |
| `MECATUI_NO_TERMINAL_TITLE` | suppress the dynamic terminal window/tab title (same as `--terminal-title=off`) — collapse to the bare `mecatui` |
| `MECATUI_DEBUG_MOUSE` | overlay raw mouse coords / click-mapping in the footer during a press/drag (troubleshooting) |
| `MECATUI_FORCE_EMOJI` / `MECATUI_NO_EMOJI` | force / suppress the emoji glyph for the YOLO posture badge (force-on, no-wins-over-force); default is conservative env-based detection (see the posture badge) |
| `MECATUI_FORCE_KITTY` / `MECATUI_NO_KITTY` | force / suppress the Kitty-graphics mascot on the welcome splash (force-on, no-wins-over-force); default is conservative env-based detection, falling back to the always-correct half-block mascot |

**Dynamic terminal window/tab title.** `mecatui` sets the terminal window/tab title
to `<session title> <handle> — <status word> mecatui`, so you can tell sessions apart
in a tab bar. The title is the **first genuine prompt** of the session (clamped to
~40 runes); `<handle>` is the fixed terminal-safe session handle; the status word
reflects the TUI phase:

| Phase | Title |
|---|---|
| running | `<title> <handle> — Working mecatui` |
| awaiting approval | `<title> <handle> — ⚠ mecatui` |
| connecting | `<title> <handle> — Connecting mecatui` |
| fatal | `<title> <handle> — ✗ mecatui` |
| idle / replay (title known) | `<title> <handle> — mecatui` |
| no title yet (session known) | `<handle> — mecatui` |
| no session yet | `mecatui` |

The title leads because tab bars **truncate from the right**; the status is a
**static word, never an animated spinner** (per-frame title churn trips OS
attention heuristics — the dock bounces / the taskbar flashes on every change).
The title self-heals across a session switch / fork / carryover (a refetch adopts
the server's stored title when this client never saw the first prompt). A dedicated
debugger instead always starts with `DEBUG <handle>`, followed by its static
phase label; the persistent amber/bold `DEBUG target <handle>` segment in the ordinary
padded header carries the same identity through every lifecycle and fatal state.

The title is terminal-escape-sanitized (C0/ESC/DEL stripped — a malicious prompt
can't embed an OSC title-injection), and newlines/tabs collapse to single spaces
(a window title is one line).

Pass `--terminal-title=off` (or `MECATUI_NO_TERMINAL_TITLE=1`) to suppress it and
leave the title at the bare `mecatui` — the escape hatch for
terminals/multiplexers where a set title does more harm than good. **tmux note:**
by default tmux's `automatic-rename` overrides pane titles; to let `mecatui`'s
title survive, set `set -g automatic-rename off` (or `set -g allow-set-title on`)
in your `~/.tmux.conf`.

**Skill discovery is ON by default**, via conventional discovery (the read-only
`Skill` tool activates progressive-disclosure `<name>/SKILL.md` units from the
conventional dirs, e.g. `.claude/skills`, when present) — consistent with
agent-definition discovery. Skills register only when at least one `SKILL.md` is
found (opt-in by presence), so with none, `caps.Skills` is false and the `?`
overlay reflects that. `/skills` opens the combined inventory panel: external skills show
name/description from the live `ListSkills` generation, while learned skills also show owner and
lifecycle state. Active learned entries are labelled `agent-owned` with owner and active version;
external skills remain unlabelled and retain precedence. `up`/`down` selects a learned skill and
`enter` opens bounded body, evidence/evaluation, receipt history, and a `v` version diff; `a` activates, `x` rejects,
`d` archives, and `r` rolls back through server-side expected-revision CAS. A stale action stays in
the detail with a refresh hint. Procedure linkage is also shown in `/reflections`; changes surface
non-modally and never force-open either overlay. The panel opens with a **type-to-filter** input focused: type to
narrow the external live list by a case-insensitive substring match over each skill's **name**
and **description**. `esc`
is two-stage: a non-empty filter is cleared first (panel stays open); a second
`esc` closes the panel. A long inventory is **scrollable** the same way `/soul` is:
`pgup`/`pgdn` (and `up`/`down`, `home`/`end`) move a fixed line-window over the
rows, with a "lines X–Y of N" indicator when the inventory overflows; the
`/agents` definition inventory scrolls identically. (Note: `j`/`k` type into
the filter — they are not intercepted as navigation, unlike the read-only
overlays; see `/models`.) Activation stays the model's
call (the `Skill` tool reads the body on demand); the panel is discovery only. Pass `--no-skills` to disable discovery entirely, or
`--skills-dir` to scope it to a single vetted directory. Note the trust boundary:
a skill auto-activates from its always-in-context metadata, so a `SKILL.md` in a
workspace you didn't author (e.g. a cloned repo's `.claude/skills`) can steer the
model — use `--no-skills` for untrusted workspaces. Only read-only discovery is
wired; the writable SkillDraft self-improvement loop stays off.

**Project trust posture (incl. the project soul).** The embedded server does **not**
blanket-trust the workspace you launch it in. `--trust-project` is **default OFF**,
unified with `mecated` (WORKSPACE-TRUST Phase 0). So, by default, a repo's
`.mecatl/settings.yaml` permission **ALLOW** rules are ignored and a project
persona/soul at `<workspace>/.mecatl/soul.md` (issue #14, Phase 3) is **not** loaded;
the repo's deny/ask permission rules are always honoured regardless, and your
user-scoped soul (`~/.config/mecatl/soul.md`) always loads and takes precedence.
Pass `--trust-project` for a repo you trust to honour its ALLOW rules and project
soul — exactly the gesture `mecated` requires. (Earlier builds hardcoded trust ON
for the TUI; that blanket-trust regression is gone.)

**First-encounter trust prompt (WORKSPACE-TRUST Phase 2c).** Rather than silently
ignoring an untrusted repo, the embedded server asks you once. Before the TUI takes
over the screen (a **pre-alt-screen** stderr prompt, alongside the other startup
notices), if the workspace is **not already trusted** and carries a **project
authority set** worth gating — a project soul, project-tier agents/commands/skills,
or a `settings.yaml` with **ALLOW** rules — `mecatui` prompts:

```
mecatui: do you trust the project files in this workspace?
  /home/me/src/some-cloned-repo
[t]rust (persist) / [o]nce (this run only) / [n]o (default):
```

- **`t`** trusts this run and **remembers** it (writes the workspace + its current
  identity-anchor hash to `~/.config/mecatl/trust.yaml`, so it stays trusted until
  the project's identity surface drifts).
- **`o`** trusts this run only (nothing persisted).
- **`n` / Enter / anything else** declines — the safe default (project authority
  withheld; the agent still runs with your user-tier config + built-in tools).

If you had trusted the repo before and its **soul / agents / commands / skills
changed since**, the prompt **re-fires** as a drift re-prompt ("this workspace
**CHANGED** since you trusted it"). A repo with nothing to gate (no project
authority, or only deny/ask rules) is **never** prompted, and an already-trusted
repo (`--trust-project`, `trustedWorkspaces:`, or a remembered + undrifted entry) is
**not** re-asked. If stdin is **not a terminal** (piped/headless), `mecatui` cannot
prompt — it proceeds **untrusted** for that run and never blocks. The echoed path is
terminal-escape-sanitized (CWE-150). The prompt lives in the `mecatui` composition
root, not the render layer — no proto event, no change to `ui`/`theme`/`client`. See
`docs/usage.md` for the full semantics; `mecated` itself never prompts (declarative).

**Built-in client-side slash commands always appear.** Typing `/` opens the
palette with a set of commands the TUI itself ships — independent of workspace
dirs and even when server slash-command expansion is off. `/clear` calls the
server's `ClearSession` successor RPC: it creates a distinct empty-history session
that inherits the source's exact placement, effective model, reasoning effort, mode,
limits, and owner. The source remains intact, and conversation/scrollback switch only
after the successor and transcript load succeed. `/help` opens the keys-&-features overlay.
`/diagnostics` is an exception to the local-only commands: the exact
whitespace-trimmed lower-case bare command follows ordinary built-in dispatch,
generates a concise sanitized report, and submits that report through the normal
model-facing prompt path. It includes only the runtime platform, client build,
server mode, server build, server implementation, and already-held
provider/model/permission-mode state, the sanitized diagnostic display projection of the current remote connection target when locally known, and the sanitized diagnostic display projection for the already-held active provider when supplied by `GetServerInfo`. Neither endpoint value is connection configuration or an instruction to reconnect. Each retains only URL scheme, host, optional port, and escaped clean path; userinfo, query, fragment, controls, TLS/auth settings, and arbitrary server configuration are excluded. Embedded UNIX-socket endpoints report unavailable unless an actual local URL endpoint is already known without lookup. In remote mode it makes one authenticated
`GetServerInfo` call; its lookup status is only `ok`, `not-supported`,
`unreachable`, or `invalid-response`. It never includes raw errors, connection or
authentication details, credentials, workspace paths, session content, or server
configuration/state. In embedded mode it uses the locally known server identity
and makes no RPC.
These commands are *always* available because they are client-owned commands (with
`/clear` using the dedicated `ClearSession` RPC); `/compact` (force one server-side
history compaction pass), `/mcp` (browse
the MCP inventory), `/agents` (browse the agent-definition inventory — the
resolved registry the `Subagent` tool routes delegations to), `/team` (the unified
agents overlay pinned to the Teams tab — same surface as `ctrl+a`, which picks a
context-sensitive default tab), `/skills` (browse the skills inventory),
`/soul` (inspect the persona — read-only), `/usermodel` (inspect the user
model and its proposal linkage — read-only), `/reflections` (review bounded pending/recent
learning proposals), `/reflect` (explicitly reflect the current completed session), `/dream`
(manually review project-memory or user-model consolidation),
`/models` (pick the model for the next session), `/worktrees`
(switch to a sibling git worktree), and `/schedule` (browse & manage scheduled
tasks) appear
only when the connected server advertises those capabilities (and, for
`/compact`/`/mcp`/`/agents`/`/skills`/`/soul`/`/usermodel`/`/reflections`/`/reflect`/`/dream`/`/models`/`/worktrees`/`/schedule`, the matching client
collaborator is wired). The fixed palette order starts
`clear, help, session, retry, diagnostics, compact`, then the available inventory, model,
workspace, schedule, and operator-setting commands (locked by a test).
`/learning` is local embedded-server operator-settings UX: each invocation selects the
next Off→Review→Auto value in `$XDG_CONFIG_HOME/mecatl/settings.yaml`, preserving
unrelated YAML and comments. `/learning-sensitivity` independently cycles
Conservative→Balanced→Eager. Both report the complete pending mode+sensitivity and that a
restart is required; neither provides a live mutation API. These labels describe
completed-trajectory observation only: Off disables automatic reflection, Review stages
bounded evidence-backed proposals for operator approval, and Auto additionally promotes only
standard-policy-eligible, non-conflicting facts. Explicit `/reflect` remains available in Off when
the server has a configured reflection provider/repository.
They do not control a separately configured `--user-model-consolidate-interval`; that
process-wide schedule remains operator-authorized even when a project lowers the effective
mode to Off. In `mecatui connect` mode, `/learning` and `/learning-sensitivity` are
read-only: they never mutate the client's local settings file and tell the operator to edit
the mode or sensitivity on the remote server host and restart that server.
`/agents` and `/team` are distinct: `/agents` is the **definition inventory** (a
palette-only `ListAgents` snapshot, gated on `caps.agents`), while `/team` opens
the **live overlay** of a team that has actually run (gated on `caps.teams`).
These never reach the model: a bare built-in line is intercepted locally even
while a run is streaming, although commands such as `/compact` then enforce their
own idle-only boundary. `/diagnostics` is the exception: its bare form submits the
generated sanitized report through the ordinary model-facing prompt path. It takes
no arguments: `/diagnostics` followed by any text or newline remains in the input,
is not sent, and shows a local argument warning. Gated-off builtins are hidden from
the palette and help overlay; typing one anyway blocks the send with a warning.
Unknown slash commands remain model-facing input so workspace commands keep their
server-side expansion. Recognized built-ins with arguments retain the input, are not
sent, and show a local argument warning; `/compact` follows the same rule.

**`/compact` (manual model-history compaction).** This built-in appears in the
palette and help only when `ServerCapabilities.manual_compaction` is true and the
client RPC collaborator is wired. Type the bare command with no arguments while the
session is idle. Mecatui blocks prompt submission until the unary request finishes,
so a new run cannot overtake the rewrite. The server also serializes the operation
against run entry and any configured session lease.

The operation runs the configured compactor once without waiting for the automatic
0.8 trigger. It sends no chat prompt, creates no model turn, and keeps the visible
scrollback. A changed response appends `Model history compacted.` as a scrollback
notice; a successful no-op appends `Model history is already compact.` Existing
cards are not removed because scrollback is the user's transcript, while the server's
model-facing persisted history is what changed. The cascade strategy may need its
summary tier, which can make a compaction-slot model call and incur that model cost.
`/compact` is refused during a run or pending approval. An older server leaves the
capability false, so the command is hidden and a directly typed bare command gets a
local unavailable warning rather than reaching the model.

**`/soul` (read-only persona inspection).** Gated on `caps.soul` AND a wired soul
fetcher. It fires `GetSoul` (a build-time snapshot the server takes once at
startup) and shows a metadata line — provenance + trust state
(`user` | `project (trusted)` | `project (UNTRUSTED → not loaded)` |
`· DRIFTED`), byte size, and a short content hash — over the persona body. Because
the body can be up to 20 KiB, the panel is **scrollable**: `pgup`/`pgdn` (and
`up`/`down`, `home`/`end`) move a line-window, with a "lines X–Y of N" indicator
when the content overflows the window; `esc` closes. It NEVER edits the soul (the
soul is agent-read-only); trust/drift are computed server-side in composition and
only displayed here.

**`/usermodel` (read-only user-model inspection).** Gated on `caps.user_model` AND
a wired user-model lister. It fires `GetUserModel` (a **live** read of the
user-model store's bounded index) and shows count · size · hash over a key/description
list. Move with `↑`/`↓`; `enter` lazily requests the selected key's exact current
value and up to 16 recent lifecycle revisions. Every server-derived field is terminal-
sanitized. Old/base-only stores show the current value and honestly report history as
unavailable. Proposal-linked revisions show the proposal id beside their existing provenance.
`esc` returns from detail or closes the panel. The surface is read-only:
Forget remains an ordinary model tool behind its permission gate.

**`/dream` (manual memory maintenance).** The command appears only when the server sends
the `manual_dream` capability object and the dream client is wired; an older server hides it.
Choose project memory or the cross-project user model. Each target row reports the server's
independent generation/decision availability and bounded reason. Pressing enter explicitly
acknowledges that generation sends the selected bounded memory values and descriptions to the
configured planner and spends tokens. This action does not enable or change either consolidation
schedule.

The scrollable review shows every exact-duplicate or synthesized-replacement operation: survivor,
sources, current values/descriptions, proposed replacement, reason, and exact-duplicate eligibility.
Every untrusted physical line is quoted and prefixed. Model-authored replacement/reason text containing
hidden controls or Unicode format characters is rejected before retention, so the replacement approved
in this view is byte-for-byte the replacement that apply can persist.
`a` confirms apply of the **whole plan**; `x` confirms whole-plan dismissal with no mutation.
There are no source checkboxes. Approved synthesis rewrites the displayed survivor and tombstones
the displayed sources atomically per operation, but independent operations may conflict or fail and
the receipt therefore reports planned/applied/conflicted/skipped/failed source counts without a
grouped-undo claim. `r` explicitly generates a fresh plan and warns that this makes another provider
call and spends again; nothing auto-refreshes after a conflict or failure. A same-decision request while
apply is still running keeps `t` available for exact idempotent receipt retrieval. A genuinely
indeterminate transport error does the same because the first request may already have applied. An
opposite decision is never offered. An opposite decision against an active apply makes the old plan
non-actionable with no fresh-generation action; a known terminal opposite decision offers explicit `r`
regeneration. `NOT_FOUND` after expiry, restart, or wrong-replica routing cannot retrieve a receipt and
offers only explicit fresh generation.

Plans are opaque, process-local, bounded, and expire. Restart, expiry, or a decision routed to a
server replica other than the generator can make the retained plan unrecoverable; starting a separate
fresh review spends another provider call. The workflow is unavailable while ownership enforcement is enabled,
when no configured planner exists, or when the selected target store lacks both reviewed atomic
operations. It does not display provider/model identity and has no recall-usage counters.

**`/reflections` and `/reflect` (proposal review).** `/reflections` is gated on the
server's proposal capability. It loads at most 50 operator proposals plus at most 50 proposals
for the current project, keeps independent scope cursors, sorts staged/conflicted work ahead of recent terminal records,
and offers `n`/`p` bounded pages, `enter` detail, `a` approve facts or materialize/evaluate evidence-backed procedures, `x` reject, and `u` compensating undo. A materialized procedure shows its learned-skill id and points to `/skills`; no receipt auto-opens a modal. Detail uses a terminal-height window with arrow/page scrolling and shows the complete bounded canonical fact key, value, scope, and optional description before approval, plus
bounded proposal metadata, triggers, decisions, promotion receipt, and ownership-checked,
digest-reverified evidence provenance plus its bounded redacted canonical preview (source session,
locator/ordinal, optional event sequence and tool call, digest, and preview). Preview projection omits
raw tool and permission arguments, reasoning, binary data, controls, and credentials. Approval is enabled only when every evidence handle is currently available and the selected partition has an exact trusted, convergence-capable memory target; approve and undo are otherwise disabled with the server-provided reason while detail/reject remain available. The server re-verifies the same evidence bindings before writing memory.
Every mutation is a gRPC server call carrying the proposal's opaque expected version; a stale
decision, a newer memory revision, or an unsupported server is shown without changing local
state; a stale CAS offers `r` refresh and replaces the detail with the current version/status. Responses are generation-correlated and all rendered strings are terminal-sanitized.
The navigational overlay does not create a permission modal or send content to the model.

`/reflect` submits the current completed session synchronously through its persisted provider/model,
including when automatic learning is Off (which lazily initializes persistence and starts the dormant Build-owned bounded coordinator only for that explicit job). Its status receipt reports abstention or
the staged/promoted/conflicted counts. It is hidden when explicit reflection is unsupported and
never reflects a running or foreign-owned session.

**`/models` (model picker — the only *selecting* overlay).** Gated on
`caps.model_selection` (the server advertises ≥1 available provider) AND a wired
model lister. It fires `ListModels` and renders the selectable models as a **flat
list**, each row tagged with its `provider_id` (`provider · name`) plus capability
glyphs (`img` when the model takes image input, `reason` when it emits reasoning)
and a compact context window (e.g. `200K`, `1M`; omitted when unknown). The picker
opens with a **type-to-filter** input focused: type to narrow the list by a
substring match (case-insensitive) over `provider_id`, model `id`, and display
name — at 300+ live models this is how you find one fast. The list **scrolls** in a
window clipped to the terminal height that follows the cursor (the selected row
stays visible when you page past the top/bottom edge), so a large catalog never
overruns the screen. A header line reads **`current: <model> (<provenance>)`** — a
best-effort, client-derived hint of where the live model came from (`server default`
/ `--model flag` / `picked this session` / `workspace default` / `global default`);
it is a hint, not authority (the server owns the resolution). Each row carries a
two-cell marker column: **`●`** on the pending selection (the one the next session
will request) and **`★`** on the client **global default** (the model new/unseen
workspaces inherit). `↑`/`↓` move the cursor over the **filtered** set, `pgup`/
`pgdown` page, `home`/`end` jump. (`j`/`k` type into the filter — they do **not**
navigate here, unlike the read-only overlays — so a name like `kimi`/`jamba` filters
as typed.) `esc` is **two-stage**: with a non-empty filter it clears the filter (the
picker stays open); with an empty filter it closes the picker. Before selection, the
picker states: **`Switching models is expensive as it clears caches.`**

For `openai-codex`, the rows are the account's live entitlements, not public
OpenAI catalog guesses. A rejected token, unreachable private service, or
successful empty account list is promoted into a provider-specific line with an
actionable `auth.yaml`/restart or connectivity remedy. A prior successful list may
remain visible during a later refresh failure, but that does not hide an inference
failure. Codex rows never receive ToolHive's `org` intent label.

`enter` on the cursor row **switches immediately** — the conversation is ALWAYS kept.
Because the provider is FIXED per session, switching live means a real handoff: a fresh
session on the picked model is **seeded with the current session's conversation** via
`source_session_id` on the `CreateSessionRequest` (`CreateSessionWithCarryover`). The
server snapshots the source conversation and seeds it into the new session, so the model
sees the full prior context. While the target is being created and hydrated, mecatui
keeps the source ID, metadata, and visible projection but disarms its old live feed; the
input remains non-interactive. It then loads and validates the target's complete,
matching authoritative transcript, adopts that transcript (rather than retaining its
local projection), and only then best-effort closes the source. The header rebinds to the
NEW session's effective model and the transient status note reads **`switched to
<model> — conversation kept`**. For a **cross-provider** switch the server strips the
prior model's provider-private state (reasoning cache, provider phase, item ids) and
replays the text/roles/tool calls to the new provider. The picker provides the single
cache warning before the switch. There is no confirm overlay and no same-provider gate:
the server accepts carryover for any provider. Creation or transcript-hydration failure
preserves the open source session,
restores its live feed, and returns to idle without claiming carryover; an unused target
is best-effort closed after hydration failure. A source-close error does not undo a
hydrated target. If no live session exists yet (pre-first-connect, or a failure left no
session), the pick falls back to a plain `CreateSession` — there's no source to carry
from. **Dropping the conversation is a separate action**: run `/clear` to create a
fresh empty-history successor that inherits the server-owned exact placement and
effective model.

`ctrl+g` sets the cursor row as the **client global default** (the `★` row) — used
by new/unseen workspaces; it is control-modified so a bare `g` stays typeable in the
filter. The pick is persisted **client-side** to a state file:
`$XDG_STATE_HOME/mecatui/models.yaml` (fallback `~/.local/state/mecatui/models.yaml`)
— a per-workspace map (realpath-keyed) plus a global `default:` block. Read
precedence on launch (highest → lowest): an in-session restart pick → the `--model`
flag → the per-workspace entry → the client global default → the server's configured
(`--default-provider`/`--default-model`) or built-in default. A workspace pick is
scoped to its workspace only; an unseen/new repo falls
back to the global default (then the server default). On launch the selection is
**reconciled** against `ListModels` BEFORE the first `CreateSession`: if the
persisted model's provider is no longer available (its key was removed), the
selection falls back to the server default for that run with a loud notice that names
the model the session actually fell back to, and the state file is left intact (the
preference returns next launch). The reconcile is **provider-level only** (issue
#41): a saved model absent from the snapshot is **sent anyway** — the boot snapshot
may still be the *embedded* catalog floor (the async live refresh always loses the
connect race), and the server validates the model string verbatim. If the server
then **rejects** the saved selection, the create retries once on the server default
and connect completes **with a loud warning** naming the rejected model and the
server's error — never silently; the state file is still not rewritten. Accepted
trade-off: a model genuinely gone from the live provider now creates a session
successfully (the header echoes the selector verbatim) and fails at the **first
run** with a visible stream error — server-authoritative, recoverable via
`/models`. The state file is machine-written **state** under
`XDG_STATE_HOME`, a sibling of the human config — the same settings-vs-state split
as `trust.yaml`.

**`/effort` (reasoning-effort picker).** Gated identically to `/models`
(`caps.model_selection`), it opens a small enum picker over the fixed reasoning-
effort tiers (`auto`, `low`, `medium`, `high`, `xhigh`, `max`) — `auto` is the
unset sentinel (operator/provider default) and is sent as the empty effort. The
cursor opens on the current (server-resolved) tier, marked with a `●`; when the
current model is known to lack reasoning support a warning notes a tier will be
ignored. `enter` on a tier applies **directly** via a **conversation fork** (ADR
0068): the server forks the session's history onto a peer session at the new effort,
so the **transcript is kept** — no restart, no wipe, no confirm step. There's a
brief "switching effort — forking conversation…" transition while the fork rebinds;
the source session is then closed. `esc` closes the picker. The only RPC is the fork
itself — the enum is fixed and client-owned (no filter).

**`/worktrees` (server-owned placement picker).** The client sends only the current
source session ID. The server owner-authorizes and exactly reattaches that session,
then returns display-safe labels/branches/revisions plus opaque selectors; no filesystem
path or exact EnvironmentRef crosses the API. Selecting a row calls `ForkSession` with
the fresh selector, preserving conversation history. Selectors are caller/source scoped,
expire on server restart, and are accepted only by ClearSession/ForkSession. A stale
selector triggers a relist; a relist or switch failure leaves the source active and
retains the current selection. No-FS sessions return an empty list and cannot upgrade.

**Workspace slash commands are ON by default** on top of the built-ins, expanding
`/<name>` inputs from the conventional workspace dirs `.mecatl/commands` and
`.claude/commands` (`<name>.md` templates — the Claude Code convention). They're
local, user-authored prompt templates, so there's no network or trust cost (unlike
MCP prompts, which ride a connected MCP server). Pass `--no-commands` to disable expansion,
or `--commands-dir` to point at a different directory. Built-ins and workspace
commands merge in the palette (a built-in wins a name collision). When no
workspace command dir exists the palette is **not** empty — the built-ins are
still there; a typed prefix that matches nothing shows a muted "no matching
command" note.

**Cross-session memory (Remember/Recall) is ON by default**, scoped per-project
under `~/.local/share/mecatui/memory/<path-slug>/` (or `$XDG_DATA_HOME/...` when
set), where `<path-slug>` is the absolute workspace path with `/` replaced by `-`
(e.g. `-home-me-dev-mecatl`) — deterministic, human-legible, and collision-free
across same-named checkouts. Pass `--no-memory` to disable it or `--memory-dir` to
relocate the store. Automatic memory consolidation (which spends tokens) stays **off** on the
embedded server and retires exact duplicates only; it never rewrites a survivor. The separate
`/dream` command explicitly generates an inspectable exact/synthesized plan and requires whole-plan
apply or dismiss. Generation and regeneration spend tokens, and pending plans do not survive restart
or move across replicas.

**The durable session store is ON by default** (issue #79): the embedded server
persists every session as append-only JSONL — the snapshot, the tool-call audit
log, and the relayed event timeline — under a per-workspace dir
`~/.local/state/mecatui/sessions/<path-slug>/` (or `$XDG_STATE_HOME/...` when set),
using the SAME `<path-slug>` scheme as the memory store so the two sit side by
side per workspace. So a session survives a restart and can be inspected after the
fact. To find your own history, list the per-workspace store and pick the subdir
matching your workspace (its name is the workspace path with `/` replaced by `-`):

```console
$ ls ~/.local/state/mecatui/sessions/   # each subdir is one workspace
-home-me-dev-mecatl   -home-me-scratch
```

**Privacy:** the store holds the **raw conversation** — your prompts, the
model's output, and tool arguments/results — in **plaintext** on disk; the dir is
created mode `0700` (owner-only). Pass `--no-store` to keep everything in memory
(persisting nothing), or `--store-dir` to relocate it. To keep the durable store
from growing without bound, embedded mode exposes an explicit local retention
policy: child snapshots default to 7 days / 500 per family, scheduled fires to 7
days, and destructive main cleanup defaults off. Set the retention flags above or
the operator `retention.version: 1` settings block. Enabling a main limit requires
explicit acknowledgement and logs the planner summary. The Sessions storage-health
view shows the effective policy. Connected mode rejects local retention flags and
can only display a remote policy advertised by the authenticated management
capability; it never claims to configure that server. **Concurrency:**
the per-workspace default assumes a single `mecatui` per workspace; a second
instance hosting an embedded server on the same workspace shares the dir, and its
retention GC may prune the other instance's idle sessions early — give a second
instance its own `--store-dir` (or `--no-store`) to avoid that.

### `@`-file mentions and media attachments

Typing `@` opens an inline **file-completion menu** — the same kind of dropdown as
the `/` palette, mutually exclusive with it (a line is either a `/command` or has an
`@token` word, never both). It lists workspace files matching the typed token
(case-insensitive substring on the path or base name; dotfiles/`.git` pruned; capped
to 8 rows and a bounded directory walk so a huge tree never blocks). `↑`/`↓` select,
`tab`/`enter` complete the highlighted path into the input (`@<path> `), `esc`
dismisses.

On submit, every `@path` in the prompt is read and routed by sniffed content type:

- an **image** or **audio** file becomes an inline media part sent over gRPC
  (`Prompt.parts`), rendered in the transcript as a `📎 image/png (inline)` line;
- any **other** file is treated as text and **inlined** into the prompt as a
  delimited block (so a text-only model still sees its content).

Media attachment is **caps-gated**: the server advertises whether its wired model
consumes images/audio (the `image`/`audio` server capabilities). Mentioning an image
file to a text-only model is a **loud refusal** — the submit is rejected with an
inline `attach: …` error, the input is kept, and nothing is sent (never a silent
drop). Client-side size limits (10 MiB per file, 20 MiB and 16 parts per prompt)
mirror the server's domain caps and fail fast before opening a stream; the server
re-validates every part regardless.

### `ctrl+v` — paste a clipboard image

`ctrl+v` reads the **OS clipboard**. The contract is **image-first, text-fallback**:

- a clipboard **image** is staged as an inline attachment and an `[Image #N]` marker
  is inserted into the prompt (cap-gated on `image` — on a text-only model the image
  is refused with a status line and nothing is staged);
- otherwise the clipboard **text** replaces any active prompt selection, or is inserted
  at the caret when there is none (always — text is not cap-gated, so `ctrl+v` still
  pastes text on an image-incapable model).

On submit, every surviving `[Image #N]` marker becomes a media part (ascending by
`N`, in display order), the marker is **stripped** from the sent text, and the part
rides the same `Prompt.parts` send path as an `@`-mention attachment — there is no
second send path. Deleting a marker from the input before sending drops that
attachment. `N` is monotonic and never reused (a delete leaves a numbering gap, by
design); `/clear` drops any staged-but-unsent attachments.

Dragging an image file onto the terminal usually arrives as a **pasted path**; a
single media-file path is staged the same way (an `[Image #N]` marker), and anything
that is not a stageable media file stays literal pasted text.

Clipboard reads shell out to a platform tool — **wl-clipboard** (`wl-paste`, Wayland),
**xclip** (X11), **pbpaste**/**pngpaste** (macOS), or **PowerShell** (Windows). With
none installed, `ctrl+v` reports an install hint. macOS caveat: `pngpaste` reads the
`«class PNGf»` pasteboard flavour, so an image copied from a **Chromium/Electron** app
(which uses the `public.png` flavour) may not paste as an image and falls back to text.

### Large text pastes

A **large** bracketed paste — **≥ 2000 characters or ≥ 30 lines**, alone or
**cumulatively** (current input + paste ≥ 2000 characters, so repeated medium pastes
can't rebuild the lag; only the incoming paste is ever staged, typed text never
converts) — does not enter the input buffer literally. It is staged behind a
`[Pasted text #N]` placeholder (the text twin of the `[Image #N]` marker), and the
full payload expands back **in place** when the prompt is sent — or, mid-run, when
`enter` steers it on a steering-capable server or queues it as a fallback follow-up
(the queue always holds final text). Below the thresholds a paste is byte-identical to the literal-insert behaviour.

Why: the input widget re-wraps every buffered line on every rendered frame, so a huge
paste sitting in the buffer made **every subsequent keystroke** pay for the paste
(issue #45 — multi-second key lag after pasting a big log). The placeholder keeps the
buffer (and the per-frame input render) small while the prompt still carries the full
text on the wire.

The marker follows the image-marker conventions: `N` is monotonic and never reused or
renumbered (its numbering is separate from `[Image #N]`); **deleting the marker from
the input before sending silently drops that paste**; `/clear` drops any
staged-but-unsent pastes. `@`-mentions or `[Image #N]` markers *inside* the pasted
payload behave exactly as if typed — expansion happens before mention/media handling.
One divergence from a small paste: without an active prompt selection, the placeholder
is **appended at the end of the input** (like image markers), not inserted at the cursor.
With a selection, the selected text is replaced before the placeholder is inserted.

## First-run welcome splash

When the conversation is empty (the zero-state), mecatui shows a centered welcome
splash instead of a bare prompt. It claims no keyboard — typing, `/`, and `?` flow
straight over it — and it vanishes the instant the first message is sent. The splash
has three parts, all rendered by the `cmd/mecatui/ui/welcome` subpackage (which imports
only `theme` + the charm libraries + stdlib — never `client`/`ui`):

- **Mascot** — the mecatito dog, rendered two ways:
  - **Half-block** (the universal path): a truecolor `▀` half-block downscale of the
    embedded PNG, responsive to terminal height (36 / 48 / 60 columns wide; taller
    terminals get the larger, more detailed render). This is what Ptyxis (sixel-only),
    tmux, and any non-Kitty terminal sees.
  - **Kitty high-res** (zero new deps, via the already-present
    `github.com/charmbracelet/x/ansi/kitty`): on a Kitty-graphics terminal —
    **kitty**, **Ghostty**, **WezTerm**, **Konsole** — the full-resolution image is
    transmitted out-of-band (via `tea.Raw`, never View content, so the alt-screen cell
    renderer can't mangle it) and painted with **Unicode placeholders** (U+10EEEE), at
    the SAME `cols×rows` cell footprint as the half-block, so the layout is identical
    either way. Detection is conservative and env-based; a miss falls back to the
    always-correct half-block. Override with `MECATUI_FORCE_KITTY=1` /
    `MECATUI_NO_KITTY=1`.
- **Wordmark** — "mecatl" in hand-authored 3-row block letterforms. On a **truecolor**
  terminal it gets a per-column jade→gold gradient (theme `primary` → `accent`); on a
  poorer color profile it collapses cleanly to the single accent color (never unstyled,
  never a banded blend).
- **Info block** — workspace path, active model, build version, a tagline, the
  caps-tailored affordance rows (`?` / `/` / `ctrl+a` / `ctrl+t`), and a "memory is on"
  note when cross-session memory is enabled.

A **tiny terminal** (very narrow or very short) degrades to a minimal hint (title +
prompt hint + affordances) without the mascot or wordmark, and never panics. Pass
`--no-banner` (or run under `--quiet` / a non-interactive stdin) to skip the splash and
show the plain prompt-hint card.

> **Asset sync note:** `cmd/mecatui/ui/welcome/assets/mecatito.png` is a COPY of the
> repo-root `assets/mecatito.png` (go:embed cannot reach a parent directory). The
> repo-root file is canonical; keep the copy in sync if the mascot changes.

> The transmit escape uses the **transmit-and-put** action (`a=T`) with `U=1`/`c=`/`r=`
> on the first chunk — a bare transmit (`a=t`) leaves the placement keys inert, the
> terminal creates no virtual placement, and the placeholder grid paints nothing
> (issue #44; regression-pinned by `TestTransmitMascotCreatesVirtualPlacement`). It
> also sends `q=2` to suppress the terminal's OK/error responses, which would
> otherwise surface as stray input.
>
> The mascot is **downscaled to the target `cols×rows` cell footprint before
> PNG re-encoding** (`downscaleMascot`, the same alpha-weighted box-average the
> half-block path uses), so the transmitted PNG is ~tens of KB (not the ~1 MB of
> the full 1254×1254 PNG). This matters on Ghostty: the exact
> `a=T`/`U=1`/U+10EEEE virtual-placement pattern is known-buggy on Ghostty 1.3.1
> stable (ghostty-org/ghostty#13056) where a large multi-chunk transmit can
> render at a fraction of its intended width; a small, single-chunk,
> already-at-cell-resolution PNG sidesteps the worst of that. Bounded by
> `TestTransmitMascotPayloadBounded`.
>
> The Kitty high-res path is **unit-tested** (escape generation + the env detection
> truth table) but **not live-verified** — the dev environment has no Kitty terminal.
> The half-block path is the verified default.
>
> **Multiplexer caveat (tmux/screen):** a terminal multiplexer between mecatui and
> the outer terminal does **not** pass Kitty graphics APC through by default — it
> strips the sequences it doesn't recognise, so the transmit never reaches the
> outer Ghostty/WezTerm and the placeholder grid paints nothing. mecatui detects a
> multiplexer session (`$TMUX` for tmux, `$STY` for GNU screen) and **falls back to
> the half-block path** in that case, since a Ghostty/WezTerm env signal
> (`TERM_PROGRAM`, `GHOSTTY_*`) is inherited verbatim by the multiplexer and would
> be a false positive. `KITTY_WINDOW_ID` and `TERM=*kitty` are kept as sufficient
> even under a multiplexer (kitty sets them; tmux strips `KITTY_WINDOW_ID` unless
> passthrough relays it). To use the high-res path under tmux, enable
> `tmux set-option -g allow-passthrough on` and force the Kitty path with
> `MECATUI_FORCE_KITTY=1`; `MECATUI_NO_KITTY=1` forces the half-block path and wins
> over force. Truth-table-pinned by `TestDetectKittyTruthTable`.

## Keys

| Key | Action |
|---|---|
| `enter` (idle) | send the prompt |
| `enter` (while a run streams) | **steer the current run** when supported (applies at the next turn boundary); otherwise **queue a follow-up** (staged; the whole queue is **merged into one prompt** and sent when the turn ends) |
| `↑` (empty input, non-empty queue) | **edit queued** — pull the merged staged follow-ups back into the input for revising (non-destructive; the queue is emptied into the textarea, not dropped). Works both mid-run and while a paused queue is held. |
| `shift+enter` (or `ctrl+j`) | newline in the input |
| paste (bracketed) | replace the active prompt selection, or insert clipboard text at the caret; a single pasted **media-file path** is staged as an attachment instead, and a **large** paste (≥ 2000 chars — alone or combined with the current input — or ≥ 30 lines) is staged behind a `[Pasted text #N]` placeholder, replacing the active selection before insertion or otherwise appending at the end of the input, expanding on send (ignored while an overlay/modal is open) |
| `ctrl+v` | read the OS clipboard — a clipboard **image** stages as an `[Image #N]` attachment (when supported), else replace the active prompt selection with clipboard **text** (see below) |
| `ctrl+u` | clear the entire unsent draft, including staged attachments and large-paste placeholders (rebindable via `ClearPrompt`) |
| `esc` (while a run streams) | cancel the in-flight run (sends `Cancel`; waits for the terminal result; leaves the draft and queued follow-ups intact) |
| `enter` (idle, **paused queue**, empty input) | resume — send the merged staged follow-ups |
| `esc` (idle, **paused queue**) | clear the queue (the current draft remains intact) |
| `ctrl+c` | graceful quit (double-press): with a non-empty prompt the first press **clears the input**; on an empty prompt it **arms** the guard and shows a footer hint — press `ctrl+c` again within 3s to exit. Any other key disarms. The fatal (dead-connection) screen exits on a single press. |
| `ctrl+d` | the unix EOF-habit quit (double-press, **empty prompt only**): on an empty prompt it arms its OWN guard and shows a hint — press `ctrl+d` again within 3s to exit. On a populated prompt it stays the textarea's delete-forward, never a quit. Independent of `ctrl+c` (neither key confirms the other). |
| `ctrl+z` | **suspend the TUI to the shell** (SIGTSTP); `fg` resumes it. Works in every state — idle, mid-run, even the permission modal (the ask stays pending). **Suspending does NOT stop the embedded `mecated` or an in-flight run** — the engine keeps working in the background and the UI re-syncs on `fg`. An in-conversation notice on resume names the session and what was suspended (a pre-suspend terminal notice can't survive the alt-screen teardown, so it's shown on return instead). |
| in the permission modal: `a`/`y` | allow once (rebindable via `Allow`; the button label reflects the live chord — `[A]llow` for the default `a`, `[Y] allow` for an override) |
| in the permission modal: `w` | always allow (this session; offered for the main agent's asks only, not surfaced subagent asks; rebindable via `AllowAlways`) |
| in the permission modal: `d`/`n`/`esc` | deny (rebindable via `Deny`) |
| in the permission modal: `←`/`→`/`tab` | cycle the focused button; `enter` activates it |
| in the permission modal (non-diff ask): `pgup`/`pgdn`, `↑`/`↓` | scroll the modal's in-card args region when rows are hidden (long args wrap; the `… · ctrl+t full args` hint shows when anything is hidden) |
| in the permission modal (non-diff ask): `ctrl+t` | open the **full-screen ask-args view** (see below); plan asks and Edit/Write asks keep the in-modal expand behaviour instead |
| in the full-screen ask-args view: `r` | toggle the args between the pretty tier (a Bash `{"command": …}` decodes to the command text, with a muted `timeout_ms: N` annotation when the envelope carries one) and the **verbatim wire args string** (rebindable via `RawArgs`; shown only when the tiers genuinely differ, hidden when they are byte-identical) |
| in the full-screen ask-args view: `esc` / `ctrl+t` | back to the modal |
| in the permission modal / plan-review bar / ask-args view: left-click a button | activate it (same as its chord — **alt screen only**). Clicking anywhere else in the modal does nothing (it is a gate, not a form) |
| `pgup` / `pgdn` | scroll the conversation up / down |
| `home` / `end` | jump to the top / bottom of the conversation (`end` resumes auto-follow) |
| mouse wheel | scroll the conversation (**alt screen only**; see below) |
| mouse click (prompt text) | place the prompt caret; drag from it to select prompt text (**alt screen only**; see below) |
| mouse drag (left) | select text in the prompt or conversation — dragging in the conversation to an edge auto-scrolls; prompt release does **not** copy (alt screen only; see below) |
| double / triple-click (left) | select word / whole line in the conversation (copies; alt screen only) |
| `ctrl+shift+c` | copy the active prompt or conversation selection; no selection is a no-op |
| right-click | copy the active prompt or conversation selection; no selection is a no-op |
| middle-click | **paste the primary selection** (X11/Wayland select-to-copy buffer) into the prompt — read via the shell backend (`wl-paste --primary` / `xclip -selection primary -o`), falling back to an OSC52 primary read; routed through the same pipeline as a bracketed paste, so a large selection stages as `[Pasted text #N]`. `shift+middle-click` always performs the terminal-native paste instead. |
| `esc` (with an active selection) | **clear the selection** first — before any other `esc` meaning |
| `?` | help overlay (on an empty prompt) |
| `/` | slash-command palette (built-in `/clear`, `/help`, `/session`, `/retry`; capability-gated `/compact`, `/mcp`, `/agents`, `/team`, `/skills`, `/soul`, `/usermodel`, `/reflections`, `/reflect`, `/dream`, `/models`, `/effort`, `/worktrees`, `/schedule`; operator-setting `/learning`; plus workspace commands) |
| `alt+m` | cycle the current session permission mode: **default → plan → accept-edits → default**. The server/session is authoritative; if the aggregate rejects the switch because a turn is running or awaiting approval, mecatui shows a notice and retries the selected mode at the next prompt boundary. |
| `ctrl+a` | open the **unified agents overlay** — ONE surface with three tabs: **Subagents** (the flat Subagent-child fleet), **Parallel** (the fork-join GROUP roster — join mode, branches, winner, fork paths), and **Teams** (the full roster + per-member focus of the most-recent team). `tab` cycles tabs, `enter` focuses a row/group, `esc` steps back / closes. The default tab is **context-sensitive** (team live → parallel live → subagents → parallel → team). Works **while idle and mid-run**; inert under a permission modal. `/team` opens it pinned to the Teams tab. |
| `ctrl+g` | select all prompt text (rebindable as `SelectAll`; inside the `/models` picker, the existing `SetGlobalDefault` binding is used instead) |
| `x` (agents overlay, on a **running** lane) | **cancel that child agent** (sends `CancelChild` with the lane's child id; the run itself keeps streaming). Works on all three tabs: a **Subagents** lane (roster or focus pane), a **Parallel branch** (inside a focused group — `↑/↓` selects the branch), and a **team member** (Teams roster or focus pane; mid-drive OR idle between rounds — the member is de-scheduled and its claimed tasks released). Confirm-less, because it is recoverable: the child is persisted (a subagent stays **resumable** by its `agentId`; a cancelled branch reads `[FAILED] cancelled by user`; a cancelled member shows `stopped — cancelled`). Inert on a done lane. If the child was parked on a surfaced permission ask, the server retracts it (`permission.retract`) and the approval modal dismisses itself. |
| `@` | file-mention menu — complete a workspace path, then attach it on submit (see below) |

**Always-allow (the always button).** A main-agent ask offers a third button —
**Al[w]ays** with the default `w` chord (it degrades to `[Q] always allow` under
an `AllowAlways` override) — alongside allow-once and deny. Choosing it permits the current call AND learns a rule that
suppresses the re-ask for the **exact same command** for the rest of this session
(session-scoped, evicted when the session closes). It never overrides a configured
deny/ask — a deny in any scope is still absolute, and a configured ask is never silenced
(it only loosens the built-in default). It is offered for the **main agent's** asks only:
a surfaced subagent ask keeps the two-button (allow-once / deny) modal, because a child
engine's permission policy learns no rules.

**Concurrent asks queue.** Concurrent subagents (team members, parallel Subagent calls)
can surface permission asks **concurrently** — each parks its child server-side until
answered. The TUI keeps a FIFO queue behind the visible modal: a second ask never
clobbers the first, and both the modal title and the footer show a **`(1 of N)`** badge
while asks are queued. Answering or denying the visible ask advances the queue (the next
modal opens immediately); a cancelled child's queued ask is withdrawn in place (with a
notice, since the count badge advertised it); any asks still queued when the run ends are
dropped. The keys are unchanged — you only ever answer one modal at a time.

**Long args wrap, scroll, and open full-screen.** A non-diff ask's args (a Bash
`{"command": …}` decodes to the command text; anything else renders as pretty
JSON) **wrap** inside the card — no more single unreadable line running off the
edge. The command renders in the bright `askArgs` style with a tool-coloured
left accent bar, so the thing being approved reads distinct from the muted
reason/hint around it. The in-card args region caps at ten rows: when anything
is hidden, a hint (`… · pgup/pgdn scroll · ctrl+t full args`) shows, and
`pgup`/`pgdn`, `↑`/`↓`, or the mouse wheel over the card scroll the region in
place. The card itself sizes to its content up to 132 columns on a wide
terminal (still a centred card, not a full-width band). `ctrl+t`
on a non-diff ask opens the **full-screen ask-args view**: the whole args fill
the conversation region and scroll, and both that view and the modal mark a
soft-wrapped command with a warning-coloured `↩` at the END of each continued
row, so a wrap is never mistaken for a real newline in the args. The buttons
pin to the bottom bar, and
`r` toggles between the pretty tier and the **raw tier — the VERBATIM wire args
string**, sanitized but otherwise untouched (the escape hatch that can never
lie: "exactly what am I approving"). The toggle is offered whenever the tiers
genuinely differ (any Bash ask, or an ask whose pretty tier re-indents the
verbatim raw); an ask whose tiers are byte-identical (empty args, a non-JSON
single-line passthrough) shows no toggle hint. `esc` or
`ctrl+t` returns to the modal; the verdict keys work from inside the view.
`ctrl+t` routes by ask type: plan asks keep the scrollable plan-review view and
Edit/Write asks keep the in-modal diff expand — see
[ADR 0222](./adr/0222-mecatui-ask-args-view.md).

For hand-testing the modal's long-args surfaces without driving a live run,
`MECATUI_DEBUG_ASK=1` registers a `/debug-ask` built-in that injects a fake
long-args permission ask through the real reducer (deliberately env-var-only —
it never appears in `--help`).

The `?` overlay enumerates the rest of the chords — `ctrl+v` (paste a clipboard
image), `ctrl+o`/`ctrl+r`/`ctrl+p` (MCP inventory / resources / prompts), `ctrl+a`
(the unified agents overlay — three tabs, available idle **and** mid-run; see the
keys table above), `ctrl+t`
(expand/collapse details), and the scroll keys (`pgup`/`pgdn`, `home`/`end`, mouse
wheel) — and greys out any whose feature the connected server has not enabled
(driven by the server's relayed capabilities). When the server serves agent
definitions (`caps.agents`), it also notes that `/agents` browses the definition
inventory.

### Remapping keys

Every action in mecatui's keymap is rebindable. Three override layers exist,
and they resolve to the same map **per action** — a higher layer rebinds only
the actions it names. Precedence, lowest to highest: the legacy server file
< the client file < the **CLI flag wins**:

- **`~/.config/mecatui/settings.yaml`** (client-owned, mecatui's own settings
  file, strictly parsed — an unknown top-level key is a startup error) —
  a `keymap:` map of action name → comma-separated chord string:

  ```yaml
  keymap:
    Agents: ctrl+f12
    Effort: ctrl+f5,ctrl+f6
  ```

- **`--keymap Action=chord[,chord2]`** — repeatable CLI flag; each occurrence
  rebinds one action and overrides the YAML entry for that action.

  The `keymap:` setting was migrated from `~/.config/mecatl/settings.yaml`
  (the server-shared operator file) — the legacy location still works but is
  **deprecated** (a startup warning names the new home), and the client file
  wins on conflict.

Settings are read **once at startup** — restart mecatui to apply a change (live
reload is a planned follow-up, issue #456).

#### The action map

The rebindable actions (names are the `keyMap` struct field names, matched
exactly). **Scope** is `global` (live at the main prompt / idle) or `overlay`
(consulted only while a specific overlay owns the keyboard — so bare letters are
safe there). Actions marked *(approval)* are the permission-modal keys.

| Action | Default chord(s) | Scope | What it does |
|---|---|---|---|
| `Submit` | `enter` | global | send the prompt; while a run streams, steer when supported or queue a follow-up otherwise |
| `Newline` | `shift+enter`, `ctrl+j` | global | newline in the input |
| `Cancel` | `esc` | global | cancel the running turn; idle Escape leaves the current draft intact |
| `ClearPrompt` | `ctrl+u` | global | clear the entire unsent draft, including staged attachments and large-paste placeholders |
| `EditBack` | `up` | global | pull the queued follow-ups back into the input (empty input only) |
| `Paste` | `ctrl+v` | global | paste a clipboard image as an attachment, else clipboard text |
| `SelectAll` | `ctrl+g` | global | select all prompt text; the `/models` picker keeps its `SetGlobalDefault` binding |
| `CopySelection` | `ctrl+shift+c` | global | copy the active prompt or conversation selection; no selection is a no-op |
| `Quit` | `ctrl+c` | global | graceful quit (double-press; first press clears the input or arms) |
| `QuitD` | `ctrl+d` | global | EOF-habit quit (double-press, empty prompt only; independent of `Quit`) |
| `Suspend` | `ctrl+z` | global | suspend the TUI to the shell (`fg` resumes; the engine keeps running) |
| `Allow` | `a`, `y`, `enter` | *(approval)* | allow the pending tool call once |
| `AllowAlways` | `w` | *(approval)* | always allow this exact call (session-scoped; main-agent asks only) |
| `Deny` | `d`, `n`, `esc` | *(approval)* | deny the pending tool call |
| `ScrollU` | `pgup` | global | scroll the conversation up |
| `ScrollD` | `pgdown` | global | scroll the conversation down |
| `ScrollTop` | `home` | global | jump the conversation to the top |
| `ScrollBottom` | `end` | global | jump to the bottom (resumes auto-follow) |
| `ModeSwitch` | `alt+m` | global | cycle permission mode (default / plan / accept-edits) |
| `MCPPanel` | `ctrl+o` | global | MCP inventory panel |
| `Resources` | `ctrl+r` | global | MCP resources picker |
| `Prompts` | `ctrl+p` | global | MCP prompts picker |
| `Agents` | `ctrl+a` | global | unified agents overlay (subagents / parallel / teams) |
| `ExpandTools` | `ctrl+t` | global | expand/collapse tool-card details & reasoning summaries; in the permission modal, opens the full-screen args view for non-diff asks (in-modal diff expand for Edit/Write; untouched for plan asks) |
| `Help` | `?` | global | this help overlay (on an empty prompt) |
| `Effort` | `ctrl+e` | global | reasoning-effort picker |
| `Up` | `up`, `k` | overlay | move the cursor up |
| `Down` | `down`, `j` | overlay | move the cursor down |
| `Choose` | `enter` | overlay | select the cursor row |
| `Close` | `esc` | overlay | close the overlay |
| `Refresh` | `r` | overlay | re-issue the overlay's primary fetch (MCP panel re-probe) |
| `Tasks` | `t` | overlay | agents overlay: flip the Teams tab to the task board |
| `Findings` | `f` | overlay | agents overlay: flip the Teams tab to the findings ledger |
| `JumpTop` | `home`, `g` | overlay | jump to the first roster row |
| `JumpEnd` | `end`, `G` | overlay | jump to the last roster row |
| `NextTab` | `tab` | overlay | agents overlay: switch tab |
| `CancelChild` | `x` | overlay | agents overlay: cancel the selected running child |
| `RawArgs` | `r` | overlay | full-screen ask-args view: toggle pretty ↔ raw args |
| `SetGlobalDefault` | `ctrl+g` | overlay¹ (`/models` picker) | set the cursor row as the client global default |

¹ `SetGlobalDefault` is consulted only inside the `/models` picker, but it is in
**neither** of the validator's collision scopes (`globalOpen` /
`overlayInternal`) — so its chord is not collision-checked against the other
actions. Keep it `ctrl`-modified (the default `ctrl+g`): a bare `g` would be
swallowed by the picker's filter input and by `ScrollTop`/`JumpTop`.

`RawArgs` and `Refresh` share the default chord `r` in **disjoint surfaces** (the
MCP overlay vs the full-screen ask-args view — an overlay never owns the keyboard
while the permission modal is open), so the shared default passes validation; an
explicit rebind of either must keep the pair disjoint, or startup fails with a
`keymap:` error.

#### What the override cannot reach — the textarea's own editing keys

The prompt input is the bubbles `textarea` widget, which ships its **own**
keymap (`textarea.DefaultKeyMap`). Its editing keys, including upstream keyboard
selection, are not remappable through `--keymap`; the client-owned exceptions are
`SelectAll`, `CopySelection`, and `ClearPrompt` in the action table above:

| Chord(s) | Edit |
|---|---|
| `right` / `ctrl+f`, `left` / `ctrl+b` | character forward / backward |
| `shift+right`, `shift+left`, `shift+up`, `shift+down` | extend or shrink the prompt selection |
| `alt+right` / `alt+f`, `alt+left` / `alt+b` | word forward / backward |
| `down` / `ctrl+n`, `up` / `ctrl+p` | next / previous line |
| `home` / `ctrl+a`, `end` / `ctrl+e` | line start / line end |
| `alt+backspace` / `ctrl+w`, `alt+delete` / `alt+d` | delete word backward / forward |
| `ctrl+k` | kill to line end |
| `ctrl+u` | clear the entire unsent draft (mecatui intercepts it as `ClearPrompt`) |
| `backspace` / `ctrl+h`, `delete` / `ctrl+d` | delete character backward / forward |
| `enter` / `ctrl+m` | insert newline (mecatui intercepts `enter` as Submit first) |
| `ctrl+v` | paste (mecatui intercepts `ctrl+v` as Paste first) |
| `alt+<` / `ctrl+home`, `alt+>` / `ctrl+end` | input begin / end |
| `alt+c`, `alt+l`, `alt+u` | capitalize / lowercase / uppercase word forward |
| `ctrl+t` | transpose characters (mecatui intercepts `ctrl+t` as ExpandTools first) |

The important interaction: **remapping a mecatui action off a chord frees that
chord to reach the textarea.** With the defaults, `ctrl+a` opens the agents
overlay and `ctrl+e` opens the effort picker — so the readline line-start /
line-end chords never reach the input. Rebind the actions away
(`keymap: {Agents: ctrl+f12, Effort: ctrl+f5}`) and `ctrl+a` / `ctrl+e` start
jumping the cursor to the line start / end instead. The `?` help overlay, the
welcome card, the footer help/approval lines, the inline-card affordances
(reasoning/subagent/team trace headers, collapse roll-ups, the team `+N more`
advertisement), the generic permission-modal buttons, AND every overlay's
navigation footer (the agents/team/mcp/effort/models/sessions/worktrees/
schedule/skills/soul/user-model pickers — `↑/↓`, `enter`, `esc`, `tab`, the
team `t`/`f`, the subagent `x`, the MCP `r`, the models `ctrl+g`, the parallel
`home/g·end/G`) all show the **live** bindings for any action backed by a
rebindable keyMap entry. Literal chords remain only for genuinely local controls
that do NOT consult the keyMap: for example the slash-palette / `@`-mention
menu's `up`/`down`/`tab`/`enter`/`esc`, raw form/list arrows and tabs (models,
skills, MCP prompt arguments, schedule creation/inspection), plan-review arrows
and mouse wheel, and the schedule panel's `c`/`p`/`r`/`f`/`d`/`/` plus the
create-form `y`/`n`. Those controls are fixed in their handlers, so the literal
shown is the chord that actually fires. With the default approval chords (`a`/`w`/`d`) the
permission-modal buttons render the historical word-embedded form (`[A]llow` /
`Al[w]ays` / `[D]eny`); rebound, they degrade to an honest standalone form
(`[Y] allow` / `[Q] always allow` / `[N] deny`, or `[ctrl+y] allow` for a
modified chord) so every displayed chord is the one that actually fires.

#### Validation rules

An invalid override fails startup with a `keymap:` error. The rules
(`keymap.Parse` + `keymap.Validate`):

- **Unknown actions are rejected** — names must match the action table exactly.
- **Empty chords are rejected**; duplicates within one action are deduped.
- **Bare printable runes are rejected on global actions** — a global-scope
  action must be a modified or special chord (`ctrl+x`, `alt+m`, `f5`, `home`,
  `tab`, …), never a bare letter that would swallow prose input. (Overlay-scope
  actions — and the approval keys — may be bare: they only fire while a modal or
  overlay owns the keyboard.)
- **No collisions within a scope**: two global actions may not share a chord,
  and two overlay actions may not share one either.
- **Approval consistency**: `Deny` may not share a chord with `Allow`,
  `AllowAlways`, `Submit`, or `Cancel`.
- **`Submit` ≠ `Newline`**: the send key and the newline key must be distinct.

**Scrollback and auto-follow.** The conversation viewport **auto-follows** the
bottom (tails streaming output) until you scroll up — with `pgup`, `home`, or the
mouse wheel. While scrolled up the header shows a muted **`↑ NN%`** position cue,
streaming continues to render *in place* (a new delta no longer yanks the view to
the bottom), and auto-follow stays off. Scrolling back to the bottom — `pgdn` past
the end, `end`, or the wheel — re-pins the view and resumes auto-follow.

**Compact tool-call cards.** A collapsed tool card summarizes its JSON arguments
into a few scannable `key: value` rows instead of dumping the full pretty-printed
JSON inline (issue #24) — so an MCP call carrying a huge issue/PR body no longer
buries the scrollback. Keys are ordered deterministically (high-signal intent keys
— `owner`, `repo`, `method`, `number`, `title`, `path`, … — first, then the rest
alphabetical); a short single-line string shows inline (`title: "compact cards"`),
a long or multi-line string collapses to a size + line-count + first-line preview
(`body: 5.1 KB / 72 lines · "## Context…"`; sizes are SI/decimal — `1 KB = 1000
bytes`), a small scalar array shows inline (`labels: [enhancement]`) while a longer
one collapses to `N items`, and a nested object to `N keys`. The card caps at a few
rows, and advertises **`ctrl+t` whenever anything was hidden** — a key overflow
(`… +K more keys · ctrl+t expand`) OR a collapsed value with no overflow
(`… ctrl+t expand`), in the same `…`-led shape as the result/diff line cap; a card
whose args are all short scalars (nothing hidden) shows no affordance line. An
**MCP** tool name (`mcp__<server>__<tool>`) renders a friendly `<Server> · <Tool>`
head (e.g. `GitHub · Issue write`) rather than the raw identifier; an unknown
hyphenated or long server token (e.g. `io-github-stacklok-playwright`) is shown raw
rather than mangled by title-casing. A **large JSON result** is likewise summarized to its prominent fields
(`url`, `number`, `state`, …) plus a size line; a Read/prose/line-shaped result is
unchanged (the existing line cap applies). Everything stays fully inspectable:
**`ctrl+t` expands** the card to the full pretty-printed JSON arguments and result,
and for an MCP card it also reveals the raw `mcp__…` tool name — Edit/Write keep
their colourised diff rendering, untouched.

**Layout (one model).** The frame is a vertical stack of regions — header, the
conversation body, zero or more **transient inline regions** (the slash-command
palette, the `@`-mention menu, the queued-follow-ups card), then the input and
footer.

**Speaker styling.** Each conversation turn leads with a speaker glyph so the back-and-forth
is scannable: a **user** turn is labelled **`▌ you`** with a gold left rail (no background
tint — the faint panel tint belongs only to the input box, never the conversation history);
an **assistant** turn is labelled **`● mecatl`** and renders rail-less (its markdown body
carries the weight), with a blank line between the label and the body for a little vertical
breathing room (the user block has no such gap — its gold rail visually connects label to
body). Each turn's body **hangs under its label** — the message text aligns under "you" /
"mecatl", not under the bullet — so a turn reads as a labelled block. The whole
conversation history is indented one column so it aligns with the 1-col-padded header/footer
chrome instead of sitting flush at the terminal's left edge, and turns are separated by a
clear blank-line gap so user and assistant messages read as distinct blocks. **Tool cards**
are capped at 100 columns on a wide terminal — past that the card
stops growing with the viewport so a long line stays at a readable measure — while on a narrow
terminal the card never exceeds the viewport. **Inline `code` spans** in assistant markdown are
de-emphasised (a receding, faint monospace span over the element background) so prose around an
`identifier` no longer fights it for attention.

**Input box.** The prompt textarea is wrapped in a **mode-coloured left rail** over a faint
panel tint — the same accent the `mode` segment uses (default accent / `plan` info /
`accept-edits` success), so the input's permission-mode cue reads at a glance. The rail is a
single-column left border and is the SINGLE vertical accent cue (the textarea's own inner
prompt bar and line-number gutter are suppressed), staying mode-coloured at full strength
whether the input is focused or blurred. The panel carries a tinted top-pad row inside it so the prompt isn't pressed against
the top border, and one blank spacer row sits above the panel so it isn't jammed against the
conversation history. The editor preserves a three-row minimum, grows and shrinks
with explicit newlines and soft wraps to eight rows, then scrolls internally to keep
the caret visible.

**Header bar.** `mecatui · session <handle> · <model> · mode <mode> · <server>`.
The session segment uses a fixed terminal-safe 12-column handle: safe `[A-Za-z0-9._-]`
bytes are literal except that a leading `-` is encoded as `%2D`; other UTF-8 bytes are
uppercase `%HH`, and only complete atoms that fit are shown. It has no leading `#` and
is the literal accepted by positional `mecatui debug` when it resolves uniquely.
Type **`/session`** for the safe quoted full ID and active-session metadata, or press
**`c`** there to copy the exact ID through the clipboard.
The **mode segment** shows the server-confirmed permission posture for the current session;
when a mid-turn switch has been deferred it shows `mode <target> pending` until the retry
succeeds at the next prompt boundary. The **model segment** shows the EFFECTIVE model the server resolved THIS session to —
echoed verbatim on the create response (`CreateSessionResponse.resolved_model`) and
shown from turn zero. While **connecting** (before the create response lands) there
is NO model segment — the server owns the resolved value and the client never guesses
it. The human display name is resolved from the held `/models` (ListModels) inventory
by `(provider_id, model_id)`, falling back to the raw model id when the inventory has
no entry yet (or a passthrough id). An older server that omits `resolved_model`
degrades to the pre-existing fallback (the picker's active selection, then the
launch-time `--model`). A `/models` pick switches IMMEDIATELY and rebinds the header
to the new session's effective model — there is no separate "pending-next" preview
(the conversation is always carried over; see `/models`). The header only CHOOSES
which KNOWN string to display; it never resolves a default itself.

**Operator-posture badge.** Right-aligned on the header — distinct from the per-session
`mode` segment, which is the PermissionMode — the server-wide automation posture surfaces
as a chrome badge for the allow-all tiers ONLY: **`⚠ auto`** rendered as amber inline
WARNING text, and a YOLO badge rendered as a filled **alarm-red `YOLO` pill** (the loudest
tier reads loudest). `strict`/`trusted` (and an older server that omits the posture) render
NO badge, so the common frame is unchanged. The grammar is deliberate: the `mode` segment
is inline coloured text on the LEFT (per-session permission posture), the posture badge is
a filled pill on the RIGHT (server-wide automation posture). When a scroll/changed-files
cue is also present the badge sits to its LEFT so the danger cue is never hidden by
scrolling. The pill's colours are **fixed (theme-independent)** — alarm red with near-white
text — because danger is a safety affordance, not themed decoration: it must read the same
in every theme.

On an emoji-capable terminal a plain **⚡️ lightning bolt rides OUTSIDE the pill** as
decoration immediately before it (the pill itself stays a clean `YOLO` chip — the bolt is
never inside it, so a terminal that renders the emoji in its own multicolour glyph can't
clash with the pill's text). On other terminals the bolt is omitted entirely and just the
clean pill shows. The choice comes from a conservative, env-based capability detection (no
terminal round-trip), decided ONCE at launch: a known modern terminal (`TERM_PROGRAM` of
ghostty / WezTerm / iTerm.app / Apple_Terminal / vscode, a kitty/Konsole/Ghostty signal, or
`COLORTERM=truecolor`) gets the bolt; anything unrecognised gets the bolt-less pill. Two env
overrides force it either way: **`MECATUI_FORCE_EMOJI=1`** forces the bolt and
**`MECATUI_NO_EMOJI=1`** suppresses it (and wins over force). The pill is identical either
way — only the decorative bolt comes and goes.

The badge announces *that* the posture is loud, not *what it permits*: type **`/posture`**
for the one-line summary of what the active tier actually allows (e.g. for YOLO: all-tools
auto-approve and the child prompt-injection defense off) — the badge is the at-a-glance
cue, `/posture` is its consequence.

**`/schedule` (scheduled-tasks overlay — Phase 3a, issue #234).** Gated on
`caps.scheduling` (a `ScheduleStore` is reachable on the server) AND a wired
schedule lister. It fires `ListSchedules` and renders the stored schedules as a
flat list: each row shows the name, a trigger summary (`cron: */5 * * * *` or
`one-shot: 2026-07-06 14:00`), the enabled/paused state, the next/last fire
times, and the fire count. UNLIKE `/worktrees`/`/models`, the filter input is
**not** focused on open — the panel's bare-rune action keys (`p`/`r`/`f`/`d`/`c`)
would otherwise be unreachable. `↑`/`↓` move the cursor, `home`/`end` jump,
**`/`** enters filter mode (focuses the input; matches on name + trigger
summary), and `esc`/`enter` while filtering exits it back to action mode
(blurs, keeps the narrowed value). `esc` in action mode is two-stage (clear
filter, then close). The per-row action keys are bare runes: **`p`** pause,
**`r`** resume, **`f`** fire-now (forces an immediate fire; the fire id
surfaces in the status line), **`d`** delete (opens a confirm sub-view: `enter`
deletes, `esc` backs out), **`c`** create (opens the in-overlay Create form).
On a successful action the list re-fetches to reflect the new state.
**`enter`** opens a read-only **inspect** sub-view: the full spec (trigger,
prompt preview, selector, mode, mutating, singleton, misfire, timezone,
max_fires) + the durable state (enabled, fire_count, next/last fire,
last_fire_session) + the fire records (id, fired_at, stop, err); `esc` returns
to the panel. In the inspect sub-view the fire records are cursor-navigable
(`↑`/`↓`, clamped): **`enter`** or **`t`** on a fire opens that fire's
read-only authoritative transcript. The jump-to-fire footer hint (`↑↓: select
fire  enter/t: open transcript  esc: back`) appears only when the transcript
client is wired; a fire whose `SessionID` is empty reports "fire has no session
id" and stays in inspect.

**`/session` (active session details).** This read-only overlay shows the current
chat's safely quoted full opaque ID, title, lifecycle state, bounded placement label, known creation
and modification timestamps, provider, and model. The header intentionally shows only
the compact short handle. Press **`c`** to copy the exact full ID byte-for-byte; mecatui reports
clipboard failure or a session change instead of claiming a stale copy. `esc` closes it.

**`/sessions` (session continuity).** The session inventory has four session tabs:
**Chats**, **Scheduled runs**, **Child runs**, and **Other**. When the server
advertises authenticated bounded storage health or either maintenance operation, a fifth
**Maintenance** tab appears. Its status view shows current/reclaimable availability,
aggregate bytes/files/formats/kinds/corruption, effective retention policy, sweep timing,
active-job state, and last failure without session IDs, owners, paths, or content.

The two actions are deliberately separate and independently capability-gated. **`o` Optimize
storage** starts with a read-only v1/v2/invalid/skipped and byte estimate, including required
temporary space, and states that every session is preserved. Applying starts a durable bounded
job; its screen supports status, cancel, and resume and shows progress plus bounded sanitized
per-item failures. **`x` Clean up sessions** is destructive: its read-only plan partitions
eligible and protected main/child/scheduled/unknown/live/awaiting rows, keeps unknown protected
by default, and requires typing `CLEAN UP`. The single-row `y`/`enter` delete consent is inert in
this bulk form. Apply-time stale/skipped/failed counts remain visible; retry begins a fresh dry
run against the current generation.

Closing the panel never cancels a maintenance job. Reopening refetches the server-owned durable
handle and progress. Explicit cancellation stops future items; completed migrations or deletions
stay committed. Older or unsupported servers hide unavailable actions rather than showing zero
impact. The same inventory is
the initial view for `mecatui sessions` and `mecatui connect ADDRESS sessions`;
those launch forms establish no session until the operator continues a chat or
presses `n` for a new one. At startup, `esc` quits; after opening an inspection,
`esc` returns to this inventory.
The Other tab keeps unknown legacy/custom rows inspect-only and visibly labels each
one **`Legacy session — inspect only`**. ADR 0291 removed writable legacy adoption:
there is no preflight/adopt action and no way to supply a replacement workspace or
placement authority.
`tab` switches tabs; the
search box filters the current tab. Search matches the title, full session ID,
its terminal-safe short handle, model, bounded placement label, and the available
relationship metadata (parent/call, schedule/origin, team/member). This keeps
scheduled fires and delegation children discoverable without making their IDs
part of the UI contract.

Inventory is progressive in both launch and in-chat forms. Mecatui renders the
first bounded page before it asks for the next one, then appends deterministic,
ID-deduplicated rows without changing the active tab, search text, exact-ID
selection, or scroll position. The footer distinguishes **loading more** from a
complete inventory. Press **`c`** while pages are loading to stop pagination;
**`r`** restarts after cancellation or retries a failed page. A later-page failure
keeps every row already shown. If the server reports that the cursor generation is
stale, the panel says it is restarting and replaces the old generation only when
the new first page arrives. Closing the panel or quitting the startup browser
cancels the outstanding request and never creates or rebinds a session.

Each row shows a state badge, relative modification time, turn count, title,
short handle, and model. The active chat is explicitly marked **`[current]`**;
a team member row also identifies its member. The handle is the same fixed
12-column escaped-prefix literal as the header (no `#`): safe `[A-Za-z0-9._-]`
bytes are literal except that a leading `-` is encoded as `%2D`; other UTF-8 bytes are
uppercase `%HH`, and only complete atoms that fit are retained. The full opaque ID remains
what the client sends back to the
server.

Pressing `enter` follows server-authored capabilities. A public Chat is
**Continue**: mecatui first loads the authoritative snapshot-derived
conversation, then rebinds the prompt to that session so the next text adds a
turn. Scheduled, Child, and inspect-capable Other runs are normally **Inspect**: their authoritative
snapshot transcript is displayed read-only, and the active chat is left
unchanged. A row with neither capability explains why it is unavailable (for
example, awaiting approval, active elsewhere, unavailable transcript, or
unavailable environment).

The selected row's server-authored action capabilities also drive the footer and
keys: **`y`** copies the exact opaque ID, **`v`** opens the authoritative
transcript read-only without attaching, **`f`** forks an eligible main chat and
adopts the peer only after the fork and transcript load both succeed, **`r`**
opens a prefilled title form, and **`d`** opens permanent-delete confirmation.
`esc` cancels the rename form or delete confirmation; `n` also cancels delete.
The current attached chat cannot be deleted from its own inventory row—switch
first. Rename/delete/fork are revalidated by the server, so a stale row can fail
without rebinding the active chat. Scheduled, child, active, awaiting, and
unknown rows show only the subset the server reports; hidden actions are also
rejected if invoked.

The snapshot transcript is the conversation source of truth. Durable event
replay may support live delivery catch-up, but is not used to establish a
conversation's completeness or to reconstruct it for Continue/Inspect. An
inspection is non-destructive: `esc` is **Back** to the inventory, never a
session reset or rebind. If the authoritative load fails or is incomplete,
continuation stays disabled and the transcript view offers **`r` Retry** and
**Back**.

**Create form (Phase 3b).** The **`c`** action key opens an in-overlay Create
form (peer of the inspect/confirm sub-views): fields for name, prompt, trigger
(cron OR natural-language), and a mutating toggle. Placement is inherited/resolved
server-side and no workspace or worktree selector is accepted from the model/UI. The trigger
field accepts EITHER a raw cron expression (`0 9 * * *`) OR a natural-language
phrase (`every 30 minutes`, `daily at 9am`, `every weekday at 9am`, `next
monday 3pm`, `in 2 hours`, `tomorrow at noon`) — compiled client-side by
`cmd/mecatui/schedparse` (a small stdlib-only pattern table, NOT a full NLP
engine; unmatched input falls back to raw cron). `tab`/`↑`/`↓` cycle focus
through the fields; on the mutating toggle, `y`/`n` set the value; `enter`
submits (fires `CreateSchedule`; the new schedule appears in the panel on
arrival). `esc` returns to the panel without creating. The form is a
common-path authoring surface — the REST/gRPC `CreateSchedule` API (and the
in-chat `Schedule` tool) covers the full flag surface (provider/model, mode,
max-fires, misfire, timezone, singleton, limits); the form keeps it simple.

**v1 limits.** The overlay lists/inspects/manages schedules and creates them
in-overlay (the `c` Create form + NL→cron compiler), but the gRPC/REST API
remains the full flag surface (provider/model, mode, max-fires, misfire,
timezone, singleton, limits) — the form covers the common path only. The
embedded mecatui server has the `ScheduleStore` (the overlay works —
create/inspect/pause/resume/fire-now/delete from the TUI) but **NOT** the
scheduler tick loop, so auto-firing on a cadence requires a `mecated` serving
the same store (its tick loop is ON by default on any schedule-capable store,
ADR 0073 — `--no-scheduler` opts out); `FireNow` works to trigger a schedule
manually regardless.

A single layout model (`layout.go`) is the source of truth: `View()` renders
it, the per-message relayout step sizes the viewport from it, and the mouse
selection maps clicks through it, so they can never disagree about where the body
sits or how tall it is. When a transient region appears the viewport **shrinks** to
make room and the footer stays on-screen — a transient never pushes the footer off
the bottom (and the viewport grows back when the transient clears).

**Footer usage segment.** Two different axes, deliberately: the **`ctx` meter** shows
**current occupancy** — the latest turn's prompt size (each `turn.end`'s input tokens,
assigned, not summed), i.e. how full the context window is right now. The **`↑`/`↓`/`⊕`
facets** beside it are **session-cumulative totals**, fed once per run from the terminal
result's cumulative usage (so a long session's spend keeps growing while the ctx meter
tracks only the live conversation size). The meter's **denominator** is the
**server-resolved per-model context window** (echoed on session create, refreshed on
every model switch and on `GetSession`, and — for a session on a *live-only* model whose
real window the curated catalog lacks — self-healed once the background live-catalog
swap lands: the server resolves the window *live-first at the point of use* for both the
engine's compaction trigger and this echoed denominator, so the next snapshot read fills
it in, issue #66). Embedded mode can set `--context-window-override`, which moves
the engine trigger **and** this echoed denominator together; in `connect` mode set the
same flag on the external `mecated`. Without that global override, operator-tier
`models.context_windows` exact provider/model entries precede live and catalog metadata. When the window is unknown the meter degrades to the bare current
size (`ctx 40K`, no bar) — including briefly on a fresh session for a live-only
model, until the live window resolves and the bar fills in. As the
context fills the bar **darkens** to signal pressure — `▒` ok, `▓` past ~60%, `█` plus a
`⚠` mark past ~85% — and the colour shifts to match (a non-colour glyph cue so it reads
with ANSI stripped). These bands are a visual fill gauge, **not** a compaction countdown:
the harness automatically compacts older history at ~80% full (the default trigger), so
in practice it keeps the window from running out before the `⚠` band is reached — the
`▓` warn band at ~60% is the earlier "filling up" cue, and the `⚠` may not appear at all
on a session that compacts first. One subtlety (issue #82): when a turn produces no
usage frame at all — a stalled or usage-less turn — the meter's input figure for that
turn is a conversation-size **estimate** (a heuristic token count over the live history),
not a provider-reported count, so the ctx meter stays meaningful instead of snapping to
zero. The estimate is display-only: it never feeds the session-cumulative facets or any
token budget, which stay on the provider's actual reported usage.

**Per-turn cache cue.** When a model exchange closes, an inline scrollback stat line records
its cost — `↑<in> ↓<out>` tokens, the elapsed model-call time, and (when the turn's cache-hit
rate is material, ≥10%) a `· N% cached` facet so the per-turn caching payoff is visible at the
turn it lands. The `?` keys-&-features overlay carries a usage **legend** decoding the arrows
(`↑ input · ↓ output · ⊕ cache write`) so the footer/turn-stat token glyphs are self-explanatory.

### Watching subagents, parallel runs, and teams — the fleet footer + the unified `ctrl+a` overlay

Three surfaces watch concurrent **Subagent children**, **Parallel fork-join runs**, and
**agent teams**, all built purely from the relayed `subagent.*` / `parallel.*` / `team.*`
event projection (REDACTED — bounded previews only, ADR 0079; full child/branch content
stays context-isolated):

- **Fleet status footer segments.** Once **≥1 subagent has started** this session the
  footer carries a peripheral cue — **`⛭ subagents 3◐ 1✓ · ctrl+a`** (N running ◐ / M
  done ✓). A **Parallel** run adds its own segment — **`⑂ parallel 1◐ 2✓ · ctrl+a`** —
  once **≥1 Parallel run has started**, so the fan-out is discoverable without opening
  anything. Both are built from collections fed alongside the inline tool-card routing and
  shed before the context meter as width tightens (full → `⑂ 1◐ 2✓ · ctrl+a` → `⑂ 1◐ 2✓`
  → dropped), exactly like the live-team segment; all three coexist when a session runs
  them. With no agent activity the footer is byte-identical to before. (Glyphs: `⛭`
  subagents, `⑂` parallel, `⟳` live team — distinct so they never collide.)

- **Unified `ctrl+a` agents overlay.** ONE surface with **three tabs — Subagents |
  Parallel | Teams**:
  - **Subagents** — one row per Subagent child: a state glyph (**◐** running / **✓** done /
    **✗** error), the goal label, a short `#<hash>` of the `ChildID` (so two similar
    goals are unambiguous), a **`⇢ bg` marker** on a detached (`background: true`)
    child, the current/last child tool, the running tool count, and token usage.
    `enter` focuses one child's `✓/✗` tool-chip trace with bounded previews (args/results
    shown capped + control-byte-scrubbed — client-only; gauntlet #7, ADR 0079); a background child's focus pane adds an
    honest delivery line — *running detached* vs *done — result ready for the agent
    (SubagentStatus)* — and never claims a collected/uncollected state (the registry's
    `delivered` flag is not on the wire; the events carry only background + done). The
    roster is windowed (pgup/pgdn, home/g·end/G, `+K above/below` tails) like the team
    roster.
  - **Parallel** — a Parallel run is a **GROUP**, not a flat fleet, so the roster lists one
    row per Parallel call: a state glyph (**◐** running / **✓** done), the **join strategy**
    (all / first / judge), the running/total **branch tally**, and the **winner** branch
    once a first/judge run resolves. `enter` focuses ONE group (one level — plan Q4),
    showing **all its branches inline** with the **winner row highlighted (★)**, the
    **run-level stop** (`run stop: <label>`, when a first/judge run resolves one — a
    join=all run carries none, so the line is omitted), and an opaque preserved-artifact
    handle when one exists. Each branch row carries its own glyph
    (**◐**/**✓**/**✗** failed), label, goal, current/last tool (or, once done, its stop
    label and **wall-clock duration**), count, and usage. Branch args/results render as
    bounded previews (capped + control-byte-scrubbed — client-only; gauntlet #7, ADR 0079).
    Artifact/delegation handles are typed inspection capabilities, never filesystem paths,
    exact placement refs, or worktree selectors; each use is owner/scope reauthorized.
  - **Teams** — the existing agent-team roster + per-member focus + task / findings
    sub-views, verbatim. The tasks sub-view rows show the truncated task **description**
    between the id and state (a description-less task keeps the compact id/state row).
  - `tab` cycles tabs (Subagents → Parallel → Teams); `esc` steps back from a focus pane
    to its roster, then closes. The **default tab is context-sensitive** (precedence:
    team live → Teams; parallel live → Parallel; subagents ran → Subagents; parallel ran →
    Parallel; team ran → Teams). `/team` opens the same overlay pinned to the Teams tab.

  A subagent that ended via a non-`end_turn` terminal renders a sensible label — the
  newer Subagent/Team stop reasons (`budget` → "budget", `structured_output` → "schema",
  `no_progress` → "no-progress", the `max_*` limits → "max-turns"/"max-tools") ride the
  same string `stop` field on the wire (no proto enum) and map to compact labels; the
  cap-family stops read as **✓** (a partial is still usable), error/cancel-family as **✗**.
  The main footer uses the same stop family: `budget` renders as **`stopped · token budget`**,
  parallel to the turn/tool-call limit labels.

**Advisory vs durable notices.** A **compaction** boundary (`compaction` event) is a
durable fact, so it lands as a muted **scrollback notice** that stays in the transcript.
A **no-progress** notice (`no_progress` event — a nudge or the terminal give-up) is
instead routed to the **transient footer status**: a successful nudge-recover must leave
no permanent residue, and the *terminal* no-progress stop is already conveyed durably and
independently by the run's `ResultMsg` → the footer label **`stopped · no progress`**. So
no-progress never enters scrollback; during an active run it is effectively silent (the
spinner already shows liveness), surfacing at most as a brief idle footer status. A
**recover-notice** (`recover_notice` event — a pre-flight advisory when a
permanently-failed session is recovered) is also transient: a warning-coloured
status line that fires ONCE before the first turn, so the user sees it before
burning a provider call. No
advisory-vs-terminal proto field is needed — the durable terminal signal rides
`ResultMsg`. A **background subagent finishing** (`subagent.end` on a `⇢ bg` lane) rides
the same transient channel — *"background subagent #<hash> done — result ready for the
agent"* — because the durable outcome is the **agent's** to collect (`SubagentStatus`),
not this client's; a foreground end stays silent (its result already landed on its own
Subagent card). The fleet footer counts (`⛭ 2◐ 1✓`) include detached background children
until their `subagent.end` arrives, however many turns later that is.

The **mouse wheel** is only active on the alternate screen (the default full-screen
TUI). With `--inline` / `--no-alt-screen` the terminal's own scrollback and native
selection are left untouched (no mouse capture).

**Prompt selection (alt screen).** Click directly on prompt text to place the caret;
drag to select from that anchor, with the mapping following the textarea's soft-wrap
and scroll state. `shift+arrow` and the textarea's upstream keyboard selection work
too. The selected range is rendered visibly and is the target for typing, text paste,
newline insertion, and deletion. Prompt mouse release does **not** copy. Prompt
selection survives permission prompts, overlays, and other non-content changes; a
prompt-content mutation clears it. Starting a conversation selection clears prompt
selection, and starting prompt selection clears conversation selection, so exactly one
surface owns a selection at a time.

**In-app text selection + copy (alt screen).** On the alt screen the app captures
the mouse, so it provides its **own** text selection: **left-click-drag** over the
conversation highlights the runes under the drag (press = anchor, drag = extend,
release = finalize). The highlight is rendered **by the app** — the selection
style is spliced into the conversation's own content lines before they reach the
viewport, **not** the viewport's native highlighter (which mis-placed the block on
ANSI-styled markdown content, painting it on the wrong line). It is a **solid
high-contrast block**: the selection's own ANSI is stripped and re-rendered with a
dedicated selection background plus a foreground chosen by the background's relative
luminance (near-black on a light theme, near-white on a dark one), so the block is
legible on every built-in theme — light (`solar`) and dark (`aztec`/`mono`) alike.
(It is **not** reverse-video, which was invisible over already-coloured content.)
The block is **glyph-bounded** — it stops at each line's last glyph rather than
filling the terminal width, so multi-line selections have a ragged right edge; an
empty line spanned in the **middle** of a multi-line selection now paints **one
cell** so the run stays solid through it. The highlight is **logical** — it
survives scrolling (wheel,
`pgup`/`pgdn`, `home`/`end`) and a streaming re-render. The selection
background is the optional **`selection`** palette slot; a theme that omits it
derives the block from its **`accent`** colour, still with a luminance-correct
foreground. Dragging to the **top or
bottom edge** of the conversation **auto-scrolls** the view in that direction and
keeps extending the selection over the newly-revealed lines (so you can select more
than one screenful) — it scrolls continuously while you hold at the edge, **ramping
up** (it starts one line at a time, then accelerates the longer you hold, capped so a
single step can never leap a full screen), and stops at the content top/bottom. On
**release** the visible
selection is copied (the default is copy-on-select): the ANSI styling and the
gutter/right-padding are stripped, multi-line selections join with `\n`, and the
footer/status confirms with a muted **`copied · N chars · M lines`**. While a
selection is active (and the view is idle) the footer-left shows a **live
`N chars · M lines`** count; after a copy it becomes **`copied · N chars · M
lines`** and the selection persists, so the confirmation rides alongside the
still-live count rather than replacing it. The count is shown only at idle — never
while a run streams, a permission ask is open, or the client is connecting. A **double-click** selects
the **word** under the cursor (a maximal run of word characters, whitespace, or
punctuation) and a **triple-click** selects the **whole logical line** — both
highlight and copy immediately, just like copy-on-select; a fourth click at the same
spot cycles back to a plain anchor. A **right-click** or **`ctrl+shift+c`** copies
the active prompt or conversation selection; with no selection either action is a
no-op. Conversation selection still copies on release; prompt selection does not.
**`esc`** clears an active selection **before** its other
meanings (cancel a running turn or close an overlay); with no selection, it cancels
an active run directly while preserving the draft, queued follow-ups, and steer. At
idle it leaves the draft intact; the paused-queue case clears only that queue.
Conversation selection is **blocked** while
an overlay/modal owns the screen (permission ask, `/mcp`, `/team`, `/agents`,
`/skills`, `/soul`, `/usermodel`, `/models`, help, the fatal screen) — a press there
starts nothing, and opening an overlay clears an in-progress conversation selection
while stopping a prompt drag without changing an already completed prompt selection.
An existing prompt selection is preserved through those non-content changes. The wheel
still scrolls while a selection exists, without clearing it.

The copy uses **OSC52** (`tea.SetClipboard`) as the primary path and **also**
mirrors the payload into the platform clipboard binary as a best-effort fallback —
**wl-copy** (Wayland), **`xclip -selection clipboard -i`** (X11), **pbcopy**
(macOS), **clip** (Windows). The shell write is best-effort: if no binary is present
the OSC52 copy still carries the selection, and a failed shell write is never
surfaced as an error.

**`--no-mouse`: native selection instead.** In-app mouse selection and the mouse
wheel exist only because the app captures the mouse — and Bubble Tea has no wheel-only
mouse mode, so capturing it is what *prevents* the terminal's own click-drag selection.
`--no-mouse` disables mouse capture and all in-app mouse gestures, restoring native
click-drag selection and native middle-click paste. Keyboard prompt selection remains
available. Capturing the mouse also suppresses the terminal's native
**middle-click primary-selection paste**, which is why the app performs it in-app
(see the keys table); `--no-mouse` restores the native middle-click paste along
with native selection, and **`shift+middle-click` always performs the
terminal-native paste** even with the mouse captured (most terminals pass
shift-modified clicks through). On a Wayland system **without wl-clipboard
installed** and a terminal that blocks OSC52 reads (e.g. Ptyxis/VTE), the in-app
middle-click paste has no working backend and silently does nothing — install
wl-clipboard, or use `shift+middle-click`. If you'd rather use your terminal's native
selection (e.g. on a
multiplexer or web terminal that strips OSC52, where neither the OSC52 nor the
shell-write copy reaches your clipboard), pass **`--no-mouse`** (or set
**`MECATUI_NO_MOUSE=1`**). It keeps the alt-screen TUI but leaves the mouse
uncaptured, so click-drag selection is handled by your terminal again — at the cost
of in-app mouse-wheel scroll and the in-app drag-select/copy layer. Keyboard scroll
(`pgup`/`pgdn`, `home`/`end`) is unaffected. (`--inline` / `--no-alt-screen`
likewise leaves the mouse uncaptured.)

**Troubleshooting — `MECATUI_DEBUG_MOUSE`.** If selection or click mapping looks
off (a highlight on the wrong line, a click that lands a row away), set
**`MECATUI_DEBUG_MOUSE=1`**: the footer-left is overridden during a press/drag with a
live diagnostic — the raw mouse cell, the layout offsets (`top` = conversation top
row, `yoff`, viewport height), the `screenToContent` mapping (`ok`, logical `L`/`C`),
and the prompt-text hit bounds. It is off by default (zero cost when unset).

### Type-while-running and queued follow-ups

The input stays **focused while a run streams**, so you can compose the next
request without waiting. Pressing `enter` mid-run **steers** the (trimmed,
non-empty) line when the server supports steering; otherwise it queues the line
instead of starting a second concurrent run. The fallback queue is capped at 16; an
over-cap `enter` is rejected with a muted `queue full (16)` status and the input is
kept. A muted card above the input shows `⏳ N queued · ↑ edit` with up to three previews (`+K more` over that).

When the run ends on a **healthy** stop, the whole queue is **merged into one prompt**
(the staged lines joined by a blank line) and submitted through the ordinary prompt
path (so it reopens the session server-side exactly like a manual follow-up); the
queue empties in a single step. A healthy stop is one where the model was *done* or
merely hit a *size bound* — `end_turn` (and the empty reason), **plus** the per-run
limits `max_turns`, `max_tool_calls`, and `budget` (the run just ran out of
turn/tool/token budget; firing the merged prompt reopens it with a fresh budget, which
is what a lined-up "continue" wants).

A terminal provider failure is classified by two presence-aware fields: retry
disposition (`unknown`, `retryable`, or `permanent`) and stream progress
(`unknown`, `precommit`, `visible`, or `complete`). Mecatui starts **one** automatic,
prompt-free failed-step retry only when a new server explicitly reports
`retryable + precommit`. It sends a fresh `Converse` stream whose first frame is
`RetryStart`, not another Prompt, so the original user message is not duplicated.
The textarea and queued future prompts stay untouched. If that retry succeeds, the
existing healthy queue drain resumes.

An absent typed field (an old server) or explicit `unknown` pauses and preserves
the queue. `/retry` is available for every bound idle session and sends `RetryStart`;
the server authoritatively accepts or rejects eligibility from durable state. It adds no
Prompt or user card and does not reset the textarea or mutate queued prompts. Visible
output is never retried automatically. Retry transport failures and clean pre-turn
brakes preserve the manual affordance and keep queued work paused; only `turn.start`
proves an authoritative model attempt. Persisted conversation/tool state is reused, but
live turn-0 instructions, operator profile, and system prompt are re-resolved.

A **permanent** provider error, such as a non-retryable 4xx rejection or context-window
overflow, renders a ONE-LINE summary block
(`✗ <first line, ≤120 runes>: retrying won't help; the request is rejected. Start a
new session or /clear.`) instead of a raw error block. The raw error payload is
available on `ctrl+t` expand under a dim `raw payload:` header. A permanent error is
never auto-retried. When a session that failed permanently is recovered for a new
prompt, a transient `recover_notice` warning line appears before the first turn.

A paused retry, hard error, user cancel, `max_consecutive_failures`, or stream close
**pauses and keeps** the queue, so a broken run or deliberate cancel never silently
fires the backlog. The card switches from the muted `⏳ N queued · ↑ edit` to a
louder `⏸ N queued · paused: <reason>` with the resume/edit/clear keys, so a held queue
is never mistaken for a hang. From there (idle), `enter` on an empty line **resumes**
(sends the merged queue), `↑` on an empty line pulls the merged queue back into the
input for **editing** (non-destructive), and `esc` **clears** the queue; sending a
fresh prompt also clears the pause and lets the queue drain at that run's clean end.

A bare built-in typed mid-run is handled locally **immediately**: an available
builtin runs, while an unavailable one retains its local warning. Neither is queued
or sent to the model. `/clear` retains its running-state warning instead of clearing
an active conversation. `/clear` itself also empties any queued follow-ups when it
runs while idle.

Queueing is **running-only**: while a permission modal is open the modal keys own the
keyboard unchanged (no mid-approval queueing).

### Steer mode (mid-run steer, when the server advertises it)

When the server's engine arms the mid-run **steer inbox** (steer-while-running, issue
#512 — ON by default; `mecated --no-steer` / `mecatui --no-steer` or the operator-tier
`steer: false` settings.yaml key opts out), the `CreateSession` capabilities echo
carries `steer: true` and the TUI **flips** mid-run input from the local terminal
queue to the engine steer path:

- `enter` mid-run sends a `steer` frame on the live Converse stream instead of
  staging locally, **except that a bare recognized TUI built-in** (such as `/help`
  or `/clear`) still runs locally. Unknown slash commands and workspace commands
  remain model-facing input. Recognized built-ins with arguments retain the input
  and show a local argument warning. The same attachment preparation used by an
  idle prompt also runs here: `@` mentions, staged image markers, aggregate caps,
  marker stripping, mixed text+media, and media-only input all retain their
  ordinary prompt semantics. Each steer mints a fresh client `message_id`; the
  frame carries that fragment's text and media parts. The engine's single-slot
  inbox **appends** each frame into the one pending bundle (merged with a blank-line
  separator) and drains the bundle at the **next turn boundary**, recording it as an
  ordinary user continuation — so the model is nudged *mid-flight*, no waiting for
  the run to end. The TUI keeps an **ordered queue of sends** (id + text); the drain
  echo carries the **watermark** (the latest contributing send's id) and the queue
  splits on it: everything up to and including the watermark landed (it renders in
  context), anything after stays pending.
- The card above the input reflects the **authoritative** server-reported state —
  the engine is the sole authority on what happened to a steer (the client cannot
  observe the exact drain moment across stream latency), so the card shows what
  the server acked/echoed, never a client-side guess: `⏳ steer: sending…` (sent,
  ack in flight) → `⏳ steer queued · ↑ edit · esc cancel` (acked, parked for the
  next boundary) → `↪ steer sent as a follow-up (run had already ended)` (a
  **too-late** race: the run had already gone terminal, so the text was
  auto-promoted to a fresh follow-up run — never silently dropped) or
  `✕ steer retracted` (a `steer_cancel` won). A second `enter` while a bundle is
  parked **appends** to it server-side (the bundle drains as ONE merged message);
  replacing a pending bundle is the explicit `↑`-cancel-then-recompose below. The
  rendering is **queued-until-landed**: the pending card sits at the bottom of
  the transcript, and the **drain echo** clears it as the committed steer appears
  IN CONTEXT at its true position (an ordinary user turn at the boundary it
  landed) — never rendered twice.
- `↑` (empty input, in-flight steer) is **cancel-then-recompose**: it issues a
  `steer_cancel` for the outstanding bundle (the watermark id) and pulls the
  queue's pending sends back into the input as ONE editable blob; resending sends
  the edited blob as a fresh fragment under a NEW `message_id` (an already-drained
  send is never re-sent — the watermark split keeps the queue honest). A late
  `none_pending` ack means the drain won — the steer shipped as sent. Escape does
  not retract a pending/queued steer: after selection-clear precedence it cancels the
  running turn directly and preserves the steer, draft, and queued follow-ups. Text
  and attachment bytes share this lifecycle: `↑` restores both to the draft, the drain
  watermark releases the landed prefix, and a successful retract drops both.

When `steer` is **false** because the feature is runtime-disabled, none of this
engages: all mid-run input, including attachment bytes, stays in the #228 local
merge queue (staged, merged, and drained as one marker-free follow-up prompt when
the run ends), and no `steer` frame is ever sent.

## Theming

Themes are pure data: a `Palette` of semantic colour slots (e.g. `accent`,
`error`, `mdHeading`, `synKeyword`) from which all lipgloss styles and the
glamour markdown/code style config are derived. Three themes ship built in:

- **aztec** (default): jade/turquoise + gold on obsidian, terracotta errors.
- **mono**: neutral greyscale with a blue accent.
- **solar**: a warm light-leaning (solarized-ish) variant.

### Custom themes

Drop a JSON file into one of these directories (increasing precedence):

1. `$XDG_CONFIG_HOME/mecatui/themes/` (or `~/.config/mecatui/themes/`)
2. `<workspace>/.mecatui/themes/` (the `--workspace` root)
3. `<cwd>/.mecatui/themes/`
4. the `--theme-dir` directory

Each file is `{ "name": "...", "palette": { ...slots... } }`. The palette is
**merged over the Aztec base**, so a partial theme only needs the slots it wants
to change:

```json
{
  "name": "midnight",
  "palette": {
    "accent": "#7C5CFF",
    "primary": "#5CC8FF",
    "error": "#FF5C7C"
  }
}
```

Select it with `--theme midnight` (or set `theme` / `MECATUI_THEME`).

### Light/dark auto-detect (ADR 0280)

Out of the box, mecatui detects a light terminal background and switches to the
built-in **solar** theme automatically — no flag, no config. On startup, when
no explicit `--theme`/`MECATUI_THEME` was given AND stdout is a real terminal,
`Init` batches Bubble Tea v2's `tea.RequestBackgroundColor` command (an OSC 11
query); the terminal's asynchronous reply arrives as a
`tea.BackgroundColorMsg`, and a light response (`!msg.IsDark()`) switches every baked theme consumer —
`Deps.Theme`, the renderer's glamour/block/join caches, and the spinner style —
to `solar`. A dark response, no response at all (many terminals or
multiplexers don't answer OSC 11), or redirected/piped stdout all leave the
default **aztec** theme untouched. Only the FIRST response acts; a duplicate or
late one (a misbehaving terminal) is a no-op.

An explicit `--theme`/`MECATUI_THEME` always wins and skips the detect
entirely — including `--theme aztec`, which pins the default rather than
leaving it to auto-detection. There is no separate opt-out flag; pinning the
theme IS the opt-out.

## Architecture & testing

- `cmd/mecatui/client/` — touches `contracts/gen` + grpc: dial, `CreateSession`,
  the `Converse` stream wrapper (serialised sends), the reader goroutine, the
  `Event → tea.Msg` mapper.
- `cmd/mecatui/embed/` — hosts the embedded server: `embed.Start(ctx, app.Config)`
  builds the harness via `internal/app` and serves it over a UNIX socket. The only
  TUI package besides `client`/main that imports `internal/...` + grpc.
- `cmd/mecatui/ui/` — the Bubble Tea model/update/view + renderers. Imports
  `client` and `theme` only; never `contracts/gen` directly.
- `cmd/mecatui/theme/` — pure styling: palette, derived styles, glamour config,
  registry, JSON loading. No `contracts/gen`, no `ui`, no grpc.

**The emoji width-method invariant.** Assistant markdown is wrapped by glamour and
painted by Bubble Tea's differential renderer, and the two measure cell width with
DIFFERENT methods: glamour wraps on GraphemeWidth (a VS16 selector promotes a
cluster to width 2), while the renderer paints on WcWidth on any terminal that does
not confirm DEC mode 2027 (Apple Terminal, most SSH sessions). When the two
disagree on an emoji cluster, every cell to its right is offset and the line
scrambles ("mecatl" → "mec##atl"), persisting after the stream settles. `render.go`
normalises emoji presentation (`normalizeEmojiWidth`, strips VS16 + collapses any
residual divergent cluster) BEFORE glamour, so for every rendered line
`WcWidth == GraphemeWidth` — the two layers agree without relying on the terminal
upgrading the renderer. The invariant is guarded by
`TestMarkdownWidthMethodAgreement`. `trimTrailingSpaces` and the reserve-final-column
wrap are retained as harmless hygiene, not the fix.

**Streaming render coalescing.** Streamed assistant/reasoning deltas arrive far
faster than the eye can see, and a full conversation re-render per token would
re-run glamour on the live (growing) block every token — O(n²) over a turn. So a
delta only appends to the conversation and marks the view dirty (`m.viewDirty`); it
does NOT re-render. The first delta of a burst arms a single one-shot frame-cadence
tick (`renderTickMsg`, ~16ms ≈ one 60fps frame, guarded by `tickArmed` so a burst
schedules exactly one tick, not one per delta); the tick flushes the dirty view,
disarms, and re-arms only if more deltas arrived — so it idles to zero when the
stream goes quiet and never free-runs. Every turn/tool/result/error boundary still
force-flushes (via `afterEvent`/`endRun`, both of which call `refreshView`, which
clears `viewDirty`), so no flush depends on the tick: a dropped or late tick can
never lose the tail, and the final frame and event ordering are unchanged — only the
per-token re-render churn is coalesced. This does NOT touch the emoji
width-normalization path above.
The `renderer.mdRenders` counter (incremented only at the real `glamour` call site)
is the test seam: N coalesced deltas leave it unchanged, one flush bumps it by one
(`coalesce_test.go`).

**Per-block render cache.** On top of the coalescing sits a per-BLOCK render
cache (`renderer.blockCache`):
each scrollback block's full rendered string is memoized keyed on the block's
render revision (`block.rev`, bumped by the conversation's mutation gateways),
the wrap width, and the ctrl+t expand toggle. On every flushed frame, settled
blocks join the conversation string straight from cache — only blocks whose
rev/width/expand changed re-render (in practice just the live tail block), so
the per-frame styling cost is O(changed blocks) rather than O(scrollback). The
`renderer.blockRenders` counter (incremented only on a cache miss) is the test
seam, and the cache-equivalence oracle in `render_cache_test.go` proves the
cache is output-invisible after every conversation mutator. Both per-block
caches (`blockCache` and the inner assistant-glamour memo `blockMD`) are dropped
when the conversation is rebuilt — `/clear` and the `/models` seamless-switch
handoff — because a rebuilt transcript reuses block indices.

**Input render memoization.** The INPUT region got the same treatment (issue #45):
the bubbles textarea's `View()` re-wraps (and SHA-256-keys, even on its internal
cache hits) every buffered line on every call, and `renderInput` runs at least
twice per reduced message (the `relayout` chokepoint's `chrome()` plus `View`'s
`assembleLayout`) — so a big input buffer taxed every streamed-delta frame and
every keystroke. `renderInput` now memoizes the rendered string on a single-entry
cache (`renderer.inputKey`/`inputView`) **keyed on state** — the buffer value,
cursor position (logical row + soft-wrap row/column offsets), focus, and box
dimensions — rather than dirty-flagged: the textarea is mutated from ~30 ui call
sites and a missed dirty-set would freeze the input, while the key is
self-validating. The textarea's only un-keyed state (its internal scroll offset;
the virtual cursor's blink phase — static in mecatui, since `cursor.BlinkMsg` is
never routed to the textarea) can change only alongside a keyed fact in the same
reducer step, and `renderInput` re-keys on every step, so the single entry can
never serve stale (see `renderInput`'s doc). Paired with the **large-paste
placeholder staging** (`[Pasted text #N]`, see "Large text pastes" above) that
keeps the buffer — and thus the key compare — small, this removes the
paste-induced keystroke lag end to end. Guarded by the `TestRenderInput*` cases
in `paste_large_test.go` (cache hit, edit/cursor/focus invalidation).

**Spinner tick phase-gate.** The footer spinner's bubbles tick chain is
self-perpetuating (every `sp.Update` returns the next tick cmd), so the reducer
drops `spinner.TickMsg` in any phase where the spinner is not rendered
(`spinnerVisible`: only `phaseRunning`/`phaseConnecting`) — terminating the chain
instead of re-rendering the whole screen at 10fps forever. Every transition INTO a
visible phase re-arms `m.sp.Tick` (submit, approval resolve, ask retraction,
restart/retry, model-switch restart; bubbles' id+tag dedup makes the blanket
re-arm safe). Together with the coalesced render tick above, an idle mecatui
performs zero Update→View cycles (`spinner_gate_test.go`).

Tests are fully offline and deterministic: the stream is driven from a scripted
fake behind the `Recv()` interface (no gRPC, no network), and whole-program /
View goldens are captured with teatest at a fixed terminal size. Refresh the
goldens with:

```sh
task test:golden     # go test ./cmd/mecatui/ui -update, then re-run
```

## See also

- [Usage & operator guide](usage.md) — the `mecated` server `mecatui` dials (or embeds), and every server flag.
- [Architecture guide](architecture.md) — the event stream and gRPC `Converse` surface this client renders.
- [UX discoverability design](adr/0025-ux-discoverability.md) — the rationale behind the capability-wiring approach this UI takes.
- [Clipboard image paste design](adr/0026-clipboard-image-paste.md) — the non-obvious decisions behind `ctrl+v`.

## Container image / brood-box

`mecatui` ships as a container image on every release, alongside `mecated`:
`ghcr.io/stacklok/mecatl/mecatui` (tagged `<version>` and `latest`, multi-arch
`linux/amd64` + `linux/arm64`, signed with cosign + SBOM + SLSA provenance —
the same supply-chain story as the `mecated` image; see the release workflow
in `.github/workflows/README.md`). It is built with ko from `./cmd/mecatui`
onto the digest-pinned brood-box wolfi base (`baseImageOverrides` in
`.ko.yaml`) and carries a
brood-box agent manifest at `/var/run/ko/agent.yaml` (from
`cmd/mecatui/kodata/agent.yaml`), located via the OCI config label
`org.stacklok.broodbox.agent`.

Import it into brood-box:

```sh
bbox agents import ghcr.io/stacklok/mecatl/mecatui:latest
```

The manifest forwards `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, and `OPENCODE_API_KEY` and allows egress to all four provider
endpoints; mecatl auto-detects the provider from whichever key is set. Edit the
manifest for a deployment that pins a single provider or a stricter egress
profile.

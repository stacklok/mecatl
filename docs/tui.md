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
# Embedded server (default): mecatui hosts mecated in-process over a UNIX socket.
# The provider is AUTO-DETECTED from whichever key is set:
ANTHROPIC_API_KEY=sk-ant-... bin/mecatui --workspace "$PWD" # Anthropic (Claude)
OPENAI_API_KEY=sk-...      bin/mecatui --workspace "$PWD"   # OpenAI
OPENROUTER_API_KEY=sk-or-... bin/mecatui --workspace "$PWD" # OpenRouter

# Offline, no network — uses the canned mock provider:
bin/mecatui --mock --workspace "$PWD"
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

To use a specific **external** server instead, use the `connect` subcommand:

```sh
bin/mecated serve &                                    # listens on 127.0.0.1:8080
bin/mecatui connect 127.0.0.1:8080 --workspace "$PWD"
```

`--workspace` defaults to the current directory and is always resolved to an
absolute path (the server requires absolute).

### Transport commands

The transport is exactly what the invocation says — there is no implicit probe
or fallback:

- **Bare `mecatui [flags]`** — always host an embedded `mecated` in-process over
  a private UNIX socket; **never probe** loopback, **never dial**. All
  embedded-server flags (`--mock`, `--trust-project`, provider knobs, …) apply,
  and the remote-only flags (`--auth-token`, `--tls`, `--tls-ca`, `--insecure`)
  are **rejected** here.

- **`mecatui connect ADDRESS [flags]`** — always dial a running `mecated` at
  `ADDRESS` (host:port); **never probe** loopback and **never embed** — the
  target must already be serving. Embedded-server flags (`--mock`,
  `--trust-project`, provider keys, …) are **rejected** here — only shared
  session/UI flags and remote flags (`--auth-token`, `--tls`, …) apply.

  ```sh
  bin/mecated serve &                                 # listens on 127.0.0.1:8080
  bin/mecatui connect 127.0.0.1:8080 --workspace "$PWD"
  bin/mecatui connect mecated.internal:443 --tls --auth-token $MECATL_AUTH_TOKEN
  ```

`ADDRESS` must immediately follow `connect`; a missing or flag-first `ADDRESS` is
a usage error, with one carve-out: `mecatui connect --help` renders the connect
help instead of the missing-ADDRESS usage error. An unknown leading command fails
closed.

> **Removed flags:** the three `--subagent-ask-reviewer*` flags were inert under
> `mecatui` (it runs interactive — a child ask surfaces to the approval modal, not
> the headless reviewer) and have been removed. They are now unknown-flag errors.
> To use the headless ask reviewer, run a headless `mecated serve --headless
> --subagent-ask-reviewer …` and point `mecatui connect` at it. The `--model-slot
> ask-reviewer=…` model slot is unaffected.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--workspace` | cwd | absolute session workspace root |
| `--mode` | `default` | permission posture: `default` \| `plan` \| `accept-edits` |
| `--theme` | `aztec` | theme name (also `MECATUI_THEME`) |
| `--theme-dir` | – | extra directory of `*.json` themes to load |
| `--auth-token` | – | bearer token for an **external** server (or `MECATL_AUTH_TOKEN`) |
| `--tls` | off | use TLS transport for an **external** server |
| `--tls-ca` | – | PEM CA bundle for external-server verification |
| `--insecure` | off | skip TLS verification (testing only) |
| `--list-themes` | – | print available themes and exit |
| `--inline` / `--no-alt-screen` | off | render inline in the terminal's normal buffer instead of the alternate screen, preserving native scrollback/search (no mouse capture; see `--no-mouse` below) |
| `--no-mouse` | off | keep the alt screen but don't capture the mouse, so the terminal's **native** click-drag selection works; trades away in-app wheel scroll + drag-select/copy (or `MECATUI_NO_MOUSE=1`; see the selection section) |
| `--terminal-title` | `on` | dynamic terminal window/tab title: `on` shows `<session title> — <status word> mecatui` (the title is the first prompt, the status word reflects the phase); `off` collapses to the bare `mecatui` (escape hatch for terminals/multiplexers where a set title does more harm than good). Accepts `on`/`off`/`true`/`false`/`1`/`0` (or `MECATUI_NO_TERMINAL_TITLE=1`; see the terminal title section) |
| `--no-banner` | off | disable the first-run welcome **splash** (mascot + gradient wordmark); the plain prompt hint + affordance list still show. Auto-forced on under `--quiet` or a non-interactive stdin |
| `--model` | – (provider default) | model id for the **embedded** server; empty = the server-configured `--default-model` (when set), else the provider-appropriate built-in (anthropic → `claude-sonnet-4-6`, openai → `gpt-5`, openrouter → `openai/gpt-5`). Overridden per session by the `/models` picker |
| `--default-provider` | – | **embedded** server: deployment-wide default provider id (e.g. `openai`, `openrouter`, `anthropic`); overrides the built-in provider preference for zero-selector sessions, while a client-side selection still wins. An unknown/unavailable provider **fails startup** |
| `--default-model` | – | **embedded** server: deployment-wide default model for the default provider; sits below client-side defaults and above the per-provider built-in. A model not catalogued for the default provider **fails startup** |
| `--subagent-model` | – (inherits `--model`) | **embedded** server: global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model (the `CLAUDE_CODE_SUBAGENT_MODEL` analogue); the Parallel judge stays on the session model. Same provider as the session; an unresolvable id **fails startup** |
| `--anthropic-base-url` | – | native Anthropic API base URL override for the **embedded** server (compatible/proxy endpoints; key from `ANTHROPIC_API_KEY`) |
| `--openai-base-url` | – | OpenAI base URL override for the **embedded** server |
| `--openrouter-base-url` | – | OpenRouter base URL override for the **embedded** server (default `https://openrouter.ai/api/v1`) |
| `--auth-file` | – (auto) | **embedded** server: path to a YAML credentials file (`providers.<name>.api_key`); overrides the conventional default `$XDG_CONFIG_HOME/mecatl/auth.yaml` (a `settings.yaml` sibling). An environment variable always wins over this file for that provider — see [`mecated`'s credentials-file docs](usage/mecated.md#credentials-file-authyaml) for the schema and precedence |
| `--mock` | off | **embedded** server: use the offline mock provider (no network) |
| `--no-bash` | off | **embedded** server: disable the Bash tool (shell-less) |
| `--memory-dir` | – (auto) | **embedded** server: per-project memory store dir; empty = a default under `$XDG_DATA_HOME/mecatui/memory` |
| `--no-memory` | off | **embedded** server: disable cross-session memory (Remember/Recall) |
| `--store-dir` | – (auto) | **embedded** server: durable JSONL session/event store dir; empty = a per-workspace default under `$XDG_STATE_HOME/mecatui/sessions`, so sessions survive restart. **Privacy:** stores the raw conversation (prompts, model output, tool args/results) in **plaintext**; the dir is created mode `0700` (owner-only) |
| `--no-store` | off | **embedded** server: disable the durable session store (use an in-memory store, persisting nothing to disk) |
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
| `--user-model-review` | off | **embedded** server: opt-in background user-model reviewer — after a session stops, a fresh single-shot child extracts durable operator facts via RememberUser (spends tokens, hence off) |
| `--user-model-review-interval` | 1 | **embedded** server: session-count debounce for `--user-model-review` (1 = every session) |
| `--trust-project` | off | **embedded** server: honour a discovered project's permission **ALLOW** rules **and** its project soul (`.mecatl/soul.md`). Default OFF, unified with `mecated` — deny/ask are always honoured regardless. Only pass it for a repo you trust |
| `--yolo` | off | **embedded** server: OPERATOR POSTURE (dangerous) — suppress permission prompts for the built-in mutate-ask floor, for ephemeral/sandboxed use only. A configured deny/ask in any scope still applies. Refused as root unless `MECATL_SANDBOX=1` (or `IS_SANDBOX=1`) |
| `--quiet` | off | discard the embedded server's operational diagnostics instead of writing them to `$XDG_STATE_HOME/mecatl/mecatui.log` (see the diagnostics note below) |
| `--perf` | off | **embedded** server: expose the loopback perf admin surface (`/metrics`, `/debug/pprof`, `/debug/vars`, `/debug/flightrecorder`) and wire domain metrics. Loopback, UNAUTHENTICATED |
| `--perf-addr` | – (`127.0.0.1:9099`) | **embedded** server: admin listen address for `--perf`. Empty = the **fixed** `127.0.0.1:9099` (predictable, so an MCP-client config can hardcode the `/mcp` URL; distinct from `mecated`'s `:9090`). Pass another `host:port`, or `127.0.0.1:0` for an ephemeral port. On a clash, startup **fails with guidance** |
| `--perf-goroutine-warn-threshold` | 0 (off) | **embedded** server: arm the live goroutine-leak watchdog — Warn whenever the goroutine count exceeds this; the `/metrics` goroutine series is exported regardless. Only consulted with `--perf` |
| `--perf-mcp` | off | **embedded** server: mount the read-only perf MCP server at `/mcp` on the `--perf` surface (introspect this process over MCP). Refuses a non-loopback `--perf-addr` |

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
an immediate hard exit (`os.Exit(130)`).

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
| `MECATUI_NO_MOUSE` | don't capture the mouse (same as `--no-mouse`) — native terminal selection over in-app wheel/drag |
| `MECATUI_NO_TERMINAL_TITLE` | suppress the dynamic terminal window/tab title (same as `--terminal-title=off`) — collapse to the bare `mecatui` |
| `MECATUI_DEBUG_MOUSE` | overlay raw mouse coords / click-mapping in the footer during a press/drag (troubleshooting) |
| `MECATUI_FORCE_EMOJI` / `MECATUI_NO_EMOJI` | force / suppress the emoji glyph for the YOLO posture badge (force-on, no-wins-over-force); default is conservative env-based detection (see the posture badge) |
| `MECATUI_FORCE_KITTY` / `MECATUI_NO_KITTY` | force / suppress the Kitty-graphics mascot on the welcome splash (force-on, no-wins-over-force); default is conservative env-based detection, falling back to the always-correct half-block mascot |

**Dynamic terminal window/tab title.** `mecatui` sets the terminal window/tab title
to `<session title> — <status word> mecatui`, so you can tell sessions apart in a
tab bar. The title is the **first genuine prompt** of the session (clamped to ~40
runes); the status word reflects the TUI phase:

| Phase | Title |
|---|---|
| running | `<title> — Working mecatui` |
| awaiting approval | `<title> — ⚠ mecatui` |
| connecting | `<title> — Connecting mecatui` |
| fatal | `<title> — ✗ mecatui` |
| idle / replay (title known) | `<title> — mecatui` |
| no title yet | `mecatui` |

The title leads because tab bars **truncate from the right**; the status is a
**static word, never an animated spinner** (per-frame title churn trips OS
attention heuristics — the dock bounces / the taskbar flashes on every change).
The title self-heals across a session switch / fork / carryover (a refetch adopts
the server's stored title when this client never saw the first prompt).

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
overlay reflects that. When skills ARE discovered, `/skills` opens a read-only
inventory panel listing each skill's name + one-line description (a startup
snapshot via the `ListSkills` RPC — skills are immutable for the process
lifetime). The panel opens with a **type-to-filter** input focused: type to
narrow the list by a case-insensitive substring match over each skill's **name**
and **description** (mirroring the `/models` picker's filter, but read-only —
there is no cursor/enter/select here, activation stays the model's call). `esc`
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
dirs and even when server slash-command expansion is off. `/clear` (reset the
conversation and scrollback) and `/help` (open the keys-&-features overlay) are
*always* available because they act purely on the TUI's own state; `/mcp` (browse
the MCP inventory), `/agents` (browse the agent-definition inventory — the
resolved registry the `Subagent` tool routes delegations to), `/team` (the unified
agents overlay pinned to the Teams tab — same surface as `ctrl+a`, which picks a
context-sensitive default tab), `/skills` (browse the skills inventory),
`/soul` (inspect the persona — read-only), `/usermodel` (inspect the user
model — read-only), `/models` (pick the model for the next session), `/worktrees`
(switch to a sibling git worktree), and `/schedule` (browse & manage scheduled
tasks) appear
only when the connected server advertises those capabilities (and, for
`/mcp`/`/agents`/`/skills`/`/soul`/`/usermodel`/`/models`/`/worktrees`/`/schedule`, the matching client
collaborator is wired). The fixed palette order is
`clear, help, mcp, agents, team, skills, soul, usermodel, models, effort, worktrees, schedule` (locked by a test).
`/agents` and `/team` are distinct: `/agents` is the **definition inventory** (a
palette-only `ListAgents` snapshot, gated on `caps.agents`), while `/team` opens
the **live overlay** of a team that has actually run (gated on `caps.teams`).
These never reach the model — a bare built-in line is intercepted and run
locally. Gated-off builtins are hidden from the palette and help overlay;
typing one anyway blocks the send with a warning (it never reaches the model).
(`/compact` is a planned follow-up: it needs a server RPC that does not
exist yet.)

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
user-model store's index, so it reflects facts saved since startup) and shows an
aggregate line (count · size · hash) over a `key — description` list, name-sorted.
The per-entry value is omitted — `RecallUser` loads it. `esc` closes; the panel
never edits the user model (the agent curates it).

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
picker stays open); with an empty filter it closes the picker.

`enter` on the cursor row **switches immediately** — the conversation is ALWAYS kept.
Because the provider is FIXED per session, switching live means a real handoff: the
old session is closed (`CloseSession`) and a fresh one is created on the picked model
**seeded with the current session's conversation** via `source_session_id` on the
`CreateSessionRequest` (`CreateSessionWithCarryover`). The server snapshots the source
conversation and seeds it into the new session, so the model sees the full prior
context. The header rebinds to the NEW session's effective model and a transient status
note reads **`switched to <model> — conversation kept`**. For a **cross-provider**
switch the server strips the prior model's provider-private state (reasoning cache,
provider phase, item ids) and replays the text/roles/tool calls to the new provider, so
the note honestly adds **`(prior reasoning cache dropped)`** — the conversation still
carries. (There is no confirm overlay and no same-provider gate: the server accepts
carryover for any provider.) If no live session exists yet (pre-first-connect, or a
failure left no session), the pick falls back to a plain `CreateSession` — there's no
source to carry from. **Dropping the conversation is a separate action**: run `/clear`
to reset the transcript and start fresh on the current model.

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
relocate the store. Background memory consolidation (the "dream" distiller, which
spends tokens) stays **off** on the embedded server.

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
from growing without bound, a retention GC reaps stale sessions: child snapshots
(subagent/parallel/team) after 7 days or 500-per-family, and top-level sessions
after 30 days or 200 store-wide, always skipping an in-flight run. **Concurrency:**
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
- otherwise the clipboard **text** is inserted into the prompt (always — text is not
  cap-gated, so `ctrl+v` still pastes text on an image-incapable model).

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
full payload expands back **in place** when the prompt is sent — or, mid-run, the
moment `enter` queues it as a follow-up (the queue always holds final text). Below
the thresholds a paste is byte-identical to the literal-insert behaviour.

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
One divergence from a small paste: the placeholder is **appended at the end of the
input** (like image markers), not inserted at the cursor — a small literal paste is
cursor-positioned.

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
| `enter` (while a run streams) | **queue a follow-up** (staged; the whole queue is **merged into one prompt** and sent when the turn ends) |
| `↑` (empty input, non-empty queue) | **edit queued** — pull the merged staged follow-ups back into the input for revising (non-destructive; the queue is emptied into the textarea, not dropped). Works both mid-run and while a paused queue is held. |
| `shift+enter` (or `ctrl+j`) | newline in the input |
| paste (bracketed) | insert clipboard text into the prompt; a single pasted **media-file path** is staged as an attachment instead, and a **large** paste (≥ 2000 chars — alone or combined with the current input — or ≥ 30 lines) is staged behind a `[Pasted text #N]` placeholder appended at the end of the input, expanding on send (ignored while an overlay/modal is open) |
| `ctrl+v` | read the OS clipboard — a clipboard **image** stages as an `[Image #N]` attachment (when supported), else paste clipboard **text** (see below) |
| `esc` (while a run streams) | clear staged input → else clear the queue → else cancel the in-flight run (sends `Cancel`; waits for the terminal result) |
| `enter` (idle, **paused queue**, empty input) | resume — send the merged staged follow-ups |
| `esc` (idle, **paused queue**) | clear staged input → else clear the queue |
| `ctrl+c` | graceful quit (double-press): with a non-empty prompt the first press **clears the input**; on an empty prompt it **arms** the guard and shows a footer hint — press `ctrl+c` again within 3s to exit. Any other key disarms. The fatal (dead-connection) screen exits on a single press. |
| in the permission modal: `a`/`y` | allow once |
| in the permission modal: `w` | always allow (this session; offered for the main agent's asks only, not surfaced subagent asks) |
| in the permission modal: `d`/`n`/`esc` | deny |
| in the permission modal: `←`/`→`/`tab` | cycle the focused button; `enter` activates it |
| `pgup` / `pgdn` | scroll the conversation up / down |
| `home` / `end` | jump to the top / bottom of the conversation (`end` resumes auto-follow) |
| mouse wheel | scroll the conversation (**alt screen only**; see below) |
| mouse drag (left) | **select text** in the conversation — drag to an edge auto-scrolls; copies on release (alt screen only; see below) |
| double / triple-click (left) | select word / whole line (copies; alt screen only) |
| right-click | copy the current selection (if any) |
| middle-click | **paste the primary selection** (X11/Wayland select-to-copy buffer) into the prompt — read via the shell backend (`wl-paste --primary` / `xclip -selection primary -o`), falling back to an OSC52 primary read; routed through the same pipeline as a bracketed paste, so a large selection stages as `[Pasted text #N]`. `shift+middle-click` always performs the terminal-native paste instead. |
| `esc` (with an active selection) | **clear the selection** first — before any other `esc` meaning |
| `?` | help overlay (on an empty prompt) |
| `/` | slash-command palette (built-in `/clear`, `/help`; caps-gated `/mcp`, `/agents`, `/team`, `/skills`, `/soul`, `/usermodel`, `/models`, `/effort`, `/worktrees`, `/schedule`; plus workspace commands) |
| `alt+m` | cycle the current session permission mode: **default → plan → accept-edits → default**. The server/session is authoritative; if the aggregate rejects the switch because a turn is running or awaiting approval, mecatui shows a notice and retries the selected mode at the next prompt boundary. |
| `ctrl+a` | open the **unified agents overlay** — ONE surface with three tabs: **Subagents** (the flat Subagent-child fleet), **Parallel** (the fork-join GROUP roster — join mode, branches, winner, fork paths), and **Teams** (the full roster + per-member focus of the most-recent team). `tab` cycles tabs, `enter` focuses a row/group, `esc` steps back / closes. The default tab is **context-sensitive** (team live → parallel live → subagents → parallel → team). Works **while idle and mid-run**; inert under a permission modal. `/team` opens it pinned to the Teams tab. |
| `x` (agents overlay, on a **running** lane) | **cancel that child agent** (sends `CancelChild` with the lane's child id; the run itself keeps streaming). Works on all three tabs: a **Subagents** lane (roster or focus pane), a **Parallel branch** (inside a focused group — `↑/↓` selects the branch), and a **team member** (Teams roster or focus pane; mid-drive OR idle between rounds — the member is de-scheduled and its claimed tasks released). Confirm-less, because it is recoverable: the child is persisted (a subagent stays **resumable** by its `agentId`; a cancelled branch reads `[FAILED] cancelled by user`; a cancelled member shows `stopped — cancelled`). Inert on a done lane. If the child was parked on a surfaced permission ask, the server retracts it (`permission.retract`) and the approval modal dismisses itself. |
| `@` | file-mention menu — complete a workspace path, then attach it on submit (see below) |

**Always-allow (the `w` button).** A main-agent ask offers a third button, **Al[w]ays**,
alongside allow-once and deny. Choosing it permits the current call AND learns a rule that
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

The `?` overlay enumerates the rest of the chords — `ctrl+v` (paste a clipboard
image), `ctrl+o`/`ctrl+r`/`ctrl+p` (MCP inventory / resources / prompts), `ctrl+a`
(the unified agents overlay — three tabs, available idle **and** mid-run; see the
keys table above), `ctrl+t`
(expand/collapse details), and the scroll keys (`pgup`/`pgdn`, `home`/`end`, mouse
wheel) — and greys out any whose feature the connected server has not enabled
(driven by the server's relayed capabilities). When the server serves agent
definitions (`caps.agents`), it also notes that `/agents` browses the definition
inventory.

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
conversation history.

**Header bar.** `mecatui · session <id> · <model> · mode <mode> · <server>`.
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
(`↑`/`↓`, clamped): **`enter`** or **`t`** on a fire jumps straight to that
fire's read-only transcript (issue #235) — a fire is just a top-level
`sched--` session, so this reuses the `/sessions` replay handoff. The
jump-to-fire footer hint (`↑↓: select fire  enter/t: open transcript  esc:
back`) appears only when a session replayer is wired; a fire whose
`SessionID` is empty reports "fire has no session id" and stays in inspect.

**Create form (Phase 3b).** The **`c`** action key opens an in-overlay Create
form (peer of the inspect/confirm sub-views): fields for name, prompt, trigger
(cron OR natural-language), workspace, and a mutating toggle. The trigger
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

**Row format.** Each `/sessions` picker row renders as
`<state-badge> <relative-time> <turns>t <label> (<model-id>)`, where `<label>`
is the session **title** (seeded once from the first genuine user prompt, clamped
to 120 runes) and falls back to the session id when no title is set. The
confirm card keeps the session id (precise identification) and shows the title
when present. A session with no genuine prompt yet (e.g. a freshly-created,
still-empty session, which is also excluded from the list) shows the id.

**State gate (open-a-session).** The `/sessions` picker and the schedule
jump-to-fire both open a session via the replay RPC (`StreamSessionEvents`),
which is a **pure durable-log read with no live-tail** — it streams what has
been appended so far and ends at the log's current tail. Opening a session
that is currently **`running`** or **`awaiting`** (parked on a permission ask)
would therefore show a *partial* transcript with no terminal result, so the
UI **blocks** it with a "session <id> is currently <state> — cannot open
read-only while active" notice. This is a best-effort client-side gate: a
session that transitions to running between the `ListSessions` call and the
open will still replay successfully (a partial transcript ending at the log's
current tail).

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
it in, issue #66). There is no client-side override; the operator escape-hatch is
mecated's `-context-window-override`, which moves the engine trigger **and** this echoed
denominator together. When the window is unknown the meter degrades to the bare current
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
    join=all run carries none, so the line is omitted), and the **preserved winner fork
    path** (`winner fork (preserved): <path>`). Each branch row carries its own glyph
    (**◐**/**✓**/**✗** failed), label, goal, current/last tool (or, once done, its stop
    label and **wall-clock duration**), count, and usage. Branch args/results render as
    bounded previews (capped + control-byte-scrubbed — client-only; gauntlet #7, ADR 0079). The fork paths are the model's no-auto-merge handle
    and ride the tool RESULT too — these events are the client observability channel only.
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
spot cycles back to a plain anchor. A **right-click** copies the current selection
too. **`esc`** clears an active selection **before** its other
meanings (cancel a run / close an overlay / clear the input or queue); with no
selection, `esc` behaves exactly as before. Selection is **blocked** while an
overlay/modal owns the screen (permission ask, `/mcp`, `/team`, `/agents`,
`/skills`, `/soul`, `/usermodel`, `/models`, help, the fatal screen) — a press there
starts nothing, and opening an overlay mid-drag clears the selection. The wheel
still scrolls while a selection exists, without clearing it.

The copy uses **OSC52** (`tea.SetClipboard`) as the primary path and **also**
mirrors the payload into the platform clipboard binary as a best-effort fallback —
**wl-copy** (Wayland), **`xclip -selection clipboard -i`** (X11), **pbcopy**
(macOS), **clip** (Windows). The shell write is best-effort: if no binary is present
the OSC52 copy still carries the selection, and a failed shell write is never
surfaced as an error.

**`--no-mouse`: native selection instead.** In-app selection and the mouse wheel
exist only because the app captures the mouse — and Bubble Tea has no wheel-only
mouse mode, so capturing it is what *prevents* the terminal's own click-drag
selection. Capturing the mouse also suppresses the terminal's native
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
row, `yoff`, viewport height), and the `screenToContent` mapping (`ok`, logical
`L`/`C`). It is off by default (zero cost when unset).

### Type-while-running and queued follow-ups

The input stays **focused while a run streams**, so you can compose the next
request without waiting. Pressing `enter` mid-run **enqueues** the (trimmed,
non-empty) line rather than starting a second concurrent run — the queue is capped
at 16; an over-cap `enter` is rejected with a muted `queue full (16)` status and the
input is kept. A muted card above the input shows `⏳ N queued · ↑ edit` with up to
three previews (`+K more` over that).

When the run ends on a **healthy** stop, the whole queue is **merged into one prompt**
(the staged lines joined by a blank line) and submitted through the ordinary prompt
path (so it reopens the session server-side exactly like a manual follow-up); the
queue empties in a single step. A healthy stop is one where the model was *done* or
merely hit a *size bound* — `end_turn` (and the empty reason), **plus** the per-run
limits `max_turns`, `max_tool_calls`, and `budget` (the run just ran out of
turn/tool/token budget; firing the merged prompt reopens it with a fresh budget, which
is what a lined-up "continue" wants).

A **transient** failure — an idle/stalled stream, an overloaded/unavailable backend, a
rate limit, or a transient upstream 5xx — is treated like a healthy stop and
**auto-resumes** the merged queue, since a plain retry is likely to succeed. This
auto-resume fires **only** when follow-ups are staged: a transient death of a run with
an **empty** queue does not auto-retry the original prompt — the user must resend it
manually.

A **permanent** provider error — a 4xx rejection other than 408/429, a
context-window overflow, a policy block — renders a ONE-LINE summary block
(`✗ <first line, ≤120 runes> — retrying won't help; the request is rejected. Start a
new session or /clear.`) instead of a raw error block. The raw error payload is
available on `ctrl+t` expand under a dim `raw payload:` header. A permanent error is
never auto-retried (the `transient` flag is forced false).
When a session that failed permanently is recovered for a new prompt, a
transient `recover_notice` warning line appears before the first turn so you see it
before burning another provider call.

A **hard**
error, a **user cancel**, `max_consecutive_failures`, or a stream close instead
**pauses and keeps** the queue, so a genuinely-broken run or a deliberate cancel never
silently fires the backlog. The card switches from the muted `⏳ N queued · ↑ edit` to a
louder `⏸ N queued · paused: <reason>` with the resume/edit/clear keys, so a held queue
is never mistaken for a hang. From there (idle), `enter` on an empty line **resumes**
(sends the merged queue), `↑` on an empty line pulls the merged queue back into the
input for **editing** (non-destructive), and `esc` **clears** the queue; sending a
fresh prompt also clears the pause and lets the queue drain at that run's clean end.

A built-in (`/clear`, `/help`) typed mid-run is enqueued like any other line and
dispatched **at drain time**, when the phase is idle and the built-in's idle-guard is
satisfied (so a queued `/clear` clears the transcript instead of sending a prompt).
`/clear` itself empties the queue along with the rest of the session-derived state.

Queueing is **running-only**: while a permission modal is open the modal keys own the
keyboard unchanged (no mid-approval queueing).

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

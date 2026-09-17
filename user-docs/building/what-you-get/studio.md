---
sidebar_position: 11
title: Using Studio
---

# Using Studio

Studio is mecatl's web workspace: a browser client for the daemon with five
surfaces on one rail — **Chats**, **Scheduled**, **Skills**, **Memory**, and
**Settings**. It is a client like `mecatui`: the daemon owns every record, and
Studio reads and writes the daemon's state rather than keeping its own.

## Starting it

Managed mode (the default) supervises a `mecated` from your checkout:

```sh
task build        # produces bin/mecated
task studio:dev   # controller + web server; open http://localhost:3000
```

The controller spawns `mecated` on a random loopback port with a generated
bearer token, resolves the workspace to the repo root, and restarts the daemon
when you change its configuration. `task studio:stop` tears everything down.

External mode points Studio at a daemon you run elsewhere:

```sh
MECATL_BASE_URL=https://mecated.internal:8081 \
MECATL_AUTH_TOKEN=... \
MECATL_WORKSPACE=/srv/workspace \
npm run start
```

In external mode there is no local controller: provider, model-router, and
MCP-gateway settings show as owned by the deployment.

Studio is **daemon-only**. If the daemon is unreachable you get an offline
banner naming the fix — never simulated content.

Studio talks to the daemon through the [TypeScript SDK](../../reference/typescript-sdk-api/core.md)
(`@stacklok-oss/mecatl-sdk`) over a same-origin proxy that holds the bearer
token on the server; the browser never sees a daemon address or credential.

For an OIDC-protected deployment, set `MECATL_OIDC_ISSUER` and
`MECATL_OIDC_CLIENT_ID` (or `MECATL_OIDC_DISCOVERY=1` to read them from the
deployment's own RFC 9728 metadata) and sign in from Settings → Model
provider → **Remote sign-in**. A discovered profile is listed — identity
provider, client ID, audience, scopes — and nothing signs in until you choose
**Continue with browser login**, the same review step as `mecatui login`.
**Copy sign-in link** is the no-browser flow: open the link in any browser that
can reach Studio. The card also names which credential the proxy sends
(sign-in token, static token, or none), where the sign-in tokens live (server
memory, or the encrypted file store that survives a restart), and the
transport knobs in effect (private CA bundle, disabled verification, private
issuer). The variables below are the `mecatui login` / `connect` flags as
deployment environment; `studio/.env.example` documents each.

## What each surface does

- **Chats** — the daemon's session store, live. The sidebar is the session
  inventory (a chat renamed or deleted here is renamed or deleted for every
  client); opening a chat reads its authoritative transcript; a new chat
  creates its daemon session on the first message. Streaming shows tool calls,
  reasoning, delegation badges when the run hands work to subagents or teams,
  and permission asks with three-way verdicts (allow once / always / deny). A
  failed run renders as failed, with a retry. When a scheduled task reports
  back into the chat it started from, the note renders as a **Scheduled task**
  card: the schedule name (linked to its detail page), the fire id, how the
  fire ended, and its outcome text as plain text. A hidden tab gets a browser
  notification for each completed fire once notifications are enabled under
  Settings → Appearance.
- **Scheduled** — the schedule registry: create and edit schedules (cron with
  timezone, or one-shot), pause/resume/fire, and audit each schedule's fire
  history down to the per-fire session transcript. The form's **Describe the
  schedule** box takes a phrase — `every 30 minutes`, `daily at 9am`, `every
  weekday at 9am`, `next monday 3pm`, `in 2 hours`, `tomorrow at noon` — and
  compiles it into the trigger the structured controls then show (daily,
  weekdays, weekly, monthly, every N minutes or hours, or a raw cron
  expression under Custom); a five-field expression typed there is taken as
  the cron itself. The daemon still validates grammar and cadence floors on
  save. The list shows each
  schedule's trigger in plain English, its next and last run, and how many
  times it has fired (against its cap, when the spec sets one); the detail
  page repeats the count under **Runs**. A text filter narrows the list by
  name, schedule, or prompt: press `/` to jump to it, Esc to clear it.
  Write-capable schedules require an explicit opt-in; the default posture is
  read-only plan mode.
- **Skills** — the daemon's resolved skill inventory (name, summary,
  provenance). Read-only today; authoring is a follow-up.
- **Memory** — the user model: durable facts the agent has stored about you.
  Read-only by design — the agent curates memory through injection-scanned
  tool calls, so Studio never offers an editor.
- **Settings** — appearance and notifications, plus (managed mode) the
  provider status, the semantic model router, and **MCP tools**: the MCP
  servers the daemon resolved at startup by source (static endpoints and
  ToolHive discovery, with each source's skip reasons and the ToolHive
  groups; Refresh re-reads the inventory) above the MCP gateway connection
  (bearer token or OAuth). The same inventory opens from a chat's header as
  the **MCP tools** panel, which on a broker deployment also lists that
  chat's connectors with their catalogue state and offers the whole-bundle
  Connect tools / Cancel setup actions. Credentials are never typed into Studio: `mecated`
  reads them from `~/.config/mecatl/auth.yaml`. The **Daemon defaults** card
  on the Model provider page sets what `mecated` starts with — the active
  provider's default and subagent model, the reasoning-effort tier, a
  context-window override, provider-side prompt caching (and the Anthropic
  cache TTL), and under Advanced the per-provider base-URL overrides, the
  ToolHive LLM gateway, model aliases and slots, and the credentials-file
  path. Each save restarts the daemon with the matching `mecated` flags
  (`--default-model`, `--subagent-model`, `--reasoning-effort`,
  `--context-window-override`, `--no-prompt-cache`, `--anthropic-cache-ttl`,
  `--<provider>-base-url`, `--toolhive-llm*`, `--model-alias`,
  `--model-slot`, `--api-key-file`); a default model the daemon does not
  list is refused at startup and the previous defaults are restored. The
  active provider chosen with "Set as active" or "Switch to offline mock"
  is remembered across Studio restarts unless `MECATL_STUDIO_PROVIDER` is
  set.

## Environment variables

| Variable | Meaning |
| --- | --- |
| `MECATL_BASE_URL` | External daemon base URL; presence selects external mode |
| `MECATL_AUTH_TOKEN` | Bearer for the external daemon (server-side only) |
| `MECATL_AUTH_PREFER_STATIC` | `1` makes a non-empty `MECATL_AUTH_TOKEN` outrank an OIDC sign-in (mecatui's ordering); by default a configured sign-in decides |
| `MECATL_AUTH_ANONYMOUS` | `1` sends the daemon no credential at all (`--anonymous`) |
| `MECATL_OIDC_ISSUER`, `MECATL_OIDC_CLIENT_ID`, `MECATL_OIDC_AUDIENCE`, `MECATL_OIDC_SCOPE`, `MECATL_OIDC_REDIRECT_URI` | Remote OIDC sign-in to an OIDC-protected external daemon (explicit values) |
| `MECATL_OIDC_DISCOVERY` | `1` discovers issuer, client ID, audience and scopes from the deployment's RFC 9728 metadata instead; the profile must be reviewed and confirmed in Settings before the first sign-in |
| `MECATL_OIDC_PRIVATE_ISSUER` | `1` allows a plain-HTTP deployment or issuer on loopback / RFC 1918 addresses (`--private-issuer`) |
| `MECATL_OIDC_CALLBACK_TIMEOUT` | Seconds a started sign-in stays valid (default 600; `--callback-timeout`) |
| `MECATL_OIDC_TOKEN_STORE`, `MECATL_OIDC_TOKEN_STORE_KEY`, `MECATL_OIDC_TOKEN_STORE_PATH` | `file` + a 32-byte base64 key keep the sign-in tokens AES-256-GCM encrypted on disk so they survive a Studio restart (`--credential-store file`); default `memory` |
| `MECATL_TLS_CA` | PEM bundle (path or inline) trusted for the daemon and identity-provider connections (`--tls-ca`) |
| `MECATL_TLS_INSECURE` | `1` disables certificate verification for those connections (`--insecure`); warned once and shown in Settings |
| `MECATL_WORKSPACE` | Display-only label of the deployment's workspace in external mode; the daemon assigns session placement itself |
| `MECATL_STUDIO_PUBLIC_ORIGIN` | Comma-separated origins Studio is served from (CSRF gate) |
| `MECATL_STUDIO_ORIGINS` | Controller's Origin allowlist (managed mode) |
| `MECATL_STUDIO_PROVIDER` | Managed provider: `mock`, `toolhive`, or any provider named in `auth.yaml`; when set it overrides the provider remembered from Settings |
| `MECATL_ALLOW_INSECURE_LOOPBACK_MCP` | `1` permits a loopback-HTTP MCP gateway |
| `BRAND_PALETTE` | Default colour palette (`default`, `aztec`, `mono`, or `solar`) for browsers that have not chosen one under Settings → Personalize → Palette; light and dark still follow the Theme setting |
| `STUDIO_PALETTE_DIR` | Directory of custom palette files (`*.json`, each a `{"name", "label", "palette": {token: colour}, "dark": {token: colour}}` document, at most 32 files of 8 KiB) listed read-only in the Palette picker as "· operator"; only allowlisted token names and `#hex` / `rgb()` / `hsl()` / `oklch()` / `oklab()` / `color()` values are accepted, a broken file is skipped with a server log line, and the directory is re-read on every request. Users add their own palettes under Settings → Personalize → Custom palettes (stored per browser) |

## Limits worth knowing

- Re-attaching live to a run Studio did not start (a scheduled fire delivering
  into a chat, another client's run) rides the daemon's session watch, which
  Studio attaches once its 20-second inventory poll reports the run. A run
  shorter than that interval shows only after it ends: Studio then re-reads
  the transcript, so the delivered note still appears without reopening the
  chat.
- Config writes in managed mode restart the daemon, which ends in-flight runs.
- There is no cost display: the daemon accounts tokens, not currency.

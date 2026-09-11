# mecatl Slack bot (example)

A reference Slack bot built on `@stacklok/mecatl-sdk`: DM it, or `@mention`
it in a channel, and it runs your message as a prompt against a real
`mecated` session and replies with the answer (in the channel case, into
the same thread). See [DESIGN.md](DESIGN.md) for the full design, why this
uses raw Slack event handlers instead of bolt-js's `Assistant` class, and
what's still a stretch goal.

This is example code, not a published package — the location under
`sdk/typescript/examples/` is provisional (see [DESIGN.md](DESIGN.md)).

## 1. Create a Slack app

**Fast path:** go to <https://api.slack.com/apps> → **Create New App** →
**From an app manifest** → pick a dev workspace you control → paste in
[`slack-app-manifest.json`](slack-app-manifest.json) → **Create**. Rename
`display_information.name` / `bot_user.display_name` first if you don't
want "mecatl-bot". This encodes the whole `agent_view` setup below,
already correct — the manual steps found real gaps in Slack's own UI (see
"Verify, don't trust the UI" below), which is exactly what this file exists
to route around.

Then, either way (manifest or manual):

1. **Settings → Socket Mode**: generate an app-level token with the
   `connections:write` scope — this is `SLACK_APP_TOKEN` (`xapp-…`).
2. **Install App to Workspace**, then copy the **Bot User OAuth Token**
   (`xoxb-…`) — this is `SLACK_BOT_TOKEN`.
3. Invite the bot to a channel (`/invite @mecatl-bot` or whatever you
   named it) before trying to `@mention` it there — an app that's not a
   channel member never sees its messages.
4. **Verify, don't trust the UI:** open **Settings → App Manifest** and
   check the raw JSON matches what you expect — scopes, `bot_events`,
   `messages_tab_enabled: true`. In testing, the App Home "Messages Tab"
   toggle showed **on** in the UI while the manifest backing it actually
   still said `false` — the toggle's on-screen state lagged the real saved
   config. If something's silently not working, check here first, before
   touching code.

**Manual path** (if you'd rather build on an existing app or understand
each setting instead of pasting the manifest):

1. **Settings → Socket Mode**: turn it on.
2. **Features → Agents & AI Apps**: enable it. This adds an `agent_view`
   block to the app manifest and the `assistant:write` scope.
3. **Features → App Home**: turn on the **Messages Tab**, and check "Allow
   users to send Slash commands and messages from the messages tab".
4. **OAuth & Permissions → Scopes → Bot Token Scopes**: add `chat:write`,
   `app_mentions:read`, `channels:history`, `groups:history` (and
   `assistant:write` if not already present) — `groups:history` covers
   private channels. Also add `users:read` **and** `users:read.email`
   together — Slack requires both to return the `email` field from
   `users.info`, which the access-control check below depends on.
5. **Features → Event Subscriptions → Subscribe to bot events**: add
   `app_home_opened`, `message.im`, `app_mention`, `message.channels`,
   `message.groups`, `agent_session_stopped` (Slack's native stop button —
   without this subscription, `agents.sessions.setStatus` returns
   `missing_agent_session_stopped_event_subscription` and users see a
   non-interactive loading indicator instead of a stop button; see
   [Slack's event reference](https://docs.slack.dev/reference/events/agent_session_stopped)).
   **Do not add `assistant_thread_started`/`assistant_thread_context_changed`**
   — those belong to Slack's *older* Assistant experience, not the
   `agent_view` feature this app uses; a manifest editor warning will tell
   you the same thing if you add them by mistake ("Agent View should not
   use `assistant_thread_started` bot event subscription").
6. Continue with steps 1–4 above (Socket Mode token, install, invite,
   verify).

## 2. Run a real `mecated`

This example always talks to a real mecatl daemon (never a mock) at
runtime — see `test/bridge.test.ts` for the one place a mock is used, which
is only for this package's own offline tests.

```sh
# from the repository root
task build
./bin/mecated serve --grpc-addr 127.0.0.1:50051 --http-addr "" \
  --workspace /path/to/a/scratch/directory
```

Set a real provider key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or
`OPENROUTER_API_KEY`) in that shell first. See [Choose models and providers](https://mecatl.dev/docs/features/choose-models).

## 3. Configure and run the bot

```sh
export SLACK_BOT_TOKEN=xoxb-...
export SLACK_APP_TOKEN=xapp-...
export MECATL_GRPC_ADDRESS=127.0.0.1:50051   # or MECATL_SOCKET_PATH=/path/to/mecated.sock

# New sessions use the server-owned placement configured by mecated --workspace.

# Strongly recommended — see "Security" below:
export SLACK_ALLOWED_EMAILS=alice@example.com,bob@example.com   # comma-separated verified emails
export SLACK_ALLOWED_EMAIL_DOMAINS=example.com                  # comma-separated domains, no "@"

# Optional — restrict which channels can trigger a prompt at all (see "Security" below):
export SLACK_ALLOWED_CHANNEL_IDS=C0123ABCDEF,C0456GHIJKL        # comma-separated Slack channel IDs

task slack-bot:dev
```

### Connecting to a remote, OIDC-gated deployment instead

Point the bot at a real deployed backend (for example the shared staging
`mecak8s`, `mecak8s.example.com:443`) instead of a local `mecated` by
setting `MECATL_GRPC_TLS=true`. This switches the target to `https://` and
requires an OAuth2 client_credentials M2M credential, which the bot uses to
mint and refresh a bearer token per call (`src/m2mToken.ts`):

```sh
export MECATL_GRPC_ADDRESS=mecak8s.example.com:443
export MECATL_GRPC_TLS=true
export MECAK8S_OIDC_TOKEN_URL=https://<okta-org>/oauth2/<auth-server-id>/v1/token
export MECAK8S_OIDC_CLIENT_ID=...
export MECAK8S_OIDC_CLIENT_SECRET=...
# MECAK8S_OIDC_SCOPE defaults to "agent.invoke" (the scope minted for this
# M2M app on the target's auth server); override only if that differs.
```

Session placement is server-owned regardless of target (see above) — nothing
extra to set here for that.

Wait for `mecatl Slack bot is running (Socket Mode)` in the log before
testing — that's the bot's own readiness signal. If neither
`SLACK_ALLOWED_EMAILS` nor `SLACK_ALLOWED_EMAIL_DOMAINS` is set, the log
will warn once that every verified, non-guest workspace member has
command-execution access — see below.

## Security

Every permission ask mecatl raises is auto-approved
(`onPermissionAsk: () => "allow_once"` in `src/bridge.ts`) — there is no
human-in-the-loop step before the model runs a shell command or edits a
file on whatever host `mecated` runs on. That's a deliberate v1 choice
(#882: "every demo run executes under an auto-approved posture so it never
blocks on a human"), but it means **whoever can reach the bot can run
commands unsupervised** — and "reach the bot" is broader than "the person
who set it up": anyone who can DM it, or who shares a channel it's invited
into, qualifies.

Three independent mitigations:

- **Access control** (`src/access.ts`) — every message resolves the
  sender's Slack identity via `users.info` before it reaches the bridge,
  through a small pluggable `AccessResolver` seam. The default
  `EmailAllowlistResolver` **always** rejects, regardless of the
  allowlist config below, a deactivated account, a workspace guest
  (multi- or single-channel), and a Slack Connect member of a different
  company entirely — these aren't policy, they're identity-integrity
  checks `SLACK_ALLOWED_USER_IDS` had no way to express. Once identity is
  verified, it applies whichever of these you've configured (both are
  optional and additive; unset means every verified, non-guest member is
  allowed, with a startup log warning saying so):
  - **`SLACK_ALLOWED_EMAILS`** — comma-separated exact verified emails.
  - **`SLACK_ALLOWED_EMAIL_DOMAINS`** — comma-separated email domains (no
    `@`), for "everyone at this company" without hand-maintaining a list —
    someone who leaves is already excluded once their Slack account is
    deactivated, no list edit needed.

  An org-specific identity backend (an Okta-roster-backed resolver, a
  directory-service/channel-group-backed one) is a separate adapter
  implementing the same `AccessResolver` interface, injected at the
  consuming deployment's level — not something that belongs in this
  reference bot.

  **Migrating from `SLACK_ALLOWED_USER_IDS`:** that variable is gone.
  Slack user IDs (`U0123ABCDEF`) aren't emails, so there's no automatic
  translation — replace your list with the equivalent verified emails (or,
  usually simpler, a domain) in `SLACK_ALLOWED_EMAILS`/
  `SLACK_ALLOWED_EMAIL_DOMAINS`, and add the `users:read` +
  `users:read.email` bot scopes (see step 1 above) before redeploying.
- **`SLACK_ALLOWED_CHANNEL_IDS`** (optional) — comma-separated Slack channel
  IDs (`C0123ABCDEF`, find one via a channel's "Copy link" in Slack). If
  set, an `@mention` in any other channel is silently ignored — the bot
  never joins that conversation at all, regardless of who sent it. This is
  a separate, coarser gate than the identity checks above: it restricts
  *which channels* the bot operates in, not *who* within them can use it.
  It does **not** apply to DMs (a DM's channel ID is per-user, so there's
  nothing meaningful to allowlist there — use `SLACK_ALLOWED_EMAILS`/
  `SLACK_ALLOWED_EMAIL_DOMAINS` to control DM access). Unset means every
  channel the bot is invited to is usable, subject to those identity
  checks.
- **`SLACK_RATE_LIMIT_MAX`** / **`SLACK_RATE_LIMIT_WINDOW_MS`** (defaults:
  20 prompts per 10 minutes, per Slack user) — bounds spend/abuse from a
  single reachable user. This is a request-count limit, not a token/spend
  budget — mecatl core supports the latter (`Deps.MaxRunTokens`), but the
  TypeScript SDK doesn't expose it as a per-call option yet (see the
  `TODO` in `src/bridge.ts`).

None of these mitigations touch the auto-approve design itself — that
trade-off stands as documented above and in `DESIGN.md`. They're
independent controls: *who* can reach the bot, versus *what* mecatl will
do once reached.

## 4. Verify it end to end

**DM:**
1. Open a DM with the bot.
2. Send a message → expect one reply.
3. Send a second message in the same DM → expect it to remember context
   from the first (same mecatl session, reused for every message in that
   DM channel — this surface has no `thread_ts` to key a narrower session
   on).

**Channel:**
1. Invite the bot to a channel, `@mention` it with a prompt → expect a
   reply in a new thread under your message.
2. Reply again in that **same thread**, without re-mentioning the bot →
   expect it to still respond (session continues, keyed on
   `channel:thread_ts`).
3. A message elsewhere in the channel (not in that thread, no mention) →
   expect silence — the bot only follows threads it was tagged into.

If nothing replies, check the bot's terminal log first — it says whether
the event even arrived before the bridge runs anything. Run with
`SLACK_LOG_LEVEL=debug` for the full picture.

## What v1 does and doesn't do

- Every permission ask mecatl raises is auto-approved — this bot is meant
  for a trusted dev workspace, not unattended production use.
- DM: one `mecated` session per Slack DM channel. Channel (public or
  private): one session per thread (`channel:thread_ts`), started by an
  `@mention` and continued by plain replies in that same thread.
- **A bot restart forgets which mecatl session belongs to which Slack
  thread** — the thread↔session map is in-memory only. An in-progress
  thread starts a fresh mecatl session after a restart, even if the real
  one is still alive/resumable server-side. Fine for the stated dev-only
  scope; see the `TODO` in `src/bridge.ts` if this needs to survive
  restarts later.
- **Channel support (public and private) is unverified beyond
  typecheck-level correctness at the time this was written** — built from
  the Slack API docs + the DM fix's lesson (don't trust docs/tutorials
  without live-testing), but not yet confirmed against a real workspace the
  way the DM path was. See `DESIGN.md`.
- Real token streaming (`chat.startStream`/`appendStream`/`stopStream`) and
  Slack's native stop button (`agent_session_stopped` → `run.cancel()`) are
  both wired now. No approval UI yet. See `DESIGN.md` for why and what's next.
- Built on raw Slack event handlers, not bolt-js's `Assistant` class — that
  class wraps a different, older Slack feature that never fires for this
  app's configuration. See `DESIGN.md` for the full story.

## Docker Compose (runs the whole thing)

Runs both `mecated` and the bot together, wired on an internal Docker
network — no separately-installed Go or Node toolchain needed.

```sh
cp .env.example .env    # fill in SLACK_BOT_TOKEN, SLACK_APP_TOKEN, a provider key,
                         # and GH_PACKAGES_TOKEN (a GitHub PAT with read:packages —
                         # needed to install @stacklok/mecatl-sdk during the image
                         # build; see .env.example. TODO: goes away once the SDK is
                         # on public npm)
docker compose up --build
```

Wait for `mecatl Slack bot is running (Socket Mode)` in the log, same as
step 3, then verify per step 4 above.

Notes:
- Both Dockerfiles (`docker/mecated.Dockerfile`, `docker/bot.Dockerfile`)
  are **local dev/demo images only** — not the project's official release
  artifact (that's `ko`, see `.ko.yaml`).
- Neither service publishes a port to the host — the bot only needs
  outbound Socket Mode, and `mecated`'s gRPC port only needs to be reached
  by the bot over the internal compose network. mecated logs a WARN about
  binding non-loopback with no auth; that's expected and acceptable here
  since the network isn't exposed.
- `mecated`'s workspace persists in a named volume (`mecatl-workspace`)
  across `docker compose restart`; `docker compose down --volumes` discards
  it.
- `docker compose down --volumes` when you're done, to clean up the network
  and volume.

## Development

`@stacklok/mecatl-sdk` installs from GitHub Packages (no public npm registry
yet — TODO: this requirement goes away once there is one), so `install`
needs a `read:packages`-scoped token: `NODE_AUTH_TOKEN=$(gh auth token) task
slack-bot:install` (works as long as your `gh` login has that scope).

```sh
task slack-bot:install     # pnpm install — see the token note above
task slack-bot:lint
task slack-bot:typecheck
task slack-bot:test        # offline; spawns a real mecated --mock, no Slack needed
task slack-bot:dev         # runs the bot against the real mecated from step 2
```

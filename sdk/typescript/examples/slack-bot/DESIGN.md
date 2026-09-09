# Slack bot design

Migrated from stacklok/mecatl#882 once the SDK tree existed (per that issue's
own instruction). Treat this as the living design note for this example;
edit it here going forward rather than the issue.

Parent: stacklok/mecatl#881. Implementation: stacklok/mecatl#883.

## The plan

Slack shipped a purpose-built **Agent Sessions API** in 2026
(`docs.slack.dev/ai/agent-sessions/`), replacing the older Assistant
messaging experience (deprecated Feb 2027). Building on it — instead of
hand-rolled message editing — is the core design choice.

> **This section is the original pre-implementation design and mixes up
> event names between two separate Slack features.** Read
> ["Two different Slack features"](#two-different-slack-features--the-thing-that-actually-blocked-v1)
> below before wiring up event subscriptions from anything in this section.

**Session-state mapping** (Slack agent-session status ↔ mecatl session state):

| Slack | mecatl |
|---|---|
| `active` | idle (ready for next prompt) |
| `processing` | running |
| `suspended` | awaiting / `PendingAsk` |
| `closed` | completed / cancelled / failed (terminal) |

- **One mecatl session per Slack thread.** A new top-level `@mention` starts a
  fresh session; replies in that thread continue it. Matches both platforms'
  own thread-scoped models.
- **Streaming is spiked first, not assumed.** Target native
  `chat.startStream`/`appendStream`/`stopStream` for real token streaming
  from `Converse`. Community reports say this works in DMs but fails in
  **channels** with `missing_recipient_team_id` — and "tag the bot in a
  channel" is exactly this plan's scenario, so validate directly before
  committing. Falls back to a single final `chat.postMessage` for v1 if it's
  still broken — without blocking on Slack fixing it.
- **`CancelRun` wires to Slack's native stop button.** Subscribing to
  `agent_session_stopped` gets the button for free while `processing`; the
  handler calls the SDK's cancel, then explicitly transitions the session
  status (Slack doesn't do this automatically on stop). Cheap — no new
  mecatl-side work.
- **Approval: auto-approved by default; manual approval is an explicit
  experiment, not a requirement.** Every demo run executes under an
  auto-approved posture so it never blocks on a human — matching "tag it,
  get an answer" v1 scope. Separately, prototype the natural-looking
  alternative: a `PendingAsk` transitions the session to `suspended`
  (documented as "needs clarification or a tool approval") plus Block Kit
  approve/deny buttons in the thread; clicking resumes and transitions
  status back. Slack's `suspended` gives the native "needs your input"
  signal but does **not** supply a ready-made widget — the buttons are still
  hand-built. Drop this without ceremony if it doesn't fit naturally; it was
  never required to ship v1.
- **Development sequencing: `mecated --mock` first, real `mecated` once the
  SDK path is proven.** Same offline-first discipline as ADR 0253's mocking
  testkit and `mecademo`.
- **Event delivery: Socket Mode.** A persistent WebSocket from the bot
  process — no public URL, no ngrok. Fits a self-hosted dev app (create your
  own Slack app in dev mode, install to one workspace — no App Store
  listing).
- **No fixed demo script.** The bot relays whatever task it's tagged with to
  a real mecatl session.

## Two different Slack features — the thing that actually blocked v1

The section above ("The plan") conflates two Slack features that turned out
to be **separate and mutually exclusive**, discovered only by live testing
against a real workspace (no amount of reading the docs surfaced this — the
docs for each feature read as if it's *the* way to build an AI app):

| | Classic "Assistant" experience | Agent Sessions API (`agent_view`) |
|---|---|---|
| Manifest feature | `features.assistant_view` (or none — bolt-js's docs/tutorials assume this by default) | `features.agent_view` — what "Agents & AI Apps" in App Settings actually writes |
| Trigger event | `assistant_thread_started` / `assistant_thread_context_changed` | `app_home_opened` (with `tab: "messages"`) |
| Status API | `assistant.threads.setStatus` (wrapped by bolt-js's `Assistant` class) | `agents.sessions.setStatus` (raw REST method, **not wrapped by bolt-js 5.0.0 at all**) |
| bolt-js support | `Assistant` class | none |
| Deprecation | Sunsets Feb 2027 | The replacement |

Every reference — Slack's own tutorials, the `bolt-js-assistant-template`
sample, most blog posts as of this writing — describes the classic
Assistant experience. This app is configured for `agent_view` (confirmed by
Slack's own App Manifest editor, which shows a validator warning:
*"Agent View should not use `assistant_thread_started` bot event
subscription"* / *"Agent View should use `app_home_opened` bot event
subscription"*). The original v1 implementation used bolt-js's `Assistant`
class and silently never worked — `assistant_thread_started` was never
going to fire for this app, full stop. See `README.md` step 7 for the
correct events to subscribe to instead.

Also observed once in testing, unconfirmed as a permanent Slack behavior:
the App Home "Messages Tab" toggle showed **on** in the settings UI while
the manifest backing it still said `"messages_tab_enabled": false"` — the
toggle's on-screen state did not reflect the saved config. Editing the
manifest JSON directly and saving fixed it. If you hit the same silence
after following every step, check the raw manifest before anything else.

## v1 baseline (implemented in #883)

The full plan above is the target shape; v1 ships only the baseline slice
(#881's own scope note: "keep this issue's scope simple — tag → prompt →
answer... streaming and steer-while-running are stretch goals, not
requirements") — **revised to the `agent_view` model above**:

- **DM.** Raw `app.event("app_home_opened", ...)`, filtered to
  `tab === "messages"`: greets a channel once (dedup'd in-memory by
  channel id). Raw `app.message(...)`, filtered to `channel_type === "im"`:
  runs the prompt and posts one final reply via `say()`. Every message in
  the DM is one session (no `thread_ts` on this surface to key a narrower
  session on).
- **Channel.** Raw `app.event("app_mention", ...)`: starts (or continues) a
  session keyed by `channel:thread_ts` (a top-level mention's own `ts`
  becomes the thread root — Slack only sets `thread_ts` on replies). Raw
  `app.message(...)`, filtered to `channel_type ∈ {channel, group}` and a
  thread already marked active by a prior mention: continues that same
  session on a plain reply that doesn't re-mention the bot (Slack fires
  BOTH `app_mention` and `message` for a message that mentions the bot, so
  the `message` listener explicitly skips anything containing
  `<@{botUserId}>` — `app_mention` owns those exclusively, or both would
  reply). Replies post via `say({text, thread_ts})` to land in the thread.
  A thread never mentioned into is never touched, even in a channel the bot
  is a member of.
- `agents.sessions.setStatus` called directly via `app.client.apiCall(...)`
  (bolt-js has no typed wrapper for this method) — `processing` before the
  run, `active` after, best-effort (a status-call failure never blocks the
  reply — this API is new enough to not fully trust yet). Channel calls
  additionally pass `thread_ts` (required for thread-based sessions per the
  API docs); DM calls omit it.
- Auto-approve every permission ask (`onPermissionAsk: () => "allow_once"`).

**Channel support is implemented but not yet live-verified** — built from
the API docs plus the DM fix's lesson (don't trust docs/tutorials without
testing against a real workspace), the same way the original `Assistant`
class approach looked correct on paper and silently didn't work. Treat it
as unproven until someone actually tags the bot in a real channel and
confirms both the initial reply and the no-re-mention thread continuation.

**`chat.startStream`/`appendStream`/`stopStream` real token streaming and
`agent_session_stopped` → `run.cancel()` are now implemented** (`src/bridge.ts`,
`src/agentSessions.ts`). Streaming passes `recipient_user_id`/`recipient_team_id`
best-effort on `chat.startStream` to address the community-reported
`missing_recipient_team_id` channel gap named above — **still unverified live**,
same "built from docs, not yet tested against a real workspace" caveat as
channel support generally. Any streaming failure (that one included) is
treated as non-fatal: `SlackTextStream` falls back to a single final `say()`
with the run's full text, exactly as this section's original plan describes,
rather than failing the whole prompt. Verify both the happy path and the
fallback against a real channel before trusting this fully.

Not yet implemented — additive later, not a rewrite:

- `suspended` status + Block Kit approve/deny UI for manual permission
  review.
- Per-run token/spend budget — the TypeScript SDK (M1) doesn't yet expose a
  per-call token limit; see the `TODO` in `src/bridge.ts`. `SLACK_RATE_LIMIT_MAX`
  (below) is a request-count mitigation, not a spend budget.
- Persisting the thread↔session map across a bot restart — see the `TODO`
  in `src/bridge.ts`. A real design decision (format, staleness), not a
  quick fix; not needed for the stated dev-only scope.

## Panel-review fixes (#883 follow-up)

A `/panel-review` after the initial v1 landed found one real ship-blocker
and several smaller gaps; addressed in the same change:

- **Authorization.** `SLACK_ALLOWED_USER_IDS` (comma-separated Slack user
  IDs) gates who can trigger a prompt at all — previously anyone reachable
  by the bot (DM, or a shared channel) got unattended command execution.
  Unset keeps the old (unrestricted) behavior but logs a startup warning.
  See README.md "Security".
- **Rate limiting.** `SLACK_RATE_LIMIT_MAX` / `SLACK_RATE_LIMIT_WINDOW_MS`
  (`src/rateLimit.ts`, an in-memory sliding window keyed by Slack user ID)
  bound request volume per user — a mitigation for abuse/spend, not a
  token budget (see the still-open TODO above).
- **Naming.** `src/assistant.ts` → `src/agentSessions.ts`, matching its one
  export (`registerAgentSessions`) — the old name was exactly the
  confusion this doc's "two different Slack features" section warns about.
- **`slack-app-manifest.json`** now matches what the code actually
  branches on: added `message.groups`/`groups:history` for private-channel
  continuation (the code already handled `channel_type === "group"`; the
  manifest hadn't caught up).
- **Docker Compose** (`docker-compose.yml`, `docker/*.Dockerfile`) — runs
  `mecated` + the bot together for a no-toolchain-needed demo. See
  README.md.

## Costs, stated honestly

- Building on Slack's Agent Sessions API is a bet on a genuinely
  bleeding-edge platform feature — bolt-js 5.0.0 (current as of this
  writing) has zero built-in support for it, meaning every interaction goes
  through raw `app.event`/`app.message`/`apiCall`, not the framework's
  higher-level helpers. That gap will likely close in a future bolt-js
  release; re-check before assuming this hand-rolled code is still the best
  option.
- The two-features confusion above cost real debugging time and could
  silently bite anyone starting from Slack's own tutorials without
  live-testing against a real workspace first. If this bot stops working
  after a bolt-js/Slack platform update, check this table again before
  assuming the code regressed.
- Channel support's `agents.sessions.setStatus`/`app_mention`/`message`
  wiring is built to match the API docs, but — per the whole point of this
  section — the docs alone got the DM path wrong once already. Don't
  advertise channel support as working until it's actually been tested
  live.
- The manual-approval experiment may simply not ship — unchanged from the
  original plan. Native streaming and stop-button wiring did ship, but
  streaming's channel behavior is unverified live — see the note above.

## See also

- stacklok/mecatl#881 (umbrella), #883 (implementation), #821 (the SDK itself)
- [ADR 0253](../../../../docs/adr/0253-sdk-mocking-testkit.md) — the mocking
  testkit; shares the `mecated --mock` sequencing discipline
- [ADR 0232](../../../../docs/adr/0232-steer-while-running.md) —
  steer-while-running, relevant if the approval/steer experiment extends to
  mid-run Slack replies
- [ADR 0204](../../../../docs/adr/0204-caller-identity-threading.md),
  [ADR 0206](../../../../docs/adr/0206-oidc-authn-module.md),
  [ADR 0212](../../../../docs/adr/0212-caller-ownership-enforcement.md) — the
  caller-identity infrastructure a future, separate identity-impersonation
  effort would build on (explicitly out of scope here)

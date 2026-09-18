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

- **One mecatl session per Slack thread.** A new top-level DM or channel
  `@mention` starts a fresh session; replies in that thread continue it.
  Matches both platforms' own thread-scoped models.
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

- **DM.** Raw `app.message(...)`, filtered to `channel_type === "im"`:
  derives the canonical root as `message.thread_ts ?? message.ts`, runs the
  prompt, and streams the reply into that thread. A top-level message starts
  a fresh mecatl session; a reply continues the session for its existing
  Slack thread. (An earlier revision also sent a one-time greeting on
  `app_home_opened` — dropped as unnecessary noise; the `app_home_opened`
  bot-event subscription stays in `slack-app-manifest.json` regardless,
  since Slack's own manifest validator requires it for an `agent_view` app
  even with no handler.)
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
  reply — this API is new enough to not fully trust yet). DM and channel
  calls pass the same canonical `thread_ts` used for session identity,
  streaming, fallback replies, and cancellation.
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
  IDs) gated who could trigger a prompt at all — previously anyone
  reachable by the bot (DM, or a shared channel) got unattended command
  execution. Unset kept the old (unrestricted) behavior but logged a
  startup warning. **Superseded by the email-based `AccessResolver` seam
  below (#1241).** See README.md "Security".

## Email-based `AccessResolver` (#1241 follow-up)

`SLACK_ALLOWED_USER_IDS` had three structural gaps: it's a hand-maintained
list of opaque IDs nothing removes someone from automatically, it can't
distinguish real members from guests, and not every Slack account maps to
an identity provider at all (guests never go through one). Replaced with:

- **`src/access.ts`**'s `AccessResolver` interface — Slack-native only
  (`slackUserId`/`channelId`, never email/Okta/directory concepts) — plus
  this repo's default, generic implementation, `EmailAllowlistResolver`.
  It resolves the sender's verified email via `users.info` (needs
  `users:read` + `users:read.email`) and **always** rejects a deactivated
  account, a workspace guest (`is_restricted`/`is_ultra_restricted`), a
  Slack Connect member of a different company (`is_stranger`), or an
  explicitly unconfirmed email (`is_email_confirmed === false`) —
  identity-integrity checks, not configurable policy. Once identity is
  verified, `SLACK_ALLOWED_EMAILS` (exact match) and/or
  `SLACK_ALLOWED_EMAIL_DOMAINS` (domain match, for "everyone at this
  company" without a hand-maintained list) gate the rest; unset means
  unrestricted among verified, non-guest members.
- **Org-specific identity backends stay out of this repo.** An
  Okta-roster-backed resolver, or a directory-service/channel-group-backed
  one for group-scoped access, is a separate adapter implementing the same
  `AccessResolver` interface, injected at the consuming deployment's level
  — tracked separately, not blocked on this.
- `registerAgentSessions` now takes the resolver as an explicit dependency
  (not read off `BotConfig` directly), so swapping it in a deployment's
  own `index.ts` needs no change to `agentSessions.ts` itself.
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

## Manual permission approval (#1397)

The manual-approval experiment this doc's earlier sections flagged as
possibly-never-shipping did ship — `src/approvals.ts`, replacing
`bridge.ts`'s hardcoded `onPermissionAsk: () => "allow_once"` entirely
(not toggleable back; #1397 asked for the auto-approve to go away, not to
become optional).

**Delivered as a DM, not in-thread Block Kit buttons** — a deliberate
deviation from the "The plan" section's original sketch above. That sketch
assumed an in-thread ephemeral message (`chat.postEphemeral`) would work
for both halves of the requirement: never actionable by the whole channel,
and clearable when the run ends. It satisfies the first (ephemeral is
already user-scoped) but not the second — Slack has no way to update an
ephemeral message except via the `response_url` handed to an actual click,
so there's no way to proactively clear it when a run ends/cancels with
nobody having clicked anything. A regular DM message is equally
user-scoped (only the bot and that one person are in it) but is a real
message, so `chat.update` works on it from any code path, at any time —
which is what "clear the pending Slack UI when the run terminates or is
canceled" actually requires.

**Resolution goes through the SDK's `onPermissionAsk` responder Promise,
never `run.resolveAsk` called from Slack code directly.** The SDK already
invokes one `onPermissionAsk(ask, signal)` per ask, in the background,
independent of whatever the caller does with the run's own event stream
(`RunImpl#startPermissionResponder` in the SDK). `PermissionApprovalGateway`
returns a `Promise` from that call and resolves it from the Slack button
click; the SDK does the rest, including safely ignoring a resolution that
arrives after the ask's own `AbortSignal` already fired. This meant no code
here ever needs to hold a `Run` reference, and multiple concurrent asks on
one run are handled for free (each gets its own responder invocation and
its own signal) — no manual `permission.ask`/`permission.retract` watching
needed in `bridge.ts`'s `for await` loop, which would otherwise stall on a
second ask while blocked awaiting Slack for the first.

**Correlation is one flat `Map<askId, PendingApproval>`** — `askId` is a
server-minted, session-scoped id, so it's already globally unique; no
per-run or per-thread indexing needed on top. A stale click, a duplicate
click, and a post-terminal click are all rejected the same way: the map
entry is deleted synchronously, before any `await`, the first time
anything consumes it (a click or the ask's own abort), so anything arriving
after that finds nothing pending.

**"Allow always" is included** — the SDK's `resolveAsk(askId, "allow_always")`
threads straight through to the server's real `Policy.Learn` semantics, so
the completion criteria's "include it only if the SDK can preserve
existing authority semantics" bar is met without extra plumbing.

**Fails closed.** If DMing the approver throws (missing `im:write` scope,
transient Slack API error), the ask resolves as `deny` rather than hanging
the run indefinitely or silently allowing.

**Out of scope, deliberately:** a timeout on an unanswered ask (not in the
issue's completion criteria — it just waits, same as any other blocked
consumer), and plan-approval asks (`PresentPlan` goes through the SDK's
separate `onPlanApproval` hook, which this bot has never configured).

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
- Native streaming and stop-button wiring shipped, but streaming's channel
  behavior is unverified live — see the note above. The manual-approval
  flow (#1397, see the section above) is likewise built and offline-tested
  but not yet confirmed against a real workspace.

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

import type { App, SayFn } from "@slack/bolt";

import type { AccessResolver } from "./access.js";
import { type PermissionApprovalGateway, registerPermissionApprovals } from "./approvals.js";
import { isStuckExternalAuthorization, type MecatlBridge } from "./bridge.js";
import type { BotConfig } from "./env.js";
import { SlidingWindowRateLimiter } from "./rateLimit.js";

const EXTERNAL_AUTH_MESSAGE =
  "This needs a connector to be authorized by an administrator before it can be used here.";
const FAILURE_MESSAGE =
  "Something went wrong running that against mecatl. Check the bot's logs for details.";
const NOT_AUTHORIZED_MESSAGE =
  "You're not authorized to use this bot. Ask the operator to grant you access.";
const RATE_LIMITED_MESSAGE = "Rate limit exceeded — try again in a bit.";
const MENTION_PREFIX = /^<@[^>]+>\s*/;

/**
 * Wires the mecatl bridge into Slack's Agent Sessions API (v1 baseline, per
 * #882/#881, revised after live testing — see DESIGN.md). bolt-js 5.0.0 has
 * no built-in support for this feature (distinct from its `Assistant` class,
 * which wraps the OLDER "Assistant" experience and never fires for an
 * `agent_view`-configured app), so this is hand-rolled via raw
 * `app.event`/`app.message` + the raw `agents.sessions.setStatus` API (not
 * yet wrapped by bolt-js).
 *
 * `agents.sessions.setStatus` and Agent Session streaming require a thread
 * root in DMs as well as regular channels. Slack omits `thread_ts` from a
 * top-level message and supplies it on replies, so the canonical root is
 * `message.thread_ts ?? message.ts`: a top-level DM starts a new session and
 * a reply continues the session rooted at its `thread_ts`.
 *
 * Two surfaces, two session-keying strategies:
 * - DM: every Slack thread is one mecatl session. A top-level message's own
 *   `ts` becomes the thread root; replies reuse their `thread_ts`.
 * - Channel (public or private): `app_mention` starts/continues a session
 *   keyed by `channel:thread_ts` (a top-level mention's own `ts` becomes the
 *   thread root); a later reply in that SAME thread without re-mentioning
 *   also continues it, via the plain `message` listener, as long as the
 *   thread was mentioned into at least once. A message containing the
 *   mention is skipped in the `message` listener — `app_mention` fires for
 *   it too, and handling it in both would double-reply. Private-channel
 *   (`message.groups`) support is wired but, like public-channel support,
 *   unverified live — see DESIGN.md.
 *
 * Streaming (`chat.startStream`/`appendStream`/`stopStream`) is wired but
 * **unverified live in channels** — DESIGN.md's own pre-implementation notes
 * flag a community-reported `missing_recipient_team_id` failure specific to
 * non-DM streaming, which is why every `runPrompt` call passes
 * `recipient_user_id`/`recipient_team_id` best-effort. `SlackTextStream`
 * treats any streaming failure (that one included) as non-fatal: it falls
 * back to a single final `say()` with the run's full text, exactly as
 * DESIGN.md's original plan describes, rather than failing the whole prompt.
 *
 * Permission asks (issue #1397, see approvals.ts) DM the requesting user a
 * Block Kit approve/deny card instead of auto-approving; `runPrompt` reports
 * the pending/resolved state via the same `agents.sessions.setStatus`
 * (`suspended` while at least one ask is pending) used for `processing`.
 */
export function registerAgentSessions(
  app: App,
  bridge: MecatlBridge,
  config: BotConfig,
  resolver: AccessResolver,
): void {
  const approvals = registerPermissionApprovals(app);
  const activeChannelThreads = new Set<string>();
  const rateLimiter = new SlidingWindowRateLimiter(config.rateLimit.max, config.rateLimit.windowMs);

  // Slack's native stop button on an Agent View session. @slack/types defines the
  // shape (AgentSessionStoppedEvent) but doesn't wire it into bolt's own event
  // union, hence the manual cast — same "hand-rolled, not yet wrapped by bolt-js"
  // situation as the rest of this file. thread_ts here is whichever anchor the
  // session was keyed on — the same channel:thread root used by both the DM
  // and channel message handlers below.
  app.event("agent_session_stopped", async ({ event }) => {
    const stopped = event as unknown as { channel: string; thread_ts: string };
    const threadKey = `${stopped.channel}:${stopped.thread_ts}`;
    try {
      await bridge.cancel(threadKey);
    } catch (error) {
      app.logger.warn("run.cancel() failed for agent_session_stopped", error);
    }
  });

  app.event("app_mention", async ({ event, context, say }) => {
    if (event.bot_id !== undefined) return;
    // Hard channel-level gate, checked before anything else: if configured, a channel not on
    // the list is silently ignored — this is the sole place a channel/group session ever
    // gets started (see registerAgentSessions's doc comment), so gating here is enough to
    // keep the bot out of every other channel too, no matter what's said in it.
    if (config.allowedChannelIds !== undefined && !config.allowedChannelIds.has(event.channel)) {
      return;
    }
    if (event.user === undefined) {
      app.logger.warn("app_mention has no user id — ignoring (can't scope a reply to nobody)");
      return;
    }
    const threadTs = event.thread_ts ?? event.ts;
    const notify = ephemeralNotifier(app, event.channel, event.user, threadTs);
    const decision = await resolver.resolve({ channelId: event.channel, slackUserId: event.user });
    if (!decision.allowed) return void notify(NOT_AUTHORIZED_MESSAGE);
    if (!rateLimiter.allow(event.user)) return void notify(RATE_LIMITED_MESSAGE);
    const threadKey = `${event.channel}:${threadTs}`;
    activeChannelThreads.add(threadKey);
    const text = event.text.replace(MENTION_PREFIX, "");
    await runPrompt(
      app,
      bridge,
      event.channel,
      threadTs,
      threadTs,
      threadKey,
      text,
      event.user,
      context.teamId,
      say,
      notify,
      approvals,
      "channel",
    );
  });

  app.message(async ({ message, context, say }) => {
    if (message.subtype !== undefined || message.bot_id !== undefined) return;
    if (message.text === undefined) return;
    if (!("channel_type" in message)) return;
    const channelId = message.channel;
    const userId = message.user;
    const text = message.text;
    const threadTs = message.thread_ts;

    if (message.channel_type === "im") {
      const rootTs = threadTs ?? message.ts;
      const notify = sayNotifier(say, rootTs);
      if (userId === undefined) {
        app.logger.warn("DM message has no user id — ignoring (can't resolve access for nobody)");
        return;
      }
      // No channelId here — the AccessResolver contract (issue #1241) defines it as absent
      // for DMs, so a channel-aware resolver can tell a user-scoped DM decision apart from
      // channel/group policy. This is a real DM, so leave it unset rather than passing this
      // surface's own channelId under that name.
      const decision = await resolver.resolve({ slackUserId: userId });
      if (!decision.allowed) return void notify(NOT_AUTHORIZED_MESSAGE);
      if (!rateLimiter.allow(userId)) return void notify(RATE_LIMITED_MESSAGE);
      const threadKey = `${channelId}:${rootTs}`;
      await runPrompt(
        app,
        bridge,
        channelId,
        rootTs,
        rootTs,
        threadKey,
        text,
        userId,
        context.teamId,
        say,
        notify,
        approvals,
        "dm",
      );
      return;
    }

    if (message.channel_type !== "channel" && message.channel_type !== "group") return;
    if (threadTs === undefined) return;
    const threadKey = `${channelId}:${threadTs}`;
    if (!activeChannelThreads.has(threadKey)) return;
    if (context.botUserId !== undefined && text.includes(`<@${context.botUserId}>`)) return;
    if (userId === undefined) {
      app.logger.warn("channel message has no user id — ignoring (can't scope a reply to nobody)");
      return;
    }
    const notify = ephemeralNotifier(app, channelId, userId, threadTs);
    const decision = await resolver.resolve({ channelId, slackUserId: userId });
    if (!decision.allowed) return void notify(NOT_AUTHORIZED_MESSAGE);
    if (!rateLimiter.allow(userId)) return void notify(RATE_LIMITED_MESSAGE);
    await runPrompt(
      app,
      bridge,
      channelId,
      threadTs,
      threadTs,
      threadKey,
      text,
      userId,
      context.teamId,
      say,
      notify,
      approvals,
      "channel",
    );
  });
}

/** A reply channel scoped to the requesting user only — never visible to the rest of a thread. */
type Notifier = (text: string) => Promise<unknown>;

function sayNotifier(say: SayFn, threadTs: string | undefined): Notifier {
  return (text) => say(threadTs === undefined ? text : { text, thread_ts: threadTs });
}

/**
 * Auth-rejection, rate-limit, and operational-failure replies must stay scoped to the person
 * who triggered them — broadcasting e.g. "you're not authorized" to the whole channel outs
 * that person to everyone else in it (#1242). `chat.postEphemeral` is Slack's mechanism for
 * that: visible only to `user`, never persisted as a regular thread message. There is
 * deliberately no channel-visible fallback for an unknown `user` (PR #1245 review) — a caller
 * with no user id to scope the reply to must ignore the event instead of calling this at all,
 * or the fallback would recreate the exact leak this exists to close.
 */
function ephemeralNotifier(
  app: App,
  channel: string,
  user: string,
  threadTs: string | undefined,
): Notifier {
  return (text) =>
    app.client.chat.postEphemeral({
      channel,
      text,
      user,
      ...(threadTs === undefined ? {} : { thread_ts: threadTs }),
    });
}

async function runPrompt(
  app: App,
  bridge: MecatlBridge,
  channelId: string,
  statusThreadTs: string,
  replyThreadTs: string | undefined,
  threadKey: string,
  text: string,
  recipientUserId: string,
  recipientTeamId: string | undefined,
  say: SayFn,
  notifyError: Notifier,
  approvals: PermissionApprovalGateway,
  origin: "dm" | "channel",
): Promise<void> {
  // Agent Session streaming is thread-scoped, so this uses the same canonical
  // root as status, fallback replies, cancellation, and the mecatl session key.
  const stream = new SlackTextStream(
    app,
    channelId,
    statusThreadTs,
    recipientUserId,
    recipientTeamId,
  );
  const onPermissionAsk = approvals.createResponder({
    authorizedUserId: recipientUserId,
    originLabel: origin === "dm" ? "a DM with the bot" : `<#${channelId}>`,
    setStatus: (status) => setSessionStatus(app, channelId, statusThreadTs, status),
  });
  try {
    const outcome = await bridge.handlePrompt(
      threadKey,
      text,
      (delta) => stream.append(delta),
      onPermissionAsk,
      // Fired from INSIDE the bridge's per-thread queue, once this call's own
      // run actually starts/settles — not eagerly here, where a second
      // same-thread message could otherwise overwrite a still-pending run's
      // `suspended` status with `processing` before its own turn arrives
      // (panel-review, samuv).
      () => setSessionStatus(app, channelId, statusThreadTs, "processing"),
      () => setSessionStatus(app, channelId, statusThreadTs, "active"),
    );
    if (stream.started) await stream.stop();
    // `!stream.started` (never streamed at all) and `stream.failed` (streamed
    // partially, then an append broke mid-run — #1289 review, samuv: without
    // this, the user silently gets only the partial text that streamed before
    // the failure, never the real final answer) both need the full-text say().
    if (!stream.started || stream.failed) {
      const reply = outcome.text.length > 0 ? outcome.text : `(${outcome.stopReason}: no text)`;
      await say(replyThreadTs === undefined ? reply : { text: reply, thread_ts: replyThreadTs });
    }
  } catch (error) {
    app.logger.error("mecatl prompt failed", error);
    // Close out any partial streamed message first (housekeeping — a streaming
    // reply that never resolves would otherwise sit stuck mid-typing) — this is
    // orthogonal to the ephemeral failure notice below, not a substitute for it:
    // whoever asked still needs to be told something went wrong, streamed
    // partial content or not.
    if (stream.started) await stream.stop();
    // Eviction for a stuck-authorization session happens inside bridge.ts's
    // #runPrompt itself, not here (#1287 review, samuv) — a caller-side evict
    // loses a real race against an already-queued follow-up prompt for the
    // same thread. This only picks which message to show.
    await notifyError(
      isStuckExternalAuthorization(error) ? EXTERNAL_AUTH_MESSAGE : FAILURE_MESSAGE,
    );
  }
}

/**
 * Drives one Slack streamed message (`chat.startStream`/`appendStream`/`stopStream`) from
 * a mecatl run's `message.delta` chunks. `append` calls are used one at a time by
 * `MecatlBridge#runPrompt`'s `for await` loop — it awaits each `onDelta` call before
 * reading the next event — so there's no need to serialize calls here too.
 *
 * A failure at any point (the documented `missing_recipient_team_id` channel gap
 * included — see this file's own doc comment) is treated as "streaming isn't available
 * here," never as a reason to fail the whole prompt: `#failed` latches so every later
 * `append` becomes a no-op. `started` and `failed` are DELIBERATELY separate signals
 * (#1289 review, samuv): `started` alone can't tell "never streamed" apart from
 * "streamed some chunks, then broke" — collapsing them made `runPrompt` treat a
 * mid-stream failure as done, silently truncating the reply to whatever streamed
 * before the break and never delivering the run's real, complete text.
 */
class SlackTextStream {
  #ts: string | undefined;
  #failed = false;

  constructor(
    private readonly app: App,
    private readonly channel: string,
    private readonly threadTs: string,
    private readonly recipientUserId: string,
    private readonly recipientTeamId: string | undefined,
  ) {}

  get started(): boolean {
    return this.#ts !== undefined;
  }

  get failed(): boolean {
    return this.#failed;
  }

  async append(delta: string): Promise<void> {
    if (this.#failed) return;
    try {
      if (this.#ts === undefined) {
        const response = await this.app.client.chat.startStream({
          channel: this.channel,
          markdown_text: delta,
          recipient_user_id: this.recipientUserId,
          thread_ts: this.threadTs,
          ...(this.recipientTeamId === undefined
            ? {}
            : { recipient_team_id: this.recipientTeamId }),
        });
        if (response.ts === undefined) {
          throw new Error("chat.startStream did not return a message ts");
        }
        this.#ts = response.ts;
        return;
      }
      await this.app.client.chat.appendStream({
        channel: this.channel,
        markdown_text: delta,
        ts: this.#ts,
      });
    } catch (error) {
      this.#failed = true;
      this.app.logger.warn("Slack text streaming failed, falling back to a final message", error);
    }
  }

  async stop(): Promise<void> {
    if (this.#ts === undefined) return;
    try {
      await this.app.client.chat.stopStream({ channel: this.channel, ts: this.#ts });
    } catch (error) {
      this.app.logger.warn("chat.stopStream failed", error);
    }
  }
}

/** Best-effort: a session-status hiccup on this still-settling API must never block a reply. */
async function setSessionStatus(
  app: App,
  channelId: string,
  threadTs: string,
  status: "active" | "processing" | "suspended" | "closed",
): Promise<void> {
  try {
    await app.client.apiCall("agents.sessions.setStatus", {
      channel_id: channelId,
      status,
      thread_ts: threadTs,
    });
  } catch (error) {
    app.logger.warn("agents.sessions.setStatus failed", error);
  }
}

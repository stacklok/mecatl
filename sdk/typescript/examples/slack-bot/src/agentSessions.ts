import type { App, SayFn } from "@slack/bolt";

import type { MecatlBridge } from "./bridge.js";
import type { BotConfig } from "./env.js";
import { SlidingWindowRateLimiter } from "./rateLimit.js";

const FAILURE_MESSAGE =
  "Something went wrong running that against mecatl. Check the bot's logs for details.";
const GREETING = "Tag me with a prompt and I'll run it against mecatl.";
const NOT_AUTHORIZED_MESSAGE =
  "You're not authorized to use this bot. Ask the operator to add your Slack user ID to SLACK_ALLOWED_USER_IDS.";
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
 * `agents.sessions.setStatus` requires `thread_ts` on EVERY call, DMs
 * included (easy to misread the API docs otherwise — a DM message has no
 * `thread_ts` field of its own, so this bridge anchors one: the first
 * message's own `ts` in a given DM channel, remembered and reused for every
 * later status call in that channel. That anchor is a status-API-only
 * concept — DM replies still post un-threaded, unlike channel replies.
 *
 * Two surfaces, two session-keying strategies:
 * - DM: every message in the channel is one mecatl session (no real
 *   `thread_ts` exists on this surface to key a narrower one on).
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
 * TODO(#883 follow-up, tracked in DESIGN.md "Not yet implemented"):
 * - `chat.startStream`/`appendStream`/`stopStream` real token streaming.
 * - `agent_session_stopped` → `run.cancel()` (Slack's native stop button).
 * - `suspended` status + Block Kit approve/deny UI for manual permission
 *   review.
 */
export function registerAgentSessions(app: App, bridge: MecatlBridge, config: BotConfig): void {
  const greetedDm = new Set<string>();
  const dmStatusAnchor = new Map<string, string>();
  const activeChannelThreads = new Set<string>();
  const rateLimiter = new SlidingWindowRateLimiter(config.rateLimit.max, config.rateLimit.windowMs);

  if (config.allowedUserIds === undefined) {
    app.logger.warn(
      "SLACK_ALLOWED_USER_IDS is not set — every workspace member who can reach this bot " +
        "(DM it, or share a channel it's invited to) has unattended command-execution access " +
        "to mecated. Set SLACK_ALLOWED_USER_IDS to restrict who can trigger a prompt.",
    );
  }

  app.event("app_home_opened", async ({ event, say }) => {
    if (event.tab !== "messages") return;
    if (greetedDm.has(event.channel)) return;
    greetedDm.add(event.channel);
    await say(GREETING);
  });

  app.event("app_mention", async ({ event, say }) => {
    if (event.bot_id !== undefined) return;
    if (!isAllowed(config, event.user)) return void say(NOT_AUTHORIZED_MESSAGE);
    if (!rateLimiter.allow(event.user ?? event.channel)) return void say(RATE_LIMITED_MESSAGE);
    const threadTs = event.thread_ts ?? event.ts;
    const threadKey = `${event.channel}:${threadTs}`;
    activeChannelThreads.add(threadKey);
    const text = event.text.replace(MENTION_PREFIX, "");
    await runPrompt(app, bridge, event.channel, threadTs, threadTs, threadKey, text, say);
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
      if (!isAllowed(config, userId)) return void say(NOT_AUTHORIZED_MESSAGE);
      if (!rateLimiter.allow(userId ?? channelId)) return void say(RATE_LIMITED_MESSAGE);
      const anchor = dmStatusAnchor.get(channelId) ?? message.ts;
      if (!dmStatusAnchor.has(channelId)) dmStatusAnchor.set(channelId, anchor);
      await runPrompt(app, bridge, channelId, anchor, undefined, channelId, text, say);
      return;
    }

    if (message.channel_type !== "channel" && message.channel_type !== "group") return;
    if (threadTs === undefined) return;
    const threadKey = `${channelId}:${threadTs}`;
    if (!activeChannelThreads.has(threadKey)) return;
    if (context.botUserId !== undefined && text.includes(`<@${context.botUserId}>`)) return;
    if (!isAllowed(config, userId)) return void say(NOT_AUTHORIZED_MESSAGE);
    if (!rateLimiter.allow(userId ?? channelId)) return void say(RATE_LIMITED_MESSAGE);
    await runPrompt(app, bridge, channelId, threadTs, threadTs, threadKey, text, say);
  });
}

function isAllowed(config: BotConfig, userId: string | undefined): boolean {
  if (config.allowedUserIds === undefined) return true;
  return userId !== undefined && config.allowedUserIds.has(userId);
}

async function runPrompt(
  app: App,
  bridge: MecatlBridge,
  channelId: string,
  statusThreadTs: string,
  replyThreadTs: string | undefined,
  threadKey: string,
  text: string,
  say: SayFn,
): Promise<void> {
  await setSessionStatus(app, channelId, statusThreadTs, "processing");
  try {
    const outcome = await bridge.handlePrompt(threadKey, text);
    const reply = outcome.text.length > 0 ? outcome.text : `(${outcome.stopReason}: no text)`;
    await say(replyThreadTs === undefined ? reply : { text: reply, thread_ts: replyThreadTs });
  } catch (error) {
    app.logger.error("mecatl prompt failed", error);
    await say(
      replyThreadTs === undefined
        ? FAILURE_MESSAGE
        : { text: FAILURE_MESSAGE, thread_ts: replyThreadTs },
    );
  } finally {
    await setSessionStatus(app, channelId, statusThreadTs, "active");
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

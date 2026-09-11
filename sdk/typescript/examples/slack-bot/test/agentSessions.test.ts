import type { App } from "@slack/bolt";
import { ServerError } from "@stacklok/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";

import type { AccessResolver } from "../src/access.js";
import { registerAgentSessions } from "../src/agentSessions.js";
import type { MecatlBridge } from "../src/bridge.js";
import type { BotConfig } from "../src/env.js";

const EXTERNAL_AUTH_MESSAGE =
  "This needs a connector to be authorized by an administrator before it can be used here.";
const NOT_AUTHORIZED_MESSAGE =
  "You're not authorized to use this bot. Ask the operator to grant you access.";
const RATE_LIMITED_MESSAGE = "Rate limit exceeded — try again in a bit.";
const FAILURE_MESSAGE =
  "Something went wrong running that against mecatl. Check the bot's logs for details.";

// biome-ignore lint/suspicious/noExplicitAny: bolt's own event/message payload types aren't the point under test — a fake App only needs the shape agentSessions.ts actually uses.
type AnyHandler = (args: any) => Promise<void>;

interface FakeApp {
  apiCall: ReturnType<typeof vi.fn>;
  app: App;
  say: ReturnType<typeof vi.fn>;
  postEphemeral: ReturnType<typeof vi.fn>;
  startStream: ReturnType<typeof vi.fn>;
  appendStream: ReturnType<typeof vi.fn>;
  stopStream: ReturnType<typeof vi.fn>;
  warn: ReturnType<typeof vi.fn>;
  appMention: AnyHandler;
  message: AnyHandler;
  agentSessionStopped: AnyHandler;
}

/** Builds a fake bolt `App` and registers `registerAgentSessions` against it, capturing the
 * handlers it installs so a test can invoke them directly with a synthetic event/message. */
function setUp(
  bridge: MecatlBridge,
  config: BotConfig,
  resolver: AccessResolver = fakeResolver(new Set(["allowed-user"])),
): FakeApp {
  const apiCall = vi.fn().mockResolvedValue(undefined);
  const say = vi.fn().mockResolvedValue(undefined);
  const postEphemeral = vi.fn().mockResolvedValue(undefined);
  const startStream = vi.fn().mockResolvedValue({ ts: "stream-ts" });
  const appendStream = vi.fn().mockResolvedValue(undefined);
  const stopStream = vi.fn().mockResolvedValue(undefined);
  const warn = vi.fn();
  const handlers: {
    appMention?: AnyHandler;
    message?: AnyHandler;
    agentSessionStopped?: AnyHandler;
  } = {};

  const app = {
    client: {
      apiCall,
      chat: { appendStream, postEphemeral, startStream, stopStream },
    },
    event: (eventName: string, handler: AnyHandler) => {
      if (eventName === "app_mention") handlers.appMention = handler;
      if (eventName === "agent_session_stopped") handlers.agentSessionStopped = handler;
    },
    logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn },
    message: (handler: AnyHandler) => {
      handlers.message = handler;
    },
  } as unknown as App;

  registerAgentSessions(app, bridge, config, resolver);

  if (
    handlers.appMention === undefined ||
    handlers.message === undefined ||
    handlers.agentSessionStopped === undefined
  ) {
    throw new Error("registerAgentSessions did not register the expected handlers");
  }
  return {
    agentSessionStopped: handlers.agentSessionStopped,
    apiCall,
    app,
    appendStream,
    appMention: handlers.appMention,
    message: handlers.message,
    postEphemeral,
    say,
    startStream,
    stopStream,
    warn,
  };
}

function fakeBridge(
  handlePrompt: MecatlBridge["handlePrompt"],
  cancel: MecatlBridge["cancel"] = vi.fn(),
): MecatlBridge {
  return { cancel, handlePrompt } as unknown as MecatlBridge;
}

/** A trivial `AccessResolver` for tests that only care about allow/deny, not `EmailAllowlistResolver`'s
 * own `users.info` logic (that gets its own dedicated test file, `access.test.ts`). */
function fakeResolver(allowedSlackUserIds: Set<string>): AccessResolver {
  return {
    resolve: (ctx) =>
      Promise.resolve(
        allowedSlackUserIds.has(ctx.slackUserId)
          ? { allowed: true, principal: ctx.slackUserId }
          : { allowed: false },
      ),
  };
}

function fakeConfig(overrides: Partial<BotConfig> = {}): BotConfig {
  return {
    allowedChannelIds: undefined,
    allowedEmailDomains: undefined,
    allowedEmails: new Set(["allowed@example.com"]),
    mecatlTarget: { baseUrl: "http://unused" },
    rateLimit: { max: 100, windowMs: 60_000 },
    slackAppToken: "xapp-test",
    slackBotToken: "xoxb-test",
    ...overrides,
  };
}

describe("registerAgentSessions", () => {
  it("rejects an unauthorized app_mention with an ephemeral reply, not a channel-visible one", async () => {
    const fake = setUp(fakeBridge(vi.fn()), fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "blocked-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: "C1",
        text: NOT_AUTHORIZED_MESSAGE,
        thread_ts: "100.001",
        user: "blocked-user",
      }),
    );
  });

  it("silently ignores an app_mention from a channel not on SLACK_ALLOWED_CHANNEL_IDS", async () => {
    const resolve = vi.fn().mockResolvedValue({ allowed: true, principal: "allowed-user" });
    const fake = setUp(
      fakeBridge(vi.fn()),
      fakeConfig({ allowedChannelIds: new Set(["C-allowed"]) }),
      { resolve },
    );

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C-blocked",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).not.toHaveBeenCalled();
    expect(resolve).not.toHaveBeenCalled();
  });

  it("still answers an app_mention from a channel on SLACK_ALLOWED_CHANNEL_IDS", async () => {
    const bridge = fakeBridge(
      vi.fn().mockResolvedValue({ sessionId: "s1", stopReason: "end_turn", text: "hi there" }),
    );
    const fake = setUp(bridge, fakeConfig({ allowedChannelIds: new Set(["C-allowed"]) }));

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C-allowed",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).toHaveBeenCalledWith({ text: "hi there", thread_ts: "100.001" });
  });

  it("rate-limits an app_mention with an ephemeral reply", async () => {
    const fake = setUp(
      fakeBridge(vi.fn()),
      fakeConfig({ rateLimit: { max: 0, windowMs: 60_000 } }),
    );

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({ text: RATE_LIMITED_MESSAGE, user: "allowed-user" }),
    );
  });

  it("reports a failed prompt in a channel thread as an ephemeral reply", async () => {
    const bridge = fakeBridge(vi.fn().mockRejectedValue(new Error("boom")));
    const fake = setUp(bridge, fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({ text: FAILURE_MESSAGE, user: "allowed-user" }),
    );
  });

  it("reports a stuck external-authorization session with an actionable message (#1283)", async () => {
    const bridge = fakeBridge(
      vi.fn().mockRejectedValue(
        new ServerError(
          'server: failed precondition: session "s1" has a live external authorization',
          {
            code: "failed_precondition",
            status: 9,
            transport: "grpc",
          },
        ),
      ),
    );
    const fake = setUp(bridge, fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({ text: EXTERNAL_AUTH_MESSAGE, user: "allowed-user" }),
    );
    // Eviction itself is bridge.ts's job now (inside its own #runPrompt, before
    // the queue can unblock a same-thread follow-up — see bridge.ts's comment
    // and bridge.test.ts's "starts a fresh session ... after evictSession").
    // This layer only needs to prove it picks the right reply.
  });

  it("still replies to a successful prompt with a regular, thread-visible say", async () => {
    const bridge = fakeBridge(
      vi.fn().mockResolvedValue({ sessionId: "s1", stopReason: "end_turn", text: "hi there" }),
    );
    const fake = setUp(bridge, fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.postEphemeral).not.toHaveBeenCalled();
    expect(fake.say).toHaveBeenCalledWith({ text: "hi there", thread_ts: "100.001" });
  });

  it("scopes a DM rejection reply to the message's thread root", async () => {
    const resolve = vi.fn().mockResolvedValue({ allowed: false });
    const fake = setUp(fakeBridge(vi.fn()), fakeConfig(), { resolve });

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "D1",
        channel_type: "im",
        subtype: undefined,
        text: "hi",
        ts: "200.001",
        user: "blocked-user",
      },
      say: fake.say,
    });

    expect(fake.postEphemeral).not.toHaveBeenCalled();
    expect(fake.say).toHaveBeenCalledWith({
      text: NOT_AUTHORIZED_MESSAGE,
      thread_ts: "200.001",
    });
    // The AccessResolver contract (issue #1241) defines channelId as absent for DMs, so a
    // channel-aware resolver can tell a user-scoped DM decision apart from channel/group
    // policy — a DM must never pass its own channelId under that name.
    expect(resolve).toHaveBeenCalledWith({ slackUserId: "blocked-user" });
  });

  it("never applies SLACK_ALLOWED_CHANNEL_IDS to a DM (each DM has its own per-user channel id)", async () => {
    const bridge = fakeBridge(
      vi.fn().mockResolvedValue({ sessionId: "s1", stopReason: "end_turn", text: "hi there" }),
    );
    const fake = setUp(bridge, fakeConfig({ allowedChannelIds: new Set(["C-allowed"]) }));

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "D-not-on-the-list",
        channel_type: "im",
        subtype: undefined,
        text: "hi",
        ts: "200.001",
        user: "allowed-user",
      },
      say: fake.say,
    });

    expect(fake.say).toHaveBeenCalledWith({ text: "hi there", thread_ts: "200.001" });
  });

  it("starts separate sessions for separate top-level DM messages", async () => {
    const handlePrompt = vi.fn().mockImplementation(async (_threadKey, text, onDelta) => {
      await onDelta(`reply to ${text}`);
      return { sessionId: `session-${text}`, stopReason: "end_turn", text: `reply to ${text}` };
    });
    const fake = setUp(fakeBridge(handlePrompt), fakeConfig());

    for (const [text, ts] of [
      ["first", "200.001"],
      ["second", "200.002"],
    ]) {
      await fake.message({
        context: { botUserId: "BOT", teamId: "T1" },
        message: {
          bot_id: undefined,
          channel: "D1",
          channel_type: "im",
          subtype: undefined,
          text,
          ts,
          user: "allowed-user",
        },
        say: fake.say,
      });
    }

    expect(handlePrompt).toHaveBeenNthCalledWith(1, "D1:200.001", "first", expect.any(Function));
    expect(handlePrompt).toHaveBeenNthCalledWith(2, "D1:200.002", "second", expect.any(Function));
    expect(fake.startStream).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ channel: "D1", thread_ts: "200.001" }),
    );
    expect(fake.startStream).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ channel: "D1", thread_ts: "200.002" }),
    );
    expect(fake.apiCall).toHaveBeenCalledWith("agents.sessions.setStatus", {
      channel_id: "D1",
      status: "processing",
      thread_ts: "200.001",
    });
    expect(fake.apiCall).toHaveBeenCalledWith("agents.sessions.setStatus", {
      channel_id: "D1",
      status: "processing",
      thread_ts: "200.002",
    });
  });

  it("continues the same session for a reply in an existing DM thread", async () => {
    const handlePrompt = vi.fn().mockImplementation(async (_threadKey, _text, onDelta) => {
      await onDelta("reply");
      return { sessionId: "s1", stopReason: "end_turn", text: "reply" };
    });
    const fake = setUp(fakeBridge(handlePrompt), fakeConfig());

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "D1",
        channel_type: "im",
        subtype: undefined,
        text: "follow up",
        thread_ts: "200.001",
        ts: "200.003",
        user: "allowed-user",
      },
      say: fake.say,
    });

    expect(handlePrompt).toHaveBeenCalledWith("D1:200.001", "follow up", expect.any(Function));
    expect(fake.startStream).toHaveBeenCalledWith(
      expect.objectContaining({ channel: "D1", thread_ts: "200.001" }),
    );
  });

  it("falls back into the current DM thread when streaming fails", async () => {
    const bridge = fakeBridge(
      vi.fn().mockImplementation(async (_threadKey, _text, onDelta) => {
        await onDelta("partial");
        return { sessionId: "s1", stopReason: "end_turn", text: "complete" };
      }),
    );
    const fake = setUp(bridge, fakeConfig());
    fake.startStream.mockRejectedValue(new Error("stream failed"));

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "D1",
        channel_type: "im",
        subtype: undefined,
        text: "hi",
        ts: "200.002",
        user: "allowed-user",
      },
      say: fake.say,
    });

    expect(fake.say).toHaveBeenCalledWith({ text: "complete", thread_ts: "200.002" });
  });

  it("reports a failed DM prompt in the current thread", async () => {
    const fake = setUp(fakeBridge(vi.fn().mockRejectedValue(new Error("boom"))), fakeConfig());

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "D1",
        channel_type: "im",
        subtype: undefined,
        text: "hi",
        ts: "200.002",
        user: "allowed-user",
      },
      say: fake.say,
    });

    expect(fake.say).toHaveBeenCalledWith({ text: FAILURE_MESSAGE, thread_ts: "200.002" });
  });

  it("rejects an unauthorized channel-thread follow-up message with an ephemeral reply", async () => {
    const fake = setUp(fakeBridge(vi.fn()), fakeConfig());

    // Seed the active-thread set the same way a real mention would.
    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });
    fake.say.mockClear();
    fake.postEphemeral.mockClear();

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "C1",
        channel_type: "channel",
        subtype: undefined,
        text: "another go",
        thread_ts: "100.001",
        ts: "100.002",
        user: "blocked-user",
      },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: "C1",
        text: NOT_AUTHORIZED_MESSAGE,
        thread_ts: "100.001",
        user: "blocked-user",
      }),
    );
  });

  it("ignores an app_mention with no user id instead of falling back to a channel-visible reply", async () => {
    const fake = setUp(fakeBridge(vi.fn()), fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: undefined,
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).not.toHaveBeenCalled();
    expect(fake.warn).toHaveBeenCalled();
  });

  it("ignores a channel-thread message with no user id instead of falling back to a channel-visible reply", async () => {
    const fake = setUp(fakeBridge(vi.fn()), fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });
    fake.say.mockClear();
    fake.postEphemeral.mockClear();

    await fake.message({
      context: { botUserId: "BOT", teamId: "T1" },
      message: {
        bot_id: undefined,
        channel: "C1",
        channel_type: "channel",
        subtype: undefined,
        text: "another go",
        thread_ts: "100.001",
        ts: "100.002",
        user: undefined,
      },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).not.toHaveBeenCalled();
    expect(fake.warn).toHaveBeenCalled();
  });

  it("streams a prompt's deltas via chat.startStream/appendStream/stopStream instead of say", async () => {
    const bridge = fakeBridge(
      vi.fn().mockImplementation(async (_threadKey, _text, onDelta) => {
        await onDelta("Hel");
        await onDelta("lo!");
        return { sessionId: "s1", stopReason: "end_turn", text: "Hello!" };
      }),
    );
    const fake = setUp(bridge, fakeConfig());

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.startStream).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: "C1",
        markdown_text: "Hel",
        recipient_team_id: "T1",
        recipient_user_id: "allowed-user",
        thread_ts: "100.001",
      }),
    );
    expect(fake.appendStream).toHaveBeenCalledWith(
      expect.objectContaining({ channel: "C1", markdown_text: "lo!", ts: "stream-ts" }),
    );
    expect(fake.stopStream).toHaveBeenCalledWith(
      expect.objectContaining({ channel: "C1", ts: "stream-ts" }),
    );
  });

  it("falls back to a final say() when chat.startStream fails (e.g. the documented channel gap)", async () => {
    const bridge = fakeBridge(
      vi.fn().mockImplementation(async (_threadKey, _text, onDelta) => {
        await onDelta("partial, never shown");
        return { sessionId: "s1", stopReason: "end_turn", text: "Hello!" };
      }),
    );
    const fake = setUp(bridge, fakeConfig());
    fake.startStream.mockRejectedValue(new Error("missing_recipient_team_id"));

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.appendStream).not.toHaveBeenCalled();
    expect(fake.stopStream).not.toHaveBeenCalled();
    expect(fake.say).toHaveBeenCalledWith({ text: "Hello!", thread_ts: "100.001" });
    expect(fake.warn).toHaveBeenCalled();
  });

  it("delivers the full answer via say() when an established stream breaks mid-run (#1289)", async () => {
    const bridge = fakeBridge(
      vi.fn().mockImplementation(async (_threadKey, _text, onDelta) => {
        await onDelta("Hello");
        // This second delta's underlying appendStream call is the one that fails below.
        await onDelta(" world!");
        return { sessionId: "s1", stopReason: "end_turn", text: "Hello world!" };
      }),
    );
    const fake = setUp(bridge, fakeConfig());
    fake.appendStream.mockRejectedValueOnce(new Error("stream broke"));

    await fake.appMention({
      event: {
        bot_id: undefined,
        channel: "C1",
        text: "<@BOT> hi",
        ts: "100.001",
        type: "app_mention",
        user: "allowed-user",
      },
      context: { teamId: "T1" },
      say: fake.say,
    });

    expect(fake.stopStream).toHaveBeenCalledWith(
      expect.objectContaining({ channel: "C1", ts: "stream-ts" }),
    );
    // The real, complete answer — not just the "Hello" that streamed before the break.
    expect(fake.say).toHaveBeenCalledWith({ text: "Hello world!", thread_ts: "100.001" });
  });

  it("cancels the bridge run for a channel-thread agent_session_stopped", async () => {
    const cancel = vi.fn().mockResolvedValue(undefined);
    const fake = setUp(fakeBridge(vi.fn(), cancel), fakeConfig());

    await fake.agentSessionStopped({ event: { channel: "C1", thread_ts: "100.001" } });

    expect(cancel).toHaveBeenCalledWith("C1:100.001");
  });

  it("cancels the bridge run for a DM agent_session_stopped, keyed on its thread", async () => {
    const cancel = vi.fn().mockResolvedValue(undefined);
    const fake = setUp(fakeBridge(vi.fn(), cancel), fakeConfig());

    await fake.agentSessionStopped({ event: { channel: "D1", thread_ts: "200.001" } });

    expect(cancel).toHaveBeenCalledWith("D1:200.001");
  });

  it("logs a warning instead of throwing when bridge.cancel() fails", async () => {
    const cancel = vi.fn().mockRejectedValue(new Error("boom"));
    const fake = setUp(fakeBridge(vi.fn(), cancel), fakeConfig());

    await expect(
      fake.agentSessionStopped({ event: { channel: "C1", thread_ts: "100.001" } }),
    ).resolves.toBeUndefined();
    expect(fake.warn).toHaveBeenCalled();
  });
});

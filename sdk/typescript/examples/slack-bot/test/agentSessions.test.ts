import type { App } from "@slack/bolt";
import { ServerError } from "@stacklok/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";

import { registerAgentSessions } from "../src/agentSessions.js";
import type { MecatlBridge } from "../src/bridge.js";
import type { BotConfig } from "../src/env.js";

const NOT_AUTHORIZED_MESSAGE =
  "You're not authorized to use this bot. Ask the operator to add your Slack user ID to SLACK_ALLOWED_USER_IDS.";
const RATE_LIMITED_MESSAGE = "Rate limit exceeded — try again in a bit.";
const FAILURE_MESSAGE =
  "Something went wrong running that against mecatl. Check the bot's logs for details.";
const EXTERNAL_AUTH_MESSAGE =
  "This needs a connector to be authorized by an administrator before it can be used here.";

// biome-ignore lint/suspicious/noExplicitAny: bolt's own event/message payload types aren't the point under test — a fake App only needs the shape agentSessions.ts actually uses.
type AnyHandler = (args: any) => Promise<void>;

interface FakeApp {
  app: App;
  say: ReturnType<typeof vi.fn>;
  postEphemeral: ReturnType<typeof vi.fn>;
  warn: ReturnType<typeof vi.fn>;
  appMention: AnyHandler;
  message: AnyHandler;
}

/** Builds a fake bolt `App` and registers `registerAgentSessions` against it, capturing the
 * two handlers it installs so a test can invoke them directly with a synthetic event/message. */
function setUp(bridge: MecatlBridge, config: BotConfig): FakeApp {
  const say = vi.fn().mockResolvedValue(undefined);
  const postEphemeral = vi.fn().mockResolvedValue(undefined);
  const warn = vi.fn();
  const handlers: { appMention?: AnyHandler; message?: AnyHandler } = {};

  const app = {
    client: {
      apiCall: vi.fn().mockResolvedValue(undefined),
      chat: { postEphemeral },
    },
    event: (eventName: string, handler: AnyHandler) => {
      if (eventName === "app_mention") handlers.appMention = handler;
    },
    logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn },
    message: (handler: AnyHandler) => {
      handlers.message = handler;
    },
  } as unknown as App;

  registerAgentSessions(app, bridge, config);

  if (handlers.appMention === undefined || handlers.message === undefined) {
    throw new Error("registerAgentSessions did not register the expected handlers");
  }
  return {
    app,
    appMention: handlers.appMention,
    message: handlers.message,
    postEphemeral,
    say,
    warn,
  };
}

function fakeBridge(
  handlePrompt: MecatlBridge["handlePrompt"],
  evictSession: MecatlBridge["evictSession"] = vi.fn(),
): MecatlBridge {
  return { evictSession, handlePrompt } as unknown as MecatlBridge;
}

function fakeConfig(overrides: Partial<BotConfig> = {}): BotConfig {
  return {
    allowedUserIds: new Set(["allowed-user"]),
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
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({ text: RATE_LIMITED_MESSAGE, user: "allowed-user" }),
    );
  });

  it("reports a failed prompt in a channel thread as an ephemeral reply", async () => {
    const evictSession = vi.fn();
    const bridge = fakeBridge(vi.fn().mockRejectedValue(new Error("boom")), evictSession);
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
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({ text: FAILURE_MESSAGE, user: "allowed-user" }),
    );
    expect(evictSession).not.toHaveBeenCalled();
  });

  it("reports a stuck external-authorization session with an actionable message and evicts it (#1283)", async () => {
    const evictSession = vi.fn();
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
      evictSession,
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
      say: fake.say,
    });

    expect(fake.say).not.toHaveBeenCalled();
    expect(fake.postEphemeral).toHaveBeenCalledWith(
      expect.objectContaining({ text: EXTERNAL_AUTH_MESSAGE, user: "allowed-user" }),
    );
    expect(evictSession).toHaveBeenCalledWith("C1:100.001");
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
      say: fake.say,
    });

    expect(fake.postEphemeral).not.toHaveBeenCalled();
    expect(fake.say).toHaveBeenCalledWith({ text: "hi there", thread_ts: "100.001" });
  });

  it("leaves a DM's rejection reply as a plain say — DMs have no visibility problem to fix", async () => {
    const fake = setUp(fakeBridge(vi.fn()), fakeConfig());

    await fake.message({
      context: { botUserId: "BOT" },
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
    expect(fake.say).toHaveBeenCalledWith(NOT_AUTHORIZED_MESSAGE);
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
      say: fake.say,
    });
    fake.say.mockClear();
    fake.postEphemeral.mockClear();

    await fake.message({
      context: { botUserId: "BOT" },
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
      say: fake.say,
    });
    fake.say.mockClear();
    fake.postEphemeral.mockClear();

    await fake.message({
      context: { botUserId: "BOT" },
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
});

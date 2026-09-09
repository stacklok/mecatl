import { describe, expect, it } from "vitest";

import { MecatlBridge } from "../src/bridge.js";
import { cannedMockReply, withMockDaemon } from "./harness.js";

describe("MecatlBridge", () => {
  it("answers a prompt with the mock provider's canned reply", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const outcome = await bridge.handlePrompt("channel:thread-1", "hello");
        expect(outcome.text).toBe(cannedMockReply);
        expect(outcome.stopReason).toBe("end_turn");
      } finally {
        await bridge.close();
      }
    });
  });

  it("reuses one mecatl session per thread key across prompts", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const first = await bridge.handlePrompt("channel:thread-1", "first");
        const second = await bridge.handlePrompt("channel:thread-1", "second");
        expect(second.sessionId).toBe(first.sessionId);
      } finally {
        await bridge.close();
      }
    });
  });

  it("starts a fresh session for a thread key after evictSession", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const first = await bridge.handlePrompt("channel:thread-1", "first");
        bridge.evictSession("channel:thread-1");
        const second = await bridge.handlePrompt("channel:thread-1", "second");
        expect(second.sessionId).not.toBe(first.sessionId);
      } finally {
        await bridge.close();
      }
    });
  });

  it("creates a separate mecatl session for a different thread key", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const first = await bridge.handlePrompt("channel:thread-1", "first");
        const other = await bridge.handlePrompt("channel:thread-2", "second");
        expect(other.sessionId).not.toBe(first.sessionId);
      } finally {
        await bridge.close();
      }
    });
  });

  it("queues concurrent prompts on the same thread instead of racing", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const [first, second] = await Promise.all([
          bridge.handlePrompt("channel:thread-1", "first"),
          bridge.handlePrompt("channel:thread-1", "second"),
        ]);
        expect(first.sessionId).toBe(second.sessionId);
      } finally {
        await bridge.close();
      }
    });
  });
});

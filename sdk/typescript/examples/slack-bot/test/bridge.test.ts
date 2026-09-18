import { describe, expect, it } from "vitest";

import { MecatlBridge } from "../src/bridge.js";
import { cannedMockReply, fixture, withMockDaemon } from "./harness.js";

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

  it("streams the run's message.delta chunks to onDelta before resolving", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const deltas: string[] = [];
        const outcome = await bridge.handlePrompt("channel:thread-1", "hello", (delta) => {
          deltas.push(delta);
        });
        expect(deltas.join("")).toBe(cannedMockReply);
        expect(outcome.text).toBe(cannedMockReply);
      } finally {
        await bridge.close();
      }
    });
  });

  it("cancel() on a thread with no active run is a safe no-op", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        await expect(bridge.cancel("channel:never-started")).resolves.toBeUndefined();
      } finally {
        await bridge.close();
      }
    });
  });

  it("cancel() reaches the live run without the in-flight prompt rejecting", async () => {
    await withMockDaemon(async ({ baseUrl }) => {
      const bridge = new MecatlBridge({ baseUrl });
      try {
        const prompt = bridge.handlePrompt("channel:thread-1", "hello");
        await bridge.cancel("channel:thread-1");
        // The mock daemon answers fast enough that the run may already have
        // finished by the time cancel() sends its frame — either way, the
        // in-flight handlePrompt() call must settle, not hang or reject.
        await expect(prompt).resolves.toMatchObject({});
      } finally {
        await bridge.close();
      }
    });
  });

  it("threads onPermissionAsk to session.run so a real ask resolves through it", async () => {
    await withMockDaemon(
      async ({ baseUrl }) => {
        const bridge = new MecatlBridge({ baseUrl });
        try {
          const asks: string[] = [];
          const outcome = await bridge.handlePrompt(
            "channel:thread-1",
            "approve the scripted write",
            undefined,
            (ask) => {
              asks.push(ask.tool);
              return "allow_once";
            },
          );
          expect(asks).toEqual(["Write"]);
          expect(outcome.text).toBe("approved write completed");
          expect(outcome.stopReason).toBe("end_turn");
        } finally {
          await bridge.close();
        }
      },
      { script: fixture("permission-ask.json") },
    );
  });

  it("with no onPermissionAsk, a permission ask is left pending until the run is cancelled", async () => {
    await withMockDaemon(
      async ({ baseUrl }) => {
        const bridge = new MecatlBridge({ baseUrl });
        try {
          const prompt = bridge.handlePrompt("channel:thread-1", "approve the scripted write");
          // Give the run a moment to reach the ask and genuinely stall on it,
          // then cancel — without a responder, nothing else will ever settle it.
          await new Promise((resolveWait) => setTimeout(resolveWait, 200));
          await bridge.cancel("channel:thread-1");
          await expect(prompt).resolves.toMatchObject({ stopReason: "cancelled" });
        } finally {
          await bridge.close();
        }
      },
      { script: fixture("permission-ask.json") },
    );
  });
});

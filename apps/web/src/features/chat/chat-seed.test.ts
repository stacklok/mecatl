// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { consumeChatSeed } from "./chat-seed";

describe("chat arrival seed", () => {
  it("consumes arrival prompts once behind send confirmation", () => {
    const arrival = new URL(
      "https://studio.example/workspace/chat?sessionId=bff-opaque&prompt=Review%20this&send=1&panel=history",
    );
    const consumed = consumeChatSeed(arrival);
    expect(consumed.seed).toEqual({ text: "Review this", requiresConfirmation: true });
    expect(consumed.url.searchParams.get("sessionId")).toBe("bff-opaque");
    expect(consumed.url.searchParams.get("panel")).toBe("history");
    expect(consumed.url.searchParams.has("prompt")).toBe(false);
    expect(consumed.url.searchParams.has("send")).toBe(false);
    expect(consumeChatSeed(consumed.url).seed).toBeUndefined();
  });

  it("normalizes and bounds untrusted URL text without splitting a surrogate", () => {
    const url = new URL("https://studio.example/workspace/chat");
    url.searchParams.set("prompt", ` \u0000line\tbreak\n${"x".repeat(32_766)}😀`);
    const consumed = consumeChatSeed(url);
    expect(consumed.seed?.text.startsWith("line\tbreak\n")).toBe(true);
    expect(consumed.seed?.text.length).toBeLessThanOrEqual(32_768);
    expect(consumed.seed?.text.endsWith("\ud83d")).toBe(false);
    expect(consumed.seed?.requiresConfirmation).toBe(false);
  });

  it("ignores an empty prompt and removes only seed query parameters", () => {
    const url = new URL(
      "https://studio.example/workspace/chat?sessionId=opaque&prompt=%00%7F&send=1",
    );
    const consumed = consumeChatSeed(url);
    expect(consumed.seed).toBeUndefined();
    expect(consumed.url.searchParams.get("sessionId")).toBe("opaque");
    expect(consumed.url.search).toBe("?sessionId=opaque");
  });
});

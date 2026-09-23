// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage, writeUserScopedItem } from "../../lib/account-storage";
import { parseQueuedMessages, takeNextQueuedMessage } from "./chat-queue";

afterEach(() => {
  clearUserScopedStorage();
  vi.unstubAllGlobals();
});

describe("chat queue", () => {
  it("validates stored messages and trims their text", () => {
    expect(
      parseQueuedMessages(
        JSON.stringify([
          { createdAt: 12, id: "one", text: " first " },
          { createdAt: "bad", id: "two", text: "second" },
          { id: "", text: "ignored" },
        ]),
      ),
    ).toEqual([
      { createdAt: 12, id: "one", text: "first" },
      { createdAt: 0, id: "two", text: "second" },
    ]);
  });

  it("treats malformed storage as an empty queue", () => {
    expect(parseQueuedMessages("{")).toEqual([]);
    expect(parseQueuedMessages("{}")).toEqual([]);
  });

  it("drains the in-memory queue when local storage is blocked", () => {
    const blocked = {
      getItem: () => {
        throw new Error("storage blocked");
      },
      key: () => null,
      length: 0,
      removeItem: () => {
        throw new Error("storage blocked");
      },
      setItem: () => {
        throw new Error("storage blocked");
      },
    };
    vi.stubGlobal("window", { dispatchEvent: vi.fn(), localStorage: blocked });
    writeUserScopedItem(
      "studio.chat.queue.s1",
      JSON.stringify([{ createdAt: 1, id: "queued", text: "next prompt" }]),
      blocked,
    );

    expect(takeNextQueuedMessage("s1")?.text).toBe("next prompt");
    expect(takeNextQueuedMessage("s1")).toBeUndefined();
  });
});

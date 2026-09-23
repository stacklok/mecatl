// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { parseQueuedMessages } from "./chat-queue";

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
});

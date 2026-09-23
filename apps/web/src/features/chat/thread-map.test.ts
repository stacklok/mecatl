// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { parseThreadMap, threadKeyForMessage, threadTitleFromRoot } from "./thread-map";

describe("thread associations", () => {
  it("uses a stable role and content key", () => {
    const message = { content: "same content", role: "user" };
    expect(threadKeyForMessage(message)).toBe(threadKeyForMessage({ ...message }));
    expect(threadKeyForMessage(message)).not.toBe(
      threadKeyForMessage({ ...message, role: "assistant" }),
    );
  });

  it("creates a compact thread title", () => {
    expect(threadTitleFromRoot("one\n two")).toBe("Thread: one two");
    expect(threadTitleFromRoot(" ")).toBe("Thread: (empty message)");
    expect(threadTitleFromRoot("a".repeat(80))).toHaveLength(49);
  });

  it("drops invalid stored associations", () => {
    expect(
      parseThreadMap(JSON.stringify({ good: { sessionId: "thread-1" }, bad: { sessionId: 2 } })),
    ).toEqual({ good: { sessionId: "thread-1" } });
    expect(parseThreadMap("not json")).toEqual({});
  });
});

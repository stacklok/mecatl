// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import type { ChatMessage } from "./chat-state";
import { serializeTranscript } from "./transcript-text";

const message = (role: string, content: string, id = role + content): ChatMessage => ({
  content,
  id,
  role,
});

describe("serializeTranscript", () => {
  it("heads each turn with its author and skips empty and non-chat turns", () => {
    expect(
      serializeTranscript(
        [
          message("user", " Why? "),
          message("assistant", ""),
          message("tool", "ignored"),
          message("assistant", "Because."),
        ],
        { agentName: "Mecatl", userName: "Giulia" },
      ),
    ).toBe("**Giulia**\n\nWhy?\n\n**Mecatl**\n\nBecause.");
  });

  it("names the user You by default and returns nothing for an empty chat", () => {
    expect(serializeTranscript([message("user", "Hi")], { agentName: "A" })).toBe("**You**\n\nHi");
    expect(serializeTranscript([], { agentName: "A" })).toBe("");
  });
});

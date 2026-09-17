import { describe, expect, it } from "vitest";
import type { AgentMessage, StreamEvent } from "../types";
import { reduceWatchEvent } from "./use-agent-chat";

/**
 * Pins the watch reducer's handling of scheduled-task delivery notes: a
 * `user_prompt` that is a note becomes a tagged delivery card with the fence
 * and header stripped, one fire renders once per kind however often the
 * replay + live boundary repeats it, and an ordinary prompt is untouched.
 */
describe("reduceWatchEvent delivery notes", () => {
  let serial = 0;
  const nextId = () => `id-${++serial}`;
  const run = (events: StreamEvent[]): AgentMessage[] =>
    events.reduce<AgentMessage[]>(
      (messages, event) => reduceWatchEvent(messages, event, nextId),
      [],
    );
  const completed =
    "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) completed with stop reason: end_turn]\nDigest: 3 PRs merged.\n<<<UNTRUSTED\n";
  const started =
    "<<<UNTRUSTED\n[scheduled task nightly started (fire f-1)]\n<<<UNTRUSTED\n";

  it("tags a delivery-note user_prompt and keeps only the stripped body", () => {
    const messages = run([{ type: "user_prompt", text: completed }]);
    expect(messages).toHaveLength(1);
    expect(messages[0].role).toBe("user");
    expect(messages[0].content).toBe("Digest: 3 PRs merged.");
    expect(messages[0].delivery).toEqual({
      scheduleName: "nightly",
      fireId: "f-1",
      kind: "completed",
      stop: "end_turn",
    });
  });

  it("renders one card when the same fire's note is replayed twice", () => {
    const messages = run([
      { type: "user_prompt", text: completed },
      { type: "token", text: "Thanks, noted." },
      { type: "user_prompt", text: completed },
    ]);
    expect(messages.filter((m) => m.delivery)).toHaveLength(1);
    expect(messages.map((m) => m.role)).toEqual(["user", "assistant"]);
  });

  it("keeps a started note and a completed note for one fire as two cards", () => {
    const messages = run([
      { type: "user_prompt", text: started },
      { type: "user_prompt", text: completed },
    ]);
    expect(messages.map((m) => m.delivery?.kind)).toEqual([
      "started",
      "completed",
    ]);
    expect(messages[0].content).toBe("");
  });

  it("leaves an ordinary user_prompt as a plain user message", () => {
    const messages = run([
      { type: "user_prompt", text: "Why does the scheduler test flake?" },
    ]);
    expect(messages).toEqual([
      {
        id: messages[0].id,
        role: "user",
        content: "Why does the scheduler test flake?",
        timestamp: messages[0].timestamp,
      },
    ]);
    expect(messages[0].delivery).toBeUndefined();
  });

  it("does not mistake an un-fenced header typed by the user for a note", () => {
    const messages = run([
      {
        type: "user_prompt",
        text: "[scheduled task nightly (fire f-1) completed with stop reason: end_turn]",
      },
    ]);
    expect(messages[0].delivery).toBeUndefined();
    expect(messages[0].content).toContain("[scheduled task nightly");
  });
});

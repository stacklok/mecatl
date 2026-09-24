// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import type { ChatMessage } from "./chat-state";
import {
  mergeRecordedDeliveryMessages,
  type RecordedDeliveryMessage,
  shouldCheckDeliveryAfterInventory,
} from "./use-delivery-follow";

const previous = { id: "chat-a", updatedAt: "2026-09-24T12:00:00.000Z" };
const advanced = { id: "chat-a", updatedAt: "2026-09-24T12:00:20.000Z" };

describe("delivery follow", () => {
  it("finds a short recorded delivery after inventory changes", () => {
    expect(
      shouldCheckDeliveryAfterInventory({
        connected: true,
        idle: true,
        next: advanced,
        previous,
        sessionId: "chat-a",
        visible: true,
      }),
    ).toBe(true);
    const liveStart: RecordedDeliveryMessage = {
      content: "Task started",
      delivery: { fireId: "fire-1", kind: "started" },
      id: "live-start",
      role: "user",
      tools: [{ args: "{}", id: "tool-1", name: "Read", output: "live detail" }],
    };
    const liveAnswer: ChatMessage = {
      content: "A richer live response",
      id: "live-answer",
      reasoning: "Looked at the file",
      role: "assistant",
    };
    const current = [liveStart, liveAnswer];
    const transcript: RecordedDeliveryMessage[] = [
      {
        content: "Task started",
        delivery: { fireId: "fire-1", kind: "started" },
        id: "transcript-start",
        role: "user",
      },
      {
        content: "Task completed",
        delivery: { fireId: "fire-1", kind: "completed", stop: "end_turn" },
        id: "transcript-complete",
        role: "user",
      },
      { content: "An ordinary user message", id: "ordinary", role: "user" },
    ];
    const merged = mergeRecordedDeliveryMessages(current, transcript);
    expect(merged).toEqual([liveStart, liveAnswer, transcript[1]]);
    expect(merged[0]).toBe(liveStart);
    expect(merged[1]).toBe(liveAnswer);
    expect(mergeRecordedDeliveryMessages(merged, transcript)).toBe(merged);
    expect(mergeRecordedDeliveryMessages([], transcript)).toEqual(transcript.slice(0, 2));
  });

  it("checks only an idle, visible, connected row for the same open chat", () => {
    const facts = {
      connected: true,
      idle: true,
      next: advanced,
      previous,
      sessionId: "chat-a",
      visible: true,
    };
    expect(shouldCheckDeliveryAfterInventory({ ...facts, idle: false })).toBe(false);
    expect(shouldCheckDeliveryAfterInventory({ ...facts, visible: false })).toBe(false);
    expect(shouldCheckDeliveryAfterInventory({ ...facts, connected: false })).toBe(false);
    expect(shouldCheckDeliveryAfterInventory({ ...facts, sessionId: "chat-b" })).toBe(false);
    expect(shouldCheckDeliveryAfterInventory({ ...facts, previous: advanced })).toBe(false);
    expect(
      shouldCheckDeliveryAfterInventory({
        ...facts,
        next: { ...advanced, updatedAt: "2026-09-24T11:59:00.000Z" },
      }),
    ).toBe(false);
    expect(shouldCheckDeliveryAfterInventory({ ...facts, previous: undefined })).toBe(false);
  });
});

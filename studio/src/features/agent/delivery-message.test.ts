import { describe, expect, it } from "vitest";
import {
  deliveryKey,
  deliveryMessage,
  hasDeliveryNote,
  transcriptHasUnseenDelivery,
} from "./delivery-message";
import type { AgentMessage } from "./types";

const completedNote =
  "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) completed with stop reason: end_turn]\nline one\nline two\n<<<UNTRUSTED\n";
const startedNote =
  "<<<UNTRUSTED\n[scheduled task nightly started (fire f-1)]\n<<<UNTRUSTED\n";

describe("deliveryMessage", () => {
  it("tags a delivery note and keeps only the stripped body as content", () => {
    expect(deliveryMessage("m1", completedNote, 5)).toEqual({
      id: "m1",
      role: "user",
      content: "line one\nline two",
      timestamp: 5,
      delivery: {
        scheduleName: "nightly",
        fireId: "f-1",
        kind: "completed",
        stop: "end_turn",
      },
    });
  });

  it("gives a start note an empty content", () => {
    const message = deliveryMessage("m2", startedNote, 0);
    expect(message.content).toBe("");
    expect(message.delivery).toEqual({
      scheduleName: "nightly",
      fireId: "f-1",
      kind: "started",
    });
  });

  it("leaves an ordinary prompt as a plain user message", () => {
    expect(deliveryMessage("m3", "Why does the test flake?", 7)).toEqual({
      id: "m3",
      role: "user",
      content: "Why does the test flake?",
      timestamp: 7,
    });
  });
});

describe("deliveryKey / hasDeliveryNote", () => {
  const messages: AgentMessage[] = [
    { id: "u", role: "user", content: "hi", timestamp: 0 },
    deliveryMessage("d", completedNote, 0),
  ];

  it("keys a note by kind and fire", () => {
    expect(
      deliveryKey({ scheduleName: "x", fireId: "f-1", kind: "completed" }),
    ).toBe("completed:f-1");
  });

  it("finds a note by fire + kind and distinguishes the start note", () => {
    expect(
      hasDeliveryNote(messages, {
        scheduleName: "other-name-same-fire",
        fireId: "f-1",
        kind: "completed",
      }),
    ).toBe(true);
    expect(
      hasDeliveryNote(messages, {
        scheduleName: "nightly",
        fireId: "f-1",
        kind: "started",
      }),
    ).toBe(false);
    expect(
      hasDeliveryNote(messages, {
        scheduleName: "nightly",
        fireId: "f-2",
        kind: "completed",
      }),
    ).toBe(false);
  });
});

describe("transcriptHasUnseenDelivery", () => {
  const local: AgentMessage[] = [
    { id: "u", role: "user", content: "hi", timestamp: 0 },
    { id: "a", role: "assistant", content: "hello", timestamp: 0 },
  ];
  const entry = (role: "user" | "assistant", text: string) => ({
    role,
    text,
    toolCalls: [],
    toolResult: undefined,
  });

  it("is true when the transcript carries a note the list lacks", () => {
    expect(
      transcriptHasUnseenDelivery(
        {
          messages: [
            entry("user", "hi"),
            entry("assistant", "hello"),
            entry("user", completedNote),
          ],
        },
        local,
      ),
    ).toBe(true);
  });

  it("is false once the same fire's note is rendered", () => {
    expect(
      transcriptHasUnseenDelivery(
        { messages: [entry("user", completedNote)] },
        [...local, deliveryMessage("d", completedNote, 0)],
      ),
    ).toBe(false);
  });

  it("is false for a transcript with no notes, and ignores assistant text", () => {
    expect(
      transcriptHasUnseenDelivery(
        { messages: [entry("user", "hi"), entry("assistant", completedNote)] },
        local,
      ),
    ).toBe(false);
  });
});

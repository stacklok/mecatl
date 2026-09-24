// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import type { ChatMessage } from "./chat-state";
import type { SubagentActivity } from "./delegation-fleet";
import { createDelegationFleet } from "./delegation-fleet";
import { placeDelegationCards } from "./delegation-placement";

function child(key: string, parentCallId: string, runId = "run-a"): SubagentActivity {
  return {
    childId: key,
    family: "subagent",
    historyIncomplete: false,
    key,
    parentCallId,
    runId,
    sessionId: "session-a",
    startObserved: true,
    state: "running",
    trace: { entries: [], omitted: 0 },
  };
}

describe("delegation card placement", () => {
  it("keeps reused parent calls with their observed assistant turns and leaves ambiguity visible", () => {
    const first = child("child-a", "call-shared", "run-a");
    const second = child("child-b", "call-shared", "run-b");
    const ambiguous = child("child-c", "call-shared", "run-c");
    const fleet = {
      ...createDelegationFleet("session-a"),
      subagents: [first, second, ambiguous],
    };
    const messages: ChatMessage[] = [
      {
        content: "First turn",
        id: "assistant-a",
        role: "assistant",
        tools: [{ args: "{}", id: "call-shared", name: "Subagent" }],
      },
      { content: "Another task", id: "user-b", role: "user" },
      {
        content: "Second turn",
        id: "assistant-b",
        role: "assistant",
        tools: [{ args: "{}", id: "call-shared", name: "Subagent" }],
      },
    ];
    const placed = placeDelegationCards(messages, fleet, {
      [second.key]: { assistantId: "stale-assistant" },
      [JSON.stringify(["session-a", "run-a", "subagent", "call-shared"])]: {
        assistantId: "assistant-a",
      },
      [JSON.stringify(["session-a", "run-b", "subagent", "call-shared"])]: {
        assistantId: "assistant-b",
      },
      [JSON.stringify(["session-a", "run-c", "subagent", "call-shared"])]: {
        assistantId: "stale-assistant",
      },
    });
    expect(placed.byMessageId["assistant-a"]?.map((entry) => entry.key)).toEqual([first.key]);
    expect(placed.byMessageId["assistant-b"]?.map((entry) => entry.key)).toEqual([second.key]);
    expect(placed.unanchored.map((entry) => entry.key)).toEqual([ambiguous.key]);
  });

  it("keeps parent-call anchors and leaves a missing call unanchored after a wider transcript refresh", () => {
    const withCall = child("child-with-call", "call-a");
    const withoutCall = child("child-without-call", "call-missing");
    const fleet = {
      ...createDelegationFleet("session-a"),
      subagents: [withCall, withoutCall],
    };
    const messages: ChatMessage[] = [
      { content: "Older answer", id: "older-assistant", role: "assistant" },
      { content: "Question", id: "fresh-user", role: "user" },
      {
        content: "Working",
        id: "fresh-assistant",
        role: "assistant",
        tools: [{ args: "{}", id: "call-a", name: "Subagent" }],
      },
    ];
    const placed = placeDelegationCards(messages, fleet, {
      [withCall.key]: { assistantId: "stale-id" },
      [withoutCall.key]: { assistantId: "stale-id" },
    });
    expect(placed.byMessageId["fresh-assistant"]?.map((entry) => entry.key)).toEqual([
      withCall.key,
    ]);
    expect(placed.byMessageId["older-assistant"]).toBeUndefined();
    expect(placed.unanchored.map((entry) => entry.key)).toEqual([withoutCall.key]);
    const observedAssistant = messages[2];
    if (!observedAssistant) throw new Error("Assistant fixture missing");
    const observed = placeDelegationCards([{ ...observedAssistant, id: "stale-id" }], fleet, {
      [withCall.key]: { assistantId: "stale-id" },
      [withoutCall.key]: { assistantId: "stale-id" },
    });
    expect(observed.byMessageId["stale-id"]?.map((entry) => entry.key)).toEqual([
      withCall.key,
      withoutCall.key,
    ]);
    expect(placeDelegationCards([], fleet, {}).unanchored).toHaveLength(2);
  });
});

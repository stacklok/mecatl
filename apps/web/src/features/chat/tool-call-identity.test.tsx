// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
  applyRunDelivery,
  approvalMatchesToolCall,
  type ChatMessage,
  initialRunDeliveryState,
  messagesFromTranscript,
} from "./chat-state";
import { liveTool } from "./chat-workspace";
import { applyDelegationDelivery, createDelegationFleet } from "./delegation-fleet";
import { placeDelegationCards } from "./delegation-placement";
import type { ToolActivity } from "./tool-activity";
import { ToolCallList } from "./tool-call-list";
import { reconcileRecordedMessages } from "./use-delivery-follow";

const options = { newId: () => crypto.randomUUID(), now: 1, replay: true, sessionId: "session-a" };
function event(kind: string, seq: number, payload?: unknown, runId = "run-a"): RunStreamEvent {
  return {
    type: "run.event",
    event: { kind, seq: String(seq), runId, payload, text: "", turn: 1, unknown: false },
  };
}
afterEach(cleanup);

describe("canonical Studio invocation identity", () => {
  it("keeps distinct invocations with identical provider-era names/args and idempotent delivery", () => {
    let state = initialRunDeliveryState("assistant", "");
    const first = event("tool.call", 1, { id: "engine-a", name: "Read", args: "{}" });
    state = applyRunDelivery(state, first, options);
    expect(applyRunDelivery(state, first, options)).toBe(state);
    state = applyRunDelivery(
      state,
      event("tool.call", 2, { id: "engine-a", name: "Read", args: "{}" }),
      options,
    );
    state = applyRunDelivery(
      state,
      event("tool.call", 3, { id: "engine-b", name: "Read", args: "{}" }),
      options,
    );
    state = applyRunDelivery(
      state,
      event("tool.result", 4, { callId: "engine-a", content: "first", isError: false }),
      options,
    );
    state = applyRunDelivery(
      state,
      event("tool.result", 5, { callId: "engine-b", content: "second", isError: true }),
      options,
    );
    expect(state.messages[0]?.tools?.map(({ id, output }) => [id, output])).toEqual([
      ["engine-a", "first"],
      ["engine-b", "second"],
    ]);
    const opened = state.messages[0]?.tools?.[1];
    if (!opened) throw new Error("missing tool");
    const moved = { ...opened, runId: undefined, output: "saved second" };
    expect(
      liveTool([{ content: "", id: "new-row", role: "assistant", tools: [moved] }], opened),
    ).toBe(moved);
    expect(
      liveTool(
        [{ content: "", id: "old-row", role: "assistant", tools: [{ ...opened, id: "engine-a" }] }],
        opened,
      ),
    ).toBe(opened);
    state = applyRunDelivery(
      state,
      event("permission.ask", 6, { askId: "ask-b", callId: "engine-b", tool: "Read" }),
      options,
    );
    const ask = state.approvals[0];
    if (!ask) throw new Error("missing ask");
    expect(approvalMatchesToolCall(ask, opened)).toBe(true);
    expect(approvalMatchesToolCall(ask, { ...opened, id: "engine-a" })).toBe(false);
    expect(ask.controlTarget).toEqual({ askId: "ask-b", runId: "run-a", sessionId: "session-a" });
    state = applyRunDelivery(state, event("approval", 7, { askId: "ask-b" }), options);
    state = applyRunDelivery(
      state,
      event("permission.ask", 8, { askId: "ask-b", callId: "engine-b" }),
      options,
    );
    expect(state.approvals).toEqual([]);
    const auth = {
      authorizationId: "auth-b",
      callId: "engine-b",
      displayName: "Server",
      status: "pending",
    };
    state = applyRunDelivery(state, event("authorization.required", 9, auth), options);
    state = applyRunDelivery(
      state,
      event("authorization.resolved", 10, { ...auth, callId: "engine-a", status: "granted" }),
      options,
    );
    expect(state.messages[0]?.authorizations?.[0]?.status).toBe("pending");
    state = applyRunDelivery(
      state,
      event("authorization.resolved", 11, { ...auth, status: "expired" }),
      options,
    );
    state = applyRunDelivery(state, event("authorization.required", 12, auth), options);
    expect(state.messages[0]?.authorizations?.[0]?.status).toBe("expired");
  });

  it("deduplicates a snapshot call without requiring live run metadata for placement", () => {
    const snapshot = messagesFromTranscript([
      {
        role: "assistant",
        text: "Snapshot",
        toolCalls: [{ id: "engine-a", name: "Read", args: "{}" }],
      },
    ]);
    let state = initialRunDeliveryState("active-unrelated", "", snapshot);
    state = applyRunDelivery(
      state,
      event("tool.call", 1, { id: "engine-b", name: "Read", args: "{}" }),
      options,
    );
    state = applyRunDelivery(
      state,
      event("tool.call", 2, { id: "engine-a", name: "Read", args: "{}" }),
      options,
    );
    expect(state.messages[0]?.tools?.[0]?.runId).toBeUndefined();
    state = applyRunDelivery(
      state,
      event("permission.ask", 3, { askId: "ask-a", callId: "engine-a", tool: "Read" }),
      options,
    );
    state = applyRunDelivery(
      state,
      event("authorization.required", 4, {
        authorizationId: "auth-a",
        callId: "engine-a",
        displayName: "Server",
        status: "pending",
      }),
      options,
    );
    const approval = state.approvals[0];
    const tool = state.messages[0]?.tools?.[0];
    if (!approval || !tool) throw new Error("Missing snapshot approval");
    expect(approvalMatchesToolCall(approval, tool)).toBe(true);
    expect(approval.controlTarget).toEqual({
      askId: "ask-a",
      runId: "run-a",
      sessionId: "session-a",
    });
    expect(state.messages[0]?.authorizations?.[0]).toMatchObject({
      callId: "engine-a",
      runId: "run-a",
      sessionId: "session-a",
    });
    expect(state.messages[1]?.authorizations).toBeUndefined();
    expect(state.messages.flatMap((message) => message.tools ?? []).map((tool) => tool.id)).toEqual(
      ["engine-a", "engine-b"],
    );
  });

  it("renders pending authorization on the exact snapshot call regardless of run identity", () => {
    render(
      <ToolCallList
        authorizations={[
          {
            authorizationId: "auth-exact",
            callId: "snapshot-call",
            displayName: "Exact authorization",
            runId: "different-live-run",
            sessionId: "session-a",
            status: "pending",
          },
          {
            authorizationId: "auth-wrong",
            callId: "wrong-call",
            displayName: "Wrong authorization",
            runId: "different-live-run",
            sessionId: "session-a",
            status: "pending",
          },
        ]}
        tools={[
          { args: "{}", id: "snapshot-call", name: "Snapshot Read" },
          { args: "{}", id: "live-call", name: "Live Read", runId: "snapshot-run" },
        ]}
      />,
    );

    const snapshotRow = screen.getByText("Tool: Snapshot Read").closest("li");
    const liveRow = screen.getByText("Tool: Live Read").closest("li");
    if (!snapshotRow || !liveRow) throw new Error("missing tool call rows");
    expect(within(snapshotRow).getByText("Exact authorization")).toBeTruthy();
    expect(within(snapshotRow).getByRole("button", { name: "Review authorization" })).toBeTruthy();
    expect(within(liveRow).queryByText("Exact authorization")).toBeNull();
    expect(screen.queryByText("Wrong authorization")).toBeNull();
  });

  it("merges saved/live tool rows only by canonical identity, not equal text or args", () => {
    const live: ChatMessage[] = [
      {
        content: "live",
        id: "live",
        role: "assistant",
        tools: [{ id: "engine-b", name: "Read", args: "{}", runId: "run-b" }],
      },
    ];
    const saved = messagesFromTranscript([
      {
        role: "assistant",
        text: "same text",
        toolCalls: [{ id: "engine-a", name: "Read", args: "{}" }],
      },
      {
        role: "assistant",
        text: "same text",
        toolCalls: [{ id: "engine-b", name: "Read", args: "{}" }],
      },
      {
        role: "tool",
        text: "",
        toolCalls: [],
        toolResult: { callId: "engine-b", content: "saved output", isError: false },
      },
    ]);
    const merged = reconcileRecordedMessages(live, saved);
    expect(merged).toHaveLength(2);
    expect(merged[1]?.id).toBe("live");
    expect(merged[1]?.tools?.[0]?.output).toBe("saved output");
    expect(reconcileRecordedMessages(merged, saved)).toBe(merged);
  });

  it.each([true, false])(
    "reconciles invocation membership across split grouping (live grouped: %s)",
    (grouped) => {
      const a = { id: "engine-a", name: "Read", args: "{}", runId: "run-a" };
      const b = { ...a, id: "engine-b" };
      const row = (id: string, tools: ToolActivity[]): ChatMessage => ({
        content: "",
        id,
        role: "assistant",
        tools,
        reasoning: `reasoning ${id}`,
        stopReason: "end_turn",
        turnStat: "live stats",
        authorizations: [
          {
            authorizationId: `auth-${id}`,
            callId: tools[0]?.id ?? "",
            displayName: "Server",
            runId: "run-a",
            sessionId: "session-a",
            status: "pending",
          },
        ],
      });
      const current = grouped ? [row("live-ab", [a, b])] : [row("live-a", [a]), row("live-b", [b])];
      const recordedA = { ...a, runId: undefined, output: "saved A", isError: false };
      const recordedB = { ...b, runId: undefined, output: "saved B", isError: true };
      const saved = (
        grouped
          ? [row("saved-a", [recordedA]), row("saved-b", [recordedB])]
          : [row("saved-ab", [recordedA, recordedB])]
      ).map((message, index) => ({
        ...message,
        content: grouped ? `Saved text ${index}` : "Saved text 0Saved text 1",
      }));
      const merged = reconcileRecordedMessages(current, saved);
      expect(merged.map((message) => message.content).filter(Boolean)).toEqual([
        "Saved text 0Saved text 1",
      ]);
      expect(merged.map((message) => message.id)).toEqual(current.map((message) => message.id));
      expect(merged.flatMap((message) => message.tools?.map((tool) => tool.id) ?? [])).toEqual([
        "engine-a",
        "engine-b",
      ]);
      for (const [index, message] of merged.entries()) {
        expect(message.reasoning).toBe(current[index]?.reasoning);
        expect(message.stopReason).toBe("end_turn");
        expect(message.turnStat).toBe("live stats");
        expect(message.authorizations).toBe(current[index]?.authorizations);
      }
      expect(liveTool(merged, b)).toMatchObject({
        id: "engine-b",
        output: "saved B",
        isError: true,
        runId: "run-a",
      });
      const repeated = reconcileRecordedMessages(merged, saved);
      const expectedResults = [
        { id: "engine-a", output: "saved A", isError: false },
        { id: "engine-b", output: "saved B", isError: true },
      ];
      for (const messages of [merged, repeated]) {
        expect(
          messages
            .flatMap((message) => message.tools ?? [])
            .map(({ id, output, isError }) => ({ id, output, isError })),
        ).toEqual(expectedResults);
      }
      expect(repeated).toBe(merged);
    },
  );

  it("places current delegation by canonical parent", () => {
    let fleet = createDelegationFleet("session-a");
    fleet = applyDelegationDelivery(
      fleet,
      event("subagent.start", 1, { childId: "child-a", parentCallId: "engine-a", goal: "Read" }),
    );
    fleet = applyDelegationDelivery(
      fleet,
      event(
        "subagent.start",
        1,
        { childId: "child-b", parentCallId: "engine-b", goal: "Read" },
        "run-b",
      ),
    );
    const messages: ChatMessage[] = [
      {
        content: "",
        id: "row-a",
        role: "assistant",
        tools: [{ id: "engine-a", args: "{}", name: "Subagent" }],
      },
      {
        content: "",
        id: "row-b",
        role: "assistant",
        tools: [{ id: "engine-b", args: "{}", name: "Subagent" }],
      },
    ];
    const placed = placeDelegationCards(messages, fleet, {});
    expect(placed.byMessageId["row-a"]?.map((item) => item.parentCallId)).toEqual(["engine-a"]);
    expect(placed.byMessageId["row-b"]?.map((item) => item.parentCallId)).toEqual(["engine-b"]);
    expect(placed.unanchored).toEqual([]);
    expect(
      placeDelegationCards(messages.slice(1), fleet, {}).unanchored.map(
        (item) => item.parentCallId,
      ),
    ).toEqual(["engine-a"]);
  });
});

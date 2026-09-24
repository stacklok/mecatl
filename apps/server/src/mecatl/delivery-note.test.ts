// SPDX-License-Identifier: Apache-2.0

import type { Client, Event } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createMecatlChatService, serializeEvent } from "./chat";
import { projectDeliveryNote } from "./delivery-note";

const fence = (content: string) => `<<<UNTRUSTED\n${content}\n<<<UNTRUSTED\n`;
const started = fence("[scheduled task Daily: reports (EU) started (fire fire-1)]");
const completed = fence(
  "[scheduled task Daily: reports (EU) (fire fire-1) completed with stop reason: end_turn]\n**All done**",
);

describe("scheduled delivery projection", () => {
  it("projects only canonical fenced delivery notes", () => {
    expect(projectDeliveryNote(started)).toEqual({
      delivery: {
        fireId: "fire-1",
        kind: "started",
        scheduleName: "Daily: reports (EU)",
      },
      text: "",
    });
    expect(projectDeliveryNote(completed)).toEqual({
      delivery: {
        fireId: "fire-1",
        kind: "completed",
        scheduleName: "Daily: reports (EU)",
        stop: "end_turn",
      },
      text: "**All done**",
    });
    expect(
      projectDeliveryNote(
        fence("[scheduled task Name\nwith punctuation: [v2] started (fire f.2)]"),
      ),
    ).toEqual({
      delivery: { fireId: "f.2", kind: "started", scheduleName: "Name\nwith punctuation: [v2]" },
      text: "",
    });
    expect(
      projectDeliveryNote(
        fence(
          "[scheduled task Silent (fire f-2) completed with stop reason: cancelled]\n(no output)",
        ),
      ),
    ).toMatchObject({ delivery: { kind: "completed", stop: "cancelled" }, text: "(no output)" });
    expect(
      projectDeliveryNote(
        fence(
          "[scheduled task Weekly: [ops]\nnightly sync (fire f-3) completed with stop reason: timeout]\nDone",
        ),
      ),
    ).toMatchObject({
      delivery: { kind: "completed", scheduleName: "Weekly: [ops]\nnightly sync", stop: "timeout" },
      text: "Done",
    });
    expect(
      projectDeliveryNote(
        fence(
          "[scheduled task Daily (fire fire-1) completed with stop reason: end_turn]\nA quoted [scheduled task Other started (fire other)]",
        ),
      ),
    ).toMatchObject({
      delivery: { fireId: "fire-1", kind: "completed", scheduleName: "Daily" },
      text: "A quoted [scheduled task Other started (fire other)]",
    });
  });

  it.each([
    "[scheduled task Daily started (fire fire-1)]",
    "<<<UNTRUSTED\n[scheduled task Daily started (fire fire-1)]",
    "<<<UNTRUSTED\n[scheduled task Daily started (fire fire-1)]\n<<<UNTRUSTED",
    `${started}trailing text`,
    "<<<UNTRUSTED\n[scheduled task Daily started (fire fire-1)]\n<<<UNTRUSTED\n<<<UNTRUSTED\n",
    fence("[scheduled task Daily started (fire )]"),
    fence("[scheduled task  started (fire fire-1)]"),
    fence("[scheduled task Daily (fire fire-1) completed with stop reason: ]\nbody"),
    fence("[scheduled task Daily (fire fire-1) completed with stop reason: end_turn]"),
    fence("[scheduled task Daily triggered (fire fire-1)]"),
  ])("leaves malformed or ordinary text untouched: %s", (text) => {
    expect(projectDeliveryNote(text)).toBeUndefined();
  });

  it("projects only a user_prompt event and keeps unrelated payloads intact", () => {
    const event = {
      kind: "user_prompt",
      payload: { parts: [], text: completed },
      runId: "run-1",
      seq: 1n,
      text: completed,
      turn: 1,
      usage: undefined,
    } as Event;
    expect(serializeEvent(event)).toMatchObject({
      delivery: { fireId: "fire-1", kind: "completed", stop: "end_turn" },
      payload: { text: completed },
      text: "**All done**",
    });
    expect(serializeEvent({ ...event, kind: "schedule.fired" } as Event)).not.toHaveProperty(
      "delivery",
    );
    expect(
      serializeEvent({ ...event, kind: "unknown", wireKind: "future.kind" } as Event),
    ).not.toHaveProperty("delivery");
  });

  it("projects only user-role transcript messages and leaves lookalikes intact", async () => {
    const transcript = vi.fn().mockResolvedValue({
      complete: true,
      messages: [
        { parts: [], role: "user", text: started, toolCalls: [] },
        { parts: [], role: "user", text: completed, toolCalls: [] },
        { parts: [], role: "assistant", text: completed, toolCalls: [] },
        { parts: [], role: "user", text: "[scheduled task ordinary]", toolCalls: [] },
      ],
      sessionId: "session-1",
    });
    const service = createMecatlChatService({
      sessions: { get: vi.fn().mockResolvedValue({ transcript }) },
    } as unknown as Client);

    const response = await service.transcript("session-1");
    expect(response.messages).toMatchObject([
      { delivery: { kind: "started" }, role: "user", text: "" },
      { delivery: { kind: "completed" }, role: "user", text: "**All done**" },
      { role: "assistant", text: completed },
      { role: "user", text: "[scheduled task ordinary]" },
    ]);
    expect(response.messages[2]).not.toHaveProperty("delivery");
    expect(response.messages[3]).not.toHaveProperty("delivery");
  });
});

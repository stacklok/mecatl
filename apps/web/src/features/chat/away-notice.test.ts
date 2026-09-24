// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { AwayNoticeTracker } from "./away-notice";

const onlineIdle = {
  connection: "online" as const,
  phase: "idle" as const,
  refreshed: true,
  sessionId: "chat-1",
};

describe("same-tab return notice", () => {
  it("shows one verified same-tab return notice", () => {
    const tracker = new AwayNoticeTracker();
    tracker.hide({ connection: "online", phase: "working", sessionId: "chat-1" }, 1_000);
    tracker.record("chat-1", "result");

    const returnId = tracker.beginReturn();
    expect(tracker.resume(onlineIdle, 21_000, returnId)).toEqual({
      durationMs: 8_000,
      text: "The run finished while you were away.",
    });
    expect(tracker.resume(onlineIdle, 22_000, returnId)).toBeUndefined();
  });

  it("does not claim completion without a received result", () => {
    const tracker = new AwayNoticeTracker();
    tracker.hide({ connection: "online", phase: "working", sessionId: "chat-1" }, 0);

    expect(tracker.resume(onlineIdle, 20_000, tracker.beginReturn())).toEqual({
      durationMs: 8_000,
      text: "This chat is idle.",
    });
  });

  it("requires 20 seconds, a matching chat, and refreshed facts", () => {
    const tracker = new AwayNoticeTracker();
    tracker.hide({ connection: "online", phase: "working", sessionId: "chat-1" }, 0);
    expect(tracker.resume(onlineIdle, 19_999, tracker.beginReturn())).toBeUndefined();

    tracker.hide({ connection: "online", phase: "working", sessionId: "chat-1" }, 20_000);
    expect(
      tracker.resume({ ...onlineIdle, sessionId: "chat-2" }, 40_000, tracker.beginReturn()),
    ).toBeUndefined();

    tracker.hide({ connection: "online", phase: "working", sessionId: "chat-1" }, 50_000);
    expect(
      tracker.resume({ ...onlineIdle, refreshed: false }, 70_000, tracker.beginReturn()),
    ).toBeUndefined();
  });

  it("uses only the current verified state and received approval events", () => {
    const tracker = new AwayNoticeTracker();
    tracker.hide({ connection: "online", phase: "awaiting", sessionId: "chat-1" }, 0);
    tracker.record("chat-1", "approval-resolved");
    expect(tracker.resume(onlineIdle, 20_000, tracker.beginReturn())?.text).toBe(
      "An approval was resolved while you were away.",
    );

    tracker.hide({ connection: "online", phase: "working", sessionId: "chat-1" }, 30_000);
    expect(
      tracker.resume(
        { ...onlineIdle, connection: "offline", phase: "unknown" },
        50_000,
        tracker.beginReturn(),
      )?.text,
    ).toBe("Mecatl is offline.");
  });

  it("ignores a return refresh that finishes after another visibility flip", () => {
    const tracker = new AwayNoticeTracker();
    const facts = { connection: "online" as const, phase: "working" as const, sessionId: "chat-1" };
    tracker.hide(facts, 0);
    const firstReturn = tracker.beginReturn();
    tracker.restartHide(facts, 21_000);

    expect(tracker.isCurrentReturn(firstReturn)).toBe(false);
    expect(tracker.resume(onlineIdle, 45_000, firstReturn)).toBeUndefined();
    expect(tracker.resume(onlineIdle, 45_000, tracker.beginReturn())?.text).toBe(
      "This chat is idle.",
    );

    tracker.restartHide(facts, 50_000);
    expect(tracker.resume(onlineIdle, 69_999, tracker.beginReturn())).toBeUndefined();
  });
});

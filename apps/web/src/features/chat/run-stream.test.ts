// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import {
  abortsOnSessionSwitch,
  acceptsDelivery,
  CATCHING_UP_NOTICE,
  controlTarget,
  decideTruncation,
  drainsQueue,
  MAX_ACTIVITY_REATTACHES,
  MISSING_HISTORY_NOTICE,
  ownsChatView,
  REPLAY_LIMIT_NOTICE,
  runStreamEnd,
} from "./run-stream";

function event(kind: string): RunStreamEvent {
  return {
    event: { kind, runId: "run-a", seq: "1", text: "", turn: 1, unknown: false },
    type: "run.event",
  };
}

describe("decideTruncation", () => {
  it("reattaches from the cursor on a bound truncation instead of failing", () => {
    const decision = decideTruncation(
      { cursor: "c-42", reason: "bound", type: "run.truncated" },
      0,
    );
    expect(decision).toEqual({ action: "reattach", cursor: "c-42", notice: CATCHING_UP_NOTICE });
  });

  it("reports missing history on a gap without claiming the run stopped", () => {
    const decision = decideTruncation({ cursor: "c-9", reason: "gap", type: "run.truncated" }, 0);
    expect(decision).toEqual({ action: "missing-history", notice: MISSING_HISTORY_NOTICE });
    expect(decision.notice).not.toMatch(/stopped before|failed/i);
  });

  it("stops following once the reattach budget for one open is spent", () => {
    const truncation = { cursor: "c-1", reason: "bound", type: "run.truncated" } as const;
    expect(decideTruncation(truncation, MAX_ACTIVITY_REATTACHES - 1).action).toBe("reattach");
    expect(decideTruncation(truncation, MAX_ACTIVITY_REATTACHES)).toEqual({
      action: "stop-following",
      notice: REPLAY_LIMIT_NOTICE,
    });
  });

  it("stops following a bound truncation that carries no cursor", () => {
    expect(decideTruncation({ cursor: "", reason: "bound", type: "run.truncated" }, 0).action).toBe(
      "stop-following",
    );
  });

  it("describes the replay truthfully as older history first, then the live run", () => {
    expect(CATCHING_UP_NOTICE).toMatch(/older activity first/);
    expect(CATCHING_UP_NOTICE).not.toMatch(/most recent/);
  });
});

describe("runStreamEnd", () => {
  it("reports no failure for a stream the view stopped following", () => {
    expect(runStreamEnd({ sawResult: false }, true, "prompt")).toEqual({ kind: "unfollowed" });
  });

  it("keeps the no-result fallback for a stream that ended on its own", () => {
    expect(runStreamEnd({ sawResult: false }, false, "prompt")).toEqual({
      failure: {
        message: "The agent stopped before returning a result.",
        permanent: false,
        prompt: "prompt",
      },
      kind: "settled",
    });
    expect(runStreamEnd({ sawResult: true }, false, "prompt")).toEqual({
      failure: undefined,
      kind: "settled",
    });
  });
});

describe("run ownership", () => {
  it("ignores a delivery once the view shows another session", () => {
    const owner = { sessionId: "chat-a" };
    expect(acceptsDelivery(owner, owner, "chat-a", event("permission.ask"))).toBe(true);
    expect(acceptsDelivery(owner, owner, "chat-b", event("permission.ask"))).toBe(false);
  });

  it("ignores a delivery from a stream that is no longer the active run", () => {
    const stale = { sessionId: "chat-a" };
    const current = { sessionId: "chat-a" };
    expect(ownsChatView(stale, current, "chat-a")).toBe(false);
    expect(acceptsDelivery(stale, current, "chat-a", event("message.delta"))).toBe(false);
    expect(acceptsDelivery(stale, undefined, "chat-a", event("message.delta"))).toBe(false);
  });

  it("ignores a run boundary that names a session other than the owner", () => {
    const owner = { sessionId: "chat-a" };
    const started = (sessionId: string): RunStreamEvent => ({
      runId: "run-1",
      sessionId,
      type: "run.started",
    });
    expect(acceptsDelivery(owner, owner, "chat-a", started("chat-a"))).toBe(true);
    expect(acceptsDelivery(owner, owner, "chat-a", started("chat-b"))).toBe(false);
  });

  it("aborts the active stream when the view switches to another session", () => {
    const owner = { sessionId: "chat-a" };
    expect(abortsOnSessionSwitch(owner, "chat-b")).toBe(true);
    expect(abortsOnSessionSwitch(owner, undefined)).toBe(true);
    expect(abortsOnSessionSwitch(owner, "chat-a")).toBe(false);
    expect(abortsOnSessionSwitch(undefined, "chat-b")).toBe(false);
  });

  it("aborts a draft run that has not created its session yet when the view switches", () => {
    expect(abortsOnSessionSwitch({}, "chat-b")).toBe(true);
    expect(abortsOnSessionSwitch({}, undefined)).toBe(false);
  });

  it("addresses controls only to the run of the viewed session", () => {
    const target = { runId: "run-a", sessionId: "chat-a" };
    expect(controlTarget(target, "chat-a")).toBe(target);
    expect(controlTarget(target, "chat-b")).toBeUndefined();
    expect(controlTarget(undefined, "chat-a")).toBeUndefined();
  });

  it("drains a queue only into its own, still-viewed session after a settled stream", () => {
    const settled = { kind: "settled" } as const;
    expect(drainsQueue(settled, "chat-a", "chat-a", false)).toBe(true);
    expect(drainsQueue(settled, "chat-a", "chat-b", false)).toBe(false);
    expect(drainsQueue(settled, "chat-a", "chat-a", true)).toBe(false);
    expect(drainsQueue(settled, undefined, undefined, false)).toBe(false);
    expect(drainsQueue({ kind: "unfollowed" }, "chat-a", "chat-a", false)).toBe(false);
  });
});

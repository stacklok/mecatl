// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import { applyRunDelivery, initialRunDeliveryState } from "./chat-state";
import {
  abortsOnSessionSwitch,
  acceptsDelivery,
  CATCHING_UP_NOTICE,
  controlTarget,
  createActivityDeduplicator,
  decideTruncation,
  drainsQueue,
  isStaleRunControl,
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
  it("settles an authorization handoff but leaves an incomplete continuation uncertain", () => {
    expect(runStreamEnd({ sawResult: false, authorizationStatus: true }, false)).toEqual({
      kind: "authorization",
    });
    expect(
      runStreamEnd(
        { sawResult: false, authorizationStatus: true, continuationStarted: true },
        false,
      ),
    ).toEqual({ kind: "uncertain" });
    expect(
      runStreamEnd({ sawResult: false, authorizationPark: true, continuationStarted: true }, false),
    ).toEqual({ kind: "authorization" });
    expect(drainsQueue({ kind: "authorization" }, "chat-a", "chat-a", false)).toBe(false);
  });

  it("reports no failure for a stream the view stopped following", () => {
    expect(runStreamEnd({ sawResult: false }, true)).toEqual({ kind: "unfollowed" });
  });

  it("keeps an abrupt close uncertain until a result or run.error arrives", () => {
    expect(runStreamEnd({ sawResult: false }, false)).toEqual({ kind: "uncertain" });
    expect(drainsQueue({ kind: "uncertain" }, "chat-a", "chat-a", false)).toBe(false);
    expect(runStreamEnd({ sawResult: true }, false)).toEqual({
      failure: undefined,
      kind: "settled",
    });
  });

  it("keeps gaps and abrupt closes separate from results", () => {
    const gap = decideTruncation({ cursor: "", reason: "gap", type: "run.truncated" }, 0);
    expect(gap.action).toBe("missing-history");
    expect(runStreamEnd({ sawResult: false }, true)).toEqual({ kind: "unfollowed" });
    expect(runStreamEnd({ sawResult: false }, false)).toEqual({ kind: "uncertain" });
    const failure = { message: "provider down", permanent: false, prompt: "prompt" };
    expect(runStreamEnd({ failure, sawResult: false }, false)).toEqual({
      failure,
      kind: "settled",
    });
  });
});

describe("bounded replay", () => {
  it("continues bounded replay without duplicate messages", () => {
    const accepts = createActivityDeduplicator();
    const options = {
      newId: (() => {
        let n = 0;
        return () => `id-${++n}`;
      })(),
      now: 1,
      replay: true,
    };
    let state = initialRunDeliveryState("assistant", "");
    const frames: RunStreamEvent[] = [
      { runId: "run-a", sessionId: "chat-a", type: "run.started" },
      {
        event: {
          kind: "user_prompt",
          runId: "run-a",
          seq: "1",
          text: "first",
          turn: 0,
          unknown: false,
        },
        type: "run.event",
      },
      {
        event: {
          kind: "message.delta",
          runId: "run-a",
          seq: "2",
          text: "one",
          turn: 0,
          unknown: false,
        },
        type: "run.event",
      },
      { cursor: "c2", reason: "bound", type: "run.truncated" },
      // A reattach can overlap a delivered boundary; it must not duplicate a delta.
      {
        event: {
          kind: "message.delta",
          runId: "run-a",
          seq: "2",
          text: "one",
          turn: 0,
          unknown: false,
        },
        type: "run.event",
      },
      {
        event: {
          kind: "result",
          runId: "run-a",
          seq: "3",
          text: "",
          turn: 0,
          unknown: false,
          payload: { stop: "end_turn" },
        },
        type: "run.event",
      },
      {
        event: {
          kind: "user_prompt",
          runId: "run-b",
          seq: "4",
          text: "second",
          turn: 0,
          unknown: false,
        },
        type: "run.event",
      },
      {
        event: {
          kind: "message.delta",
          runId: "run-b",
          seq: "5",
          text: "two",
          turn: 0,
          unknown: false,
        },
        type: "run.event",
      },
    ];
    const resumes: string[] = [];
    for (const frame of frames) {
      if (frame.type === "run.truncated") {
        const decision = decideTruncation(frame, resumes.length);
        if (decision.action === "reattach") resumes.push(decision.cursor);
      } else if (accepts(frame)) {
        state = applyRunDelivery(state, frame, options);
      }
    }
    expect(resumes).toEqual(["c2"]);
    expect(state.runId).toBe("run-b");
    expect(state.messages.map((message) => message.content)).toEqual([
      "first",
      "one",
      "second",
      "two",
    ]);
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

  it("recognizes only the BFF stale-run conflict for a queue fallback", () => {
    expect(isStaleRunControl({ code: "stale_run_control", status: 409 })).toBe(true);
    expect(isStaleRunControl({ code: "runtime_unavailable", status: 503 })).toBe(false);
    expect(isStaleRunControl({ code: "stale_run_control", status: 400 })).toBe(false);
    expect(isStaleRunControl(new Error("network down"))).toBe(false);
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

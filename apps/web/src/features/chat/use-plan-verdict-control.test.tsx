// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client } from "@mecatl-studio/contracts/client";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setRequestRecoveryState } from "../../lib/api-client";
import type { ApprovalRequest } from "./approval-panel";
import {
  PLAN_ACCEPTED_NOTICE,
  PLAN_ITERATE_NOTICE,
  PLAN_STALE_REASON,
  PLAN_START_FAILED_ERROR,
  PLAN_START_UNCONFIRMED_ERROR,
} from "./plan-continuation";
import { usePlanVerdictControl } from "./use-plan-verdict-control";
import { VerdictLedger } from "./verdict-ledger";

const target = { askId: "ask-1", runId: "run-1", sessionId: "chat-1" };
const plan: ApprovalRequest = {
  args: '{"plan":"Ship"}',
  askId: "ask-1",
  controlTarget: target,
  reason: "Review",
  tool: "PresentPlan",
};

// The verdict POST is faked at the fetch boundary; the continuation follow is injected.
const posts = vi.fn<(request: Request) => Promise<Response>>();
const follow = vi.fn<NonNullable<Parameters<typeof usePlanVerdictControl>[0]["follow"]>>();

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
beforeEach(() => {
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
  client.setConfig({ baseUrl: "http://studio.test" });
  posts.mockReset().mockResolvedValue(new Response(null, { status: 204 }));
  follow.mockReset();
  vi.stubGlobal("fetch", posts);
});

function setup(overrides: Partial<Parameters<typeof usePlanVerdictControl>[0]> = {}) {
  const callbacks = {
    onStarted: vi.fn(),
    onUncertain: vi.fn(),
    retract: vi.fn(),
    setControlPending: vi.fn(),
    setError: vi.fn(),
    setNotice: vi.fn(),
  };
  const ledger = new VerdictLedger();
  const hook = renderHook(() =>
    usePlanVerdictControl({
      activeTarget: () => ({ runId: "run-1", sessionId: "chat-1" }),
      controlPending: false,
      features: ["exact_plan_ask_control"],
      isViewed: (id) => id === "chat-1",
      follow,
      ledger,
      ...callbacks,
      ...overrides,
    }),
  );
  return { ...callbacks, hook, ledger };
}

describe("usePlanVerdictControl", () => {
  it("reports one reason a plan cannot be decided, whichever surface asks", () => {
    expect(setup().hook.result.current.planUnavailableReason(plan)).toBeUndefined();
    expect(setup({ features: [] }).hook.result.current.planUnavailableReason(plan)).toMatch(
      /unavailable on this Mecatl server/,
    );
    for (const overrides of [
      { activeTarget: () => ({ runId: "run-2", sessionId: "chat-1" }) },
      { activeTarget: () => undefined },
      { isViewed: () => false },
    ])
      expect(setup(overrides).hook.result.current.planUnavailableReason(plan)).toBe(
        PLAN_STALE_REASON,
      );
  });

  it("stays silent and sends nothing when the plan is blocked", async () => {
    const busy = setup({ controlPending: true });
    const inFlight = setup();
    inFlight.ledger.begin(target);
    const uncertain = setup();
    uncertain.ledger.begin(target);
    uncertain.ledger.markUncertain(target);
    const stale = setup({ isViewed: () => false });
    for (const { hook, setError, setControlPending } of [busy, inFlight, uncertain, stale]) {
      await act(async () => hook.result.current.respondToPlan(plan, "iterate"));
      expect(setError).not.toHaveBeenCalled();
      expect(setControlPending).not.toHaveBeenCalled();
    }
    const notPlan = setup();
    await act(async () =>
      notPlan.hook.result.current.respondToPlan({ ...plan, tool: "Edit" }, "iterate"),
    );
    expect(posts).not.toHaveBeenCalled();
  });

  it("settles an iterate verdict once and never follows a continuation", async () => {
    const { hook, ledger, retract, setError, setNotice, setControlPending } = setup();
    await act(async () => hook.result.current.respondToPlan(plan, "iterate"));
    expect(posts).toHaveBeenCalledTimes(1);
    expect(ledger.phase(target)).toBe("acknowledged");
    expect(retract).toHaveBeenCalledWith(target);
    expect(setNotice).toHaveBeenCalledWith(PLAN_ITERATE_NOTICE);
    // A fresh verdict clears whatever the last one left behind, on both surfaces.
    expect(setError).toHaveBeenCalledWith(undefined);
    expect(follow).not.toHaveBeenCalled();
    expect(setControlPending.mock.calls).toEqual([[true], [false]]);
  });

  it("hands the surface the execution run an approval started, and records the reattach", async () => {
    follow.mockResolvedValue({
      kind: "started",
      resumeFrom: "cursor-1",
      runId: "run-2",
    });
    const { hook, onStarted, setNotice } = setup();
    await act(async () => hook.result.current.respondToPlan(plan, "approve"));
    expect(setNotice).toHaveBeenCalledWith(PLAN_ACCEPTED_NOTICE);
    expect(onStarted).toHaveBeenCalledWith({
      resumeFrom: "cursor-1",
      runId: "run-2",
      sessionId: "chat-1",
    });
    expect(hook.result.current.reattachRef.current).toEqual({
      cursor: "cursor-1",
      runId: "run-2",
      sessionId: "chat-1",
    });
    expect(hook.result.current.continuationActive).toBe(true);
    act(() => hook.result.current.reset());
    expect(hook.result.current.reattachRef.current).toBeUndefined();
    expect(hook.result.current.continuationActive).toBe(false);
  });

  it.each([
    [{ kind: "failed" as const }, PLAN_START_FAILED_ERROR],
    [{ kind: "uncertain" as const }, PLAN_START_UNCONFIRMED_ERROR],
  ])("tells the reader when an approved plan's execution is %o", async (evidence, message) => {
    follow.mockResolvedValue(evidence);
    const { hook, onStarted, setError, setNotice } = setup();
    await act(async () => hook.result.current.respondToPlan(plan, "approve"));
    expect(setError).toHaveBeenLastCalledWith(message);
    expect(setNotice).toHaveBeenLastCalledWith(undefined);
    expect(onStarted).not.toHaveBeenCalled();
  });

  it("leaves an ambiguous failure blocked and uncertain, and never retracts it", async () => {
    posts.mockRejectedValue(new Error("connection lost"));
    const { hook, ledger, onUncertain, retract, setError } = setup();
    await act(async () => hook.result.current.respondToPlan(plan, "iterate"));
    expect(ledger.phase(target)).toBe("uncertain");
    expect(onUncertain).toHaveBeenCalled();
    expect(retract).not.toHaveBeenCalled();
    expect(setError).toHaveBeenLastCalledWith(expect.stringContaining("Could not confirm"));
  });

  it("touches nothing a session the reader has left", async () => {
    let viewed = true;
    const { hook, setControlPending, setNotice } = setup({ isViewed: () => viewed });
    posts.mockImplementation(async () => {
      viewed = false;
      return new Response(null, { status: 204 });
    });
    await act(async () => hook.result.current.respondToPlan(plan, "iterate"));
    expect(setNotice).not.toHaveBeenCalled();
    expect(setControlPending.mock.calls).toEqual([[true]]);
  });
});

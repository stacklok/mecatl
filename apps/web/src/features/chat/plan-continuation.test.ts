// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { client } from "@mecatl-studio/contracts/client";
import { expect, it, vi } from "vitest";
import {
  exactPlanControlAvailability,
  followPlanContinuation,
  followPlanContinuationFromBff,
} from "./plan-continuation";

type Frame = { cursor?: string; delivery: RunStreamEvent };
const target = { askId: "ask-plan", planRunId: "run-plan", sessionId: "session-1" };

function event(kind: string, runId: string, payload: unknown, seq: string): RunStreamEvent {
  return {
    event: { kind, payload, runId, seq, text: "", turn: 1, unknown: false },
    type: "run.event",
  };
}

function pages(...frames: Frame[][]) {
  const open = vi.fn(async (_cursor?: string): Promise<AsyncIterable<Frame>> => {
    const page = frames.shift() ?? [];
    return {
      async *[Symbol.asyncIterator]() {
        for (const frame of page) yield frame;
      },
    };
  });
  return open;
}

it("follows durable plan continuation across the old terminal and a cursor reattach", async () => {
  const open = pages(
    [
      {
        cursor: "approved-terminal",
        delivery: event("result", "run-plan", { stop: "plan_approved" }, "1"),
      },
    ],
    [
      {
        cursor: "other-failure",
        delivery: event(
          "plan.continuation_failed",
          "",
          { askId: "different", planRunId: "run-plan" },
          "2",
        ),
      },
      { cursor: "execution", delivery: event("message.delta", "run-execution", {}, "1") },
    ],
  );
  const wait = vi.fn(async () => {});
  await expect(followPlanContinuation(target, open, { maxReads: 3, wait })).resolves.toEqual({
    kind: "started",
    resumeFrom: "other-failure",
    runId: "run-execution",
  });
  expect(open.mock.calls.map(([cursor]) => cursor)).toEqual([undefined, "approved-terminal"]);
  expect(wait).toHaveBeenCalledOnce();
});

it("uses only a matching durable failure and reports uncertainty at the polling bound", async () => {
  const failure = event(
    "plan.continuation_failed",
    "",
    { askId: "ask-plan", planRunId: "run-plan", error: "must never render" },
    "2",
  );
  const failed = pages(
    [{ cursor: "terminal", delivery: event("result", "run-plan", { stop: "plan_approved" }, "1") }],
    [{ cursor: "failure", delivery: failure }],
  );
  await expect(
    followPlanContinuation(target, failed, { maxReads: 3, wait: async () => {} }),
  ).resolves.toEqual({ kind: "failed" });

  const uncertain = pages(
    [{ cursor: "c1", delivery: event("result", "run-plan", { stop: "plan_approved" }, "1") }],
    [
      {
        cursor: "c2",
        delivery: event(
          "plan.continuation_failed",
          "",
          { askId: "other", planRunId: "run-plan" },
          "2",
        ),
      },
    ],
    [],
  );
  const wait = vi.fn(async () => {});
  await expect(followPlanContinuation(target, uncertain, { maxReads: 3, wait })).resolves.toEqual({
    kind: "uncertain",
  });
  expect(uncertain.mock.calls.map(([cursor]) => cursor)).toEqual([undefined, "c1", "c2"]);
  expect(wait).toHaveBeenCalledTimes(2);
});

it("keeps exact plan review read-only without the advertised feature", () => {
  expect(exactPlanControlAvailability(undefined)).toEqual({
    available: false,
    reason: expect.stringMatching(/unavailable/i),
  });
  expect(exactPlanControlAvailability(["server_info"])).toEqual({
    available: false,
    reason: expect.stringMatching(/unavailable/i),
  });
  expect(exactPlanControlAvailability(["exact_plan_ask_control"])).toEqual({
    available: true,
  });
});

it("bounds a live activity stream that stays open after plan acknowledgement", async () => {
  const fetch = vi.fn(
    async (request: Request) =>
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            request.signal.addEventListener("abort", () => controller.error(new Error("aborted")), {
              once: true,
            });
          },
        }),
        { headers: { "Content-Type": "text/event-stream" } },
      ),
  );
  vi.stubGlobal("fetch", fetch);
  client.setConfig({ baseUrl: "http://studio.test" });
  const controller = new AbortController();
  let timeout: ReturnType<typeof setTimeout> | undefined;
  try {
    const result = await Promise.race([
      followPlanContinuationFromBff(target, controller.signal, undefined, 25),
      new Promise<"timed out">((resolve) => {
        timeout = setTimeout(() => resolve("timed out"), 250);
      }),
    ]);
    expect(fetch).toHaveBeenCalled();
    expect(fetch.mock.calls[0]?.[0].signal.aborted).toBe(true);
    expect(result).toEqual({ kind: "uncertain" });
    expect(controller.signal.aborted).toBe(false);
  } finally {
    if (timeout) clearTimeout(timeout);
    controller.abort();
    vi.unstubAllGlobals();
  }
});

// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { watchSessionActivity } from "@mecatl-studio/contracts/generated";

export interface PlanContinuationTarget {
  askId: string;
  planRunId: string;
  sessionId: string;
}

export type PlanContinuationEvidence =
  | { kind: "started"; runId: string }
  | { kind: "failed" }
  | { kind: "uncertain" };

export interface PlanActivityFrame {
  cursor?: string;
  delivery: RunStreamEvent;
}

export type OpenPlanActivity = (resumeFrom?: string) => Promise<AsyncIterable<PlanActivityFrame>>;

/** Only the advertised strict control may make a plan card actionable. */
export function exactPlanControlAvailability(features?: readonly string[]): {
  available: boolean;
  reason?: string;
} {
  return features?.includes("exact_plan_ask_control")
    ? { available: true }
    : {
        available: false,
        reason: "Exact plan review is unavailable on this Mecatl server.",
      };
}

function payloadRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

/** A 204 plan verdict is acknowledgement, not evidence that execution began. */
export async function followPlanContinuation(
  target: PlanContinuationTarget,
  open: OpenPlanActivity,
  options: {
    maxReads?: number;
    resumeFrom?: string;
    signal?: AbortSignal;
    wait?: (milliseconds: number) => Promise<void>;
  } = {},
): Promise<PlanContinuationEvidence> {
  const maxReads = options.maxReads ?? 4;
  const wait =
    options.wait ??
    ((milliseconds) => new Promise<void>((resolve) => setTimeout(resolve, milliseconds)));
  let resumeFrom = options.resumeFrom;
  let approvedTerminal = false;

  for (let read = 0; read < maxReads; read += 1) {
    if (options.signal?.aborted) return { kind: "uncertain" };
    try {
      const stream = await open(resumeFrom);
      for await (const { cursor, delivery } of stream) {
        if (cursor) resumeFrom = cursor;
        if (delivery.type === "run.truncated") {
          if (delivery.reason === "gap" || !delivery.cursor) return { kind: "uncertain" };
          resumeFrom = delivery.cursor;
          break;
        }
        if (delivery.type === "run.error") break;
        if (delivery.type === "run.started") {
          if (
            approvedTerminal &&
            delivery.runId !== target.planRunId &&
            delivery.sessionId === target.sessionId
          )
            return { kind: "started", runId: delivery.runId };
          continue;
        }
        const event = delivery.event;
        const payload = payloadRecord(event.payload);
        if (
          !event.unknown &&
          event.kind === "plan.continuation_failed" &&
          payload?.planRunId === target.planRunId &&
          payload.askId === target.askId
        ) {
          return { kind: "failed" };
        }
        if (
          event.kind === "result" &&
          event.runId === target.planRunId &&
          payload?.stop === "plan_approved"
        ) {
          approvedTerminal = true;
          continue;
        }
        if (approvedTerminal && event.runId && event.runId !== target.planRunId)
          return { kind: "started", runId: event.runId };
      }
    } catch {
      // The acknowledgement may already have committed; only activity can settle it.
    }
    if (read + 1 < maxReads) await wait(250);
  }
  return { kind: "uncertain" };
}

/** Reads Studio's cursor-bearing BFF activity stream after an allow acknowledgement. */
export async function followPlanContinuationFromBff(
  target: PlanContinuationTarget,
  signal: AbortSignal,
  resumeFrom?: string,
  maxDurationMs = 10_000,
): Promise<PlanContinuationEvidence> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (signal.aborted) abort();
  else signal.addEventListener("abort", abort, { once: true });
  let deadlineTimer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<PlanContinuationEvidence>((resolve) => {
    deadlineTimer = setTimeout(() => {
      abort();
      resolve({ kind: "uncertain" });
    }, maxDurationMs);
  });
  try {
    return await Promise.race([
      followPlanContinuation(
        target,
        async (cursor) => {
          let latestCursor = cursor;
          const response = await watchSessionActivity({
            onSseEvent: ({ id }) => {
              if (id) latestCursor = id;
            },
            path: { sessionId: target.sessionId },
            query: cursor ? { resumeFrom: cursor } : undefined,
            signal: controller.signal,
            sseMaxRetryAttempts: 1,
          });
          return {
            async *[Symbol.asyncIterator]() {
              for await (const delivery of response.stream) {
                yield { cursor: latestCursor, delivery };
              }
            },
          };
        },
        { resumeFrom, signal: controller.signal },
      ),
      deadline,
    ]);
  } finally {
    if (deadlineTimer !== undefined) clearTimeout(deadlineTimer);
    signal.removeEventListener("abort", abort);
    controller.abort();
  }
}

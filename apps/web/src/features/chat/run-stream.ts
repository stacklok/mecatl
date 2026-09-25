// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent, RunTruncated } from "@mecatl-studio/contracts";
import type { RunDeliveryState, RunFailure } from "./chat-state";

/**
 * How many times one open of a chat may reattach the activity stream after a
 * bounded replay. Each reattach resumes after the last delivered cursor, so a
 * healthy stream always makes progress; the cap only stops a pathological
 * server from keeping the browser in an endless replay loop.
 */
export const MAX_ACTIVITY_REATTACHES = 50;

export const CATCHING_UP_NOTICE =
  "This chat has a long history. Replaying older activity first, then catching up to the live run.";

export const MISSING_HISTORY_NOTICE =
  "Some activity is missing from the live log, so this view cannot follow the run past that point. The transcript below shows the saved history.";

export const REPLAY_LIMIT_NOTICE =
  "This chat's history is too long to replay in full, so this view stopped following the run. The transcript below shows the saved history; reopen the chat to follow the run again.";

/** Match the daemon's active states when deciding whether a chat needs a live stream. */
export function isActiveSessionState(state: string | undefined): boolean {
  return state === "running" || state === "awaiting" || state === "authorizing";
}

/** What the workspace does when a stream ends with `run.truncated`. */
export type TruncationDecision =
  /** Keep reading: reattach the activity stream from `cursor`. */
  | { action: "reattach"; cursor: string; notice: string }
  /** A durable hole: stop following, say history is missing, load the transcript. */
  | { action: "missing-history"; notice: string }
  /** The reattach budget is spent (or no cursor came back): stop following and load the transcript. */
  | { action: "stop-following"; notice: string };

/**
 * Decides how to continue after a truncation frame. A truncation is a stream
 * boundary, never a run outcome: none of the decisions describe the run as
 * stopped or failed.
 */
export function decideTruncation(
  truncation: RunTruncated,
  reattaches: number,
  limit: number = MAX_ACTIVITY_REATTACHES,
): TruncationDecision {
  if (truncation.reason === "gap") {
    return { action: "missing-history", notice: MISSING_HISTORY_NOTICE };
  }
  if (!truncation.cursor || reattaches >= limit) {
    return { action: "stop-following", notice: REPLAY_LIMIT_NOTICE };
  }
  return { action: "reattach", cursor: truncation.cursor, notice: CATCHING_UP_NOTICE };
}

/** How a consumed run stream ended, as far as the workspace needs to react. */
export type RunStreamEnd =
  /** A result or run.error supplied an authoritative run outcome. */
  | { kind: "settled"; failure?: RunFailure }
  /** A run parked on, or a control reported, an external authorization without a result. */
  | { kind: "authorization" }
  /** The stream closed without an outcome; controls and queued prompts remain available. */
  | { kind: "uncertain" }
  /** The view stopped following the run because of a truncation; the run's outcome is unknown. */
  | { kind: "unfollowed" };

/**
 * The end of a consumed stream. An unfollowed stream reports no failure and
 * must not drain the queue: the run may still be working.
 */
export function runStreamEnd(
  state: Pick<RunDeliveryState, "failure" | "sawResult"> & {
    authorizationPark?: boolean;
    authorizationStatus?: boolean;
    continuationStarted?: boolean;
  },
  unfollowed: boolean,
): RunStreamEnd {
  if (unfollowed) return { kind: "unfollowed" };
  if (
    !state.failure &&
    !state.sawResult &&
    (state.authorizationPark || (state.authorizationStatus && !state.continuationStarted))
  )
    return { kind: "authorization" };
  if (!state.sawResult && !state.failure) return { kind: "uncertain" };
  return { failure: state.failure, kind: "settled" };
}

/**
 * Suppresses overlapping durable event frames across replay reattachments.
 * Event sequence numbers are decimal strings from the BFF; run boundaries are
 * still passed through because the reducer handles repeated starts itself.
 */
export function createActivityDeduplicator(): (delivery: RunStreamEvent) => boolean {
  const latestByRun = new Map<string, bigint>();
  return (delivery) => {
    if (delivery.type !== "run.event") return true;
    const { runId, seq } = delivery.event;
    // Authorization controls publish session-scoped status events with an
    // empty run ID and seq 0. The activity cursor, not (runId, seq), orders them.
    if (
      !runId &&
      (delivery.event.kind === "authorization.required" ||
        delivery.event.kind === "authorization.resolved")
    )
      return true;
    if (!/^\d+$/u.test(seq)) return true;
    const current = BigInt(seq);
    const previous = latestByRun.get(runId);
    if (previous !== undefined && current <= previous) return false;
    latestByRun.set(runId, current);
    return true;
  };
}

/** A stream's claim on the chat view: the session its run belongs to. */
export interface RunOwnership {
  sessionId?: string;
}

/**
 * True while `owner` is still the active run and the chat view still shows
 * the session it belongs to. Every state write and every control of a run is
 * gated on this, so a stream for a chat the user left cannot touch the chat
 * they switched to.
 */
export function ownsChatView(
  owner: RunOwnership,
  activeOwner: RunOwnership | undefined,
  viewedSessionId: string | undefined,
): boolean {
  return activeOwner === owner && owner.sessionId === viewedSessionId;
}

/**
 * True when `delivery` may be applied to the view on behalf of `owner`: the
 * stream still owns the view, and a run boundary names the owning session.
 */
export function acceptsDelivery(
  owner: RunOwnership,
  activeOwner: RunOwnership | undefined,
  viewedSessionId: string | undefined,
  delivery: RunStreamEvent,
): boolean {
  if (!ownsChatView(owner, activeOwner, viewedSessionId)) return false;
  if (delivery.type === "run.started") return delivery.sessionId === owner.sessionId;
  return true;
}

/** True when switching the view to `nextSessionId` must abort the active run's stream. */
export function abortsOnSessionSwitch(
  activeOwner: RunOwnership | undefined,
  nextSessionId: string | undefined,
): boolean {
  return activeOwner !== undefined && activeOwner.sessionId !== nextSessionId;
}

/** The exact run a control (stop, steer, verdict) addresses. */
export interface RunTarget {
  runId: string;
  sessionId: string;
}

/**
 * The run a control from the current view may address, or `undefined` when
 * the active run belongs to another session.
 */
export function controlTarget(
  target: RunTarget | undefined,
  viewedSessionId: string | undefined,
): RunTarget | undefined {
  return target && target.sessionId === viewedSessionId ? target : undefined;
}

/** A stale control is the one BFF error for which steering may become a queue. */
export function isStaleRunControl(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    error.status === 409 &&
    "code" in error &&
    error.code === "stale_run_control"
  );
}

/** True when a queue for `queueSessionId` may start its next run in the current view. */
export function drainsQueue(
  end: RunStreamEnd,
  queueSessionId: string | undefined,
  viewedSessionId: string | undefined,
  aborted: boolean,
): queueSessionId is string {
  return (
    !aborted &&
    end.kind === "settled" &&
    queueSessionId !== undefined &&
    queueSessionId === viewedSessionId
  );
}

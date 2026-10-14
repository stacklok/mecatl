// SPDX-License-Identifier: Apache-2.0

import { resolvePlanAsk } from "@mecatl-studio/contracts/generated";
import { useCallback, useRef, useState } from "react";
import { errorMessage } from "../../lib/error-message";
import type { ApprovalRequest } from "./approval-panel";
import {
  exactPlanControlAvailability,
  followPlanContinuationFromBff,
  PLAN_ACCEPTED_NOTICE,
  PLAN_ITERATE_NOTICE,
  PLAN_STALE_REASON,
  PLAN_START_FAILED_ERROR,
  PLAN_START_UNCONFIRMED_ERROR,
} from "./plan-continuation";
import type { PlanVerdict } from "./plan-review-card";
import type { VerdictLedger } from "./verdict-ledger";

type AskTarget = { askId: string; runId: string; sessionId: string };

/** The one reader a surface must move onto the execution run a plan approval started. */
export interface PlanReattach {
  cursor?: string;
  runId: string;
  sessionId: string;
}

interface PlanVerdictControlOptions {
  /** The run this surface may address right now, if any. */
  activeTarget: () => { runId: string; sessionId: string } | undefined;
  /** True while any other control (stop, an ordinary verdict) is mid-request. */
  controlPending: boolean;
  features: readonly string[] | undefined;
  /** Reads the continuation evidence; only tests replace it. */
  follow?: typeof followPlanContinuationFromBff;
  isViewed: (sessionId: string) => boolean;
  ledger: VerdictLedger;
  /** Where the surface re-renders after a verdict turns uncertain. */
  onUncertain: () => void;
  /**
   * The only surface-specific step: move this surface's reader onto the
   * execution run. The hook has already recorded `reattachRef`.
   */
  onStarted: (started: { resumeFrom?: string; runId: string; sessionId: string }) => void;
  /** The ask is settled by this verdict; called whether or not its session is still viewed. */
  retract: (target: AskTarget) => void;
  setControlPending: (pending: boolean) => void;
  setError: (message: string | undefined) => void;
  setNotice: (message: string | undefined) => void;
}

/**
 * The plan verdict flow both chat surfaces share: one `planUnavailableReason`,
 * one blocking rule, and one set of messages, so a click and Escape can never
 * disagree about whether a plan may be decided. The hook owns the follow of
 * the approval's continuation and the reattach it hands to the surface's
 * watch effect through `reattachRef`.
 */
export function usePlanVerdictControl(options: PlanVerdictControlOptions) {
  const followAbort = useRef<AbortController | undefined>(undefined);
  const reattachRef = useRef<PlanReattach | undefined>(undefined);
  const [continuationActive, setContinuationActive] = useState(false);

  const abort = useCallback(() => {
    followAbort.current?.abort();
    followAbort.current = undefined;
  }, []);

  /** A session switch: nothing of the old session's continuation may carry over. */
  const reset = useCallback(() => {
    abort();
    reattachRef.current = undefined;
    setContinuationActive(false);
  }, [abort]);

  const endContinuation = useCallback(() => setContinuationActive(false), []);

  function planUnavailableReason(approval: ApprovalRequest): string | undefined {
    const featureReason = exactPlanControlAvailability(options.features).reason;
    if (featureReason) return featureReason;
    const active = options.activeTarget();
    const target = approval.controlTarget;
    return !target ||
      target.askId !== approval.askId ||
      target.sessionId !== active?.sessionId ||
      target.runId !== active?.runId ||
      !options.isViewed(target.sessionId)
      ? PLAN_STALE_REASON
      : undefined;
  }

  async function respondToPlan(approval: ApprovalRequest, verdict: PlanVerdict) {
    const target = approval.controlTarget;
    if (
      approval.tool !== "PresentPlan" ||
      !target ||
      planUnavailableReason(approval) ||
      options.controlPending ||
      !options.ledger.begin(target)
    )
      return;
    const { ledger, isViewed, setError, setNotice } = options;
    options.setControlPending(true);
    setError(undefined);
    try {
      await resolvePlanAsk({
        body: { verdict },
        path: { askId: target.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      ledger.acknowledge(target);
      options.retract(target);
      if (!isViewed(target.sessionId)) return;
      if (verdict === "iterate") {
        setNotice(PLAN_ITERATE_NOTICE);
        return;
      }
      setNotice(PLAN_ACCEPTED_NOTICE);
      const controller = new AbortController();
      followAbort.current = controller;
      const evidence = await (options.follow ?? followPlanContinuationFromBff)(
        { askId: target.askId, planRunId: target.runId, sessionId: target.sessionId },
        controller.signal,
      );
      if (controller.signal.aborted || !isViewed(target.sessionId)) return;
      if (evidence.kind === "started") {
        // The verdict watcher establishes the new run and a durable replay
        // checkpoint. Transfer this view's one reader to that run so its
        // transcript and controls follow the same evidence.
        reattachRef.current = {
          cursor: evidence.resumeFrom,
          runId: evidence.runId,
          sessionId: target.sessionId,
        };
        setContinuationActive(true);
        options.onStarted({
          resumeFrom: evidence.resumeFrom,
          runId: evidence.runId,
          sessionId: target.sessionId,
        });
      } else {
        setNotice(undefined);
        setError(
          evidence.kind === "failed" ? PLAN_START_FAILED_ERROR : PLAN_START_UNCONFIRMED_ERROR,
        );
      }
    } catch (caught) {
      ledger.markUncertain(target);
      options.onUncertain();
      if (isViewed(target.sessionId))
        setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      followAbort.current = undefined;
      if (isViewed(target.sessionId)) options.setControlPending(false);
    }
  }

  return {
    abort,
    continuationActive,
    endContinuation,
    planUnavailableReason,
    reattachRef,
    reset,
    respondToPlan,
  };
}

// SPDX-License-Identifier: Apache-2.0

import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import { PlanReviewCard, type PlanVerdict } from "./plan-review-card";

/** The verdict wiring every surface hands its pending asks. */
export interface PendingAskControls {
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  onRespondToPlan?: (approval: ApprovalRequest, verdict: PlanVerdict) => void;
  planUnavailableReason?: (approval: ApprovalRequest) => string | undefined;
}

/** The list key for one pending ask: its run and ask, so a new ask remounts its card. */
export function pendingAskKey(approval: ApprovalRequest): string {
  return `${approval.controlTarget?.runId ?? ""}:${approval.askId}`;
}

/**
 * One pending `permission.ask` as its card: the plan review for `PresentPlan`,
 * the ordinary approval panel otherwise. An ask with no `controlTarget` can
 * never be decided, so it is disabled before the surface's own predicate is
 * asked. Callers rendering a list key each card with `pendingAskKey`.
 */
export function PendingAskCard({
  approval,
  controls,
}: {
  approval: ApprovalRequest;
  controls: PendingAskControls;
}) {
  const disabled = !approval.controlTarget || (controls.approvalDisabled?.(approval) ?? false);
  const key = pendingAskKey(approval);
  const uncertain = controls.approvalUncertain?.(approval);
  return approval.tool === "PresentPlan" ? (
    <PlanReviewCard
      approval={approval}
      disabled={disabled}
      key={key}
      onRespond={(verdict) => controls.onRespondToPlan?.(approval, verdict)}
      uncertain={uncertain}
      unavailableReason={controls.planUnavailableReason?.(approval)}
    />
  ) : (
    <ApprovalPanel
      approval={approval}
      disabled={disabled}
      key={key}
      onRespond={(verdict) => controls.onRespondToApproval?.(approval, verdict)}
      uncertain={uncertain}
    />
  );
}

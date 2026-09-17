import { isPlanAsk } from "@/features/agent/plan-ask";
import type { ApprovalRequest } from "@/features/agent/types";

/**
 * What the run is parked on, when it is parked: the streaming indicator's
 * phase names the wait instead of a tool/writing phase that is not moving.
 * "approval" is a permission ask; "plan" is a PresentPlan ask — the operator
 * is reviewing a plan, not authorizing a tool.
 */
export type AwaitingPhase = "approval" | "plan";

/**
 * The phase for the ask at the head of the queue (undefined = nothing is
 * parked): a PresentPlan ask is a plan review, every other ask an approval.
 */
export function awaitingPhaseFor(
  approval: Pick<ApprovalRequest, "toolName"> | null | undefined,
): AwaitingPhase | undefined {
  if (!approval) return undefined;
  return isPlanAsk(approval.toolName) ? "plan" : "approval";
}

/**
 * The activity-line label while the daemon waits on the operator, or null
 * when the run is not parked — the caller then falls back to the derived
 * phase (`streamingPhaseLabel`, which names the running tool). The wait wins
 * over every derived phase: "Running Bash" while the daemon is actually
 * waiting on the operator misreports who is holding the run.
 */
export function awaitingPhaseLabel(awaiting?: AwaitingPhase): string | null {
  switch (awaiting) {
    case "approval":
      return "Awaiting approval";
    case "plan":
      return "Plan review";
    default:
      return null;
  }
}

/** The header spinner's accessible name while a run is live. */
export function streamingSpinnerLabel(awaiting?: AwaitingPhase): string {
  switch (awaiting) {
    case "approval":
      return "Awaiting your approval";
    case "plan":
      return "Awaiting your plan review";
    default:
      return "Generating a response";
  }
}

// SPDX-License-Identifier: Apache-2.0

import { Button } from "../../components/ui/button";
import type { ApprovalRequest } from "./approval-panel";

export type PlanVerdict = "approve" | "accept_edits" | "iterate";

/** Small control bridge for the strict plan route; the review card owns rich plan presentation. */
export function PlanControlBridge({
  approval,
  disabled,
  onRespond,
  unavailableReason,
}: {
  approval: ApprovalRequest;
  disabled: boolean;
  onRespond: (verdict: PlanVerdict) => void;
  unavailableReason?: string;
}) {
  let plan = "";
  try {
    const args: unknown = JSON.parse(approval.args);
    if (
      typeof args === "object" &&
      args !== null &&
      "plan" in args &&
      typeof args.plan === "string"
    ) {
      plan = args.plan.trim();
    }
  } catch {
    // Invalid arguments cannot authorize execution.
  }

  return (
    <section
      className="mx-auto mb-3 w-[calc(100%-2rem)] max-w-3xl rounded-xl border p-4"
      aria-label="Plan review"
    >
      <h2 className="text-sm font-semibold">Plan review</h2>
      {approval.reason && <p className="mt-2 text-sm">{approval.reason}</p>}
      {plan && (
        <pre className="my-3 max-h-48 overflow-auto whitespace-pre-wrap rounded-lg border bg-background p-3 text-sm">
          {plan}
        </pre>
      )}
      {!plan && <p className="mt-2 text-sm">The plan text is unavailable or malformed.</p>}
      {unavailableReason ? (
        <p className="mt-2 text-sm" role="status">
          {unavailableReason}
        </p>
      ) : (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button disabled={disabled || !plan} onClick={() => onRespond("approve")} size="sm">
            Approve &amp; run
          </Button>
          <Button
            disabled={disabled || !plan}
            onClick={() => onRespond("accept_edits")}
            size="sm"
            variant="outline"
          >
            Auto-accept edits
          </Button>
          <Button
            disabled={disabled}
            onClick={() => onRespond("iterate")}
            size="sm"
            variant="ghost"
          >
            Iterate
          </Button>
        </div>
      )}
      <details className="mt-3 text-xs">
        <summary>Raw arguments</summary>
        <pre className="mt-2 whitespace-pre-wrap break-all">{approval.args || "{}"}</pre>
      </details>
    </section>
  );
}

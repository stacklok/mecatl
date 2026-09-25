// SPDX-License-Identifier: Apache-2.0

import { Button } from "../../components/ui/button";
import type { ApprovalRequest } from "./approval-panel";

export type PlanVerdict = "approve" | "accept_edits" | "iterate";

/** Present only scrubbed event arguments. The tool name chooses this view, not verdict authority. */
export function PlanReviewCard({
  approval,
  disabled,
  onRespond,
  uncertain = false,
  unavailableReason,
}: {
  approval: ApprovalRequest;
  disabled: boolean;
  onRespond: (verdict: PlanVerdict) => void;
  uncertain?: boolean;
  unavailableReason?: string;
}) {
  let plan = "";
  let note = "";
  try {
    const args: unknown = approval.args.length <= 65_536 ? JSON.parse(approval.args) : undefined;
    if (typeof args === "object" && args !== null && !Array.isArray(args)) {
      if ("plan" in args && typeof args.plan === "string" && args.plan.length <= 32_768) {
        plan = args.plan.trim();
      }
      if ("note" in args && typeof args.note === "string") note = args.note.slice(0, 8_192);
    }
  } catch {
    // Invalid arguments cannot authorize execution.
  }

  return (
    <section
      className="my-2 min-w-0 max-w-full rounded-xl border border-warning/40 bg-warning/5 p-4"
      aria-label="Plan review"
    >
      <h2 className="text-sm font-semibold">Plan review</h2>
      {approval.reason && <p className="mt-2 text-sm">{approval.reason}</p>}
      {note && <p className="mt-2 whitespace-pre-wrap break-words text-sm">{note}</p>}
      {plan && (
        <pre className="my-3 max-h-48 overflow-auto whitespace-pre-wrap rounded-lg border bg-background p-3 text-sm">
          {plan}
        </pre>
      )}
      {!plan && <p className="mt-2 text-sm">The plan text is unavailable or malformed.</p>}
      {uncertain && (
        <p className="mt-2 text-sm" role="status">
          This verdict's outcome is uncertain. Refresh activity before deciding again.
        </p>
      )}
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
        <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border bg-background p-3 font-mono">
          {approval.args ? approval.args.slice(0, 65_536) : "Arguments unavailable"}
        </pre>
        {approval.args.length > 65_536 && <p>Arguments truncated for display.</p>}
      </details>
    </section>
  );
}

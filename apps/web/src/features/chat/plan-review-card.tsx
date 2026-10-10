// SPDX-License-Identifier: Apache-2.0

import { ClipboardList } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { ApprovalRequest } from "./approval-panel";
import { AskArgsView } from "./ask-args-view";
import { useActiveEscapeAsk } from "./escape-hint-context";

export type PlanVerdict = "approve" | "accept_edits" | "iterate";

/**
 * Present only scrubbed event arguments. The tool name chooses this view, not verdict authority.
 *
 * The prototype's plan-review card: brand-toned, with the plan in the same
 * argument box and "Raw arguments" toggle as an ordinary ask. Verdicts still go
 * only through Studio's exact plan-ask control (`plan-asks/{askId}`, gated on
 * `exact_plan_ask_control`); the surface passes `unavailableReason` when that
 * control can't answer this exact ask.
 */
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
  const escapeHint = useActiveEscapeAsk(approval) && !disabled && !unavailableReason;
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
      className="my-3 min-w-0 max-w-full rounded-xl border border-brand/30 bg-brand/5 p-4"
      aria-label="Plan review"
    >
      <div className="flex items-center gap-2">
        <ClipboardList aria-hidden="true" className="size-4 shrink-0 text-brand-ink" />
        <h2 className="text-sm font-semibold text-brand-ink">Plan review</h2>
      </div>
      {approval.reason && <p className="mt-2 text-sm">{approval.reason}</p>}
      {note && <p className="mt-2 whitespace-pre-wrap break-words text-sm">{note}</p>}
      <AskArgsView
        args={approval.args}
        className="mt-3"
        formatted={
          plan ? (
            <pre className="whitespace-pre-wrap break-words p-3 text-sm">{plan}</pre>
          ) : (
            <p className="p-3 text-sm">The plan text is unavailable or malformed.</p>
          )
        }
        rawLimit={65_536}
        tone="brand"
        tool={approval.tool}
      />
      {uncertain && (
        <p className="mt-3 text-sm" role="status">
          This verdict's outcome is uncertain. Refresh activity before deciding again.
        </p>
      )}
      {unavailableReason ? (
        <p className="mt-3 text-sm" role="status">
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
            className="text-muted-foreground"
            disabled={disabled}
            onClick={() => onRespond("iterate")}
            size="sm"
            variant="ghost"
          >
            Iterate
          </Button>
        </div>
      )}
      {escapeHint && <p className="mt-2 text-xs text-muted-foreground">Esc to Iterate</p>}
    </section>
  );
}

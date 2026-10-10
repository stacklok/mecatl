// SPDX-License-Identifier: Apache-2.0

import { useRef } from "react";
import type { ApprovalRequest, ApprovalVerdict } from "./approval-panel";
import { ApprovalVerdictBar } from "./approval-verdict-bar";
import { AskArgsView } from "./ask-args-view";
import { isDestructiveToolName } from "./destructive-tool";
import { useActiveEscapeAsk } from "./escape-hint-context";

/** The detail panel's live view of one ask; the surface re-resolves it from its ledger. */
export interface ApprovalDetail {
  approval: ApprovalRequest;
  disabled: boolean;
  onRespond: (verdict: ApprovalVerdict) => void;
  position: number;
  total: number;
  uncertain: boolean;
}

/**
 * The body of the approval detail panel, ported from the prototype's
 * `approval-detail-panel.tsx`: the same ask as its card, with the full
 * arguments and diff instead of the card's capped view. The side-panel frame
 * (`SidePanelShell`) owns the title, close, and resize. Like the card, it holds
 * no verdict state: the verdict ledger decides `disabled` and `uncertain`.
 */
export function ApprovalDetailPanel({
  approval,
  disabled,
  onRespond,
  uncertain,
}: Omit<ApprovalDetail, "position" | "total">) {
  const body = useRef<HTMLDivElement>(null);
  const destructive = isDestructiveToolName(approval.tool);
  const active = useActiveEscapeAsk(approval) && !disabled && !uncertain;

  return (
    <div className="px-4 py-3" ref={body}>
      {approval.reason && <p className="text-sm">{approval.reason}</p>}
      {destructive && (
        <p
          className={approval.reason ? "mt-2 text-xs text-destructive" : "text-xs text-destructive"}
        >
          This action modifies or deletes data.
        </p>
      )}
      <AskArgsView args={approval.args} bounded={false} className="mt-3" tool={approval.tool} />
      {uncertain && (
        <p className="mt-3 text-sm" role="status">
          This verdict's outcome is uncertain. Refresh activity before deciding again.
        </p>
      )}
      <div className="mt-4">
        <ApprovalVerdictBar
          allowAvailable={approval.args.trim().length > 0}
          destructive={destructive}
          disabled={disabled}
          onRespond={onRespond}
          scope={body}
          shortcuts={active}
        />
      </div>
      {active && <p className="mt-2 text-xs text-muted-foreground">Esc to Deny</p>}
    </div>
  );
}

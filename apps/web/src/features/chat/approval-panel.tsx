// SPDX-License-Identifier: Apache-2.0

import { Fullscreen, ShieldAlert } from "lucide-react";
import { useRef } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useOpenApprovalDetail } from "./approval-detail-context";
import { ApprovalVerdictBar } from "./approval-verdict-bar";
import { AskArgsView } from "./ask-args-view";
import { isDestructiveToolName } from "./destructive-tool";
import { useActiveEscapeAsk } from "./escape-hint-context";

export interface ApprovalRequest {
  args: string;
  askId: string;
  /** Presentation correlation only; never a verdict target. */
  callId?: string;
  /** Immutable identity from the permission.ask delivery, when its run is known. */
  controlTarget?: { askId: string; runId: string; sessionId: string };
  reason: string;
  tool: string;
}

export type ApprovalVerdict = "allow_once" | "allow_always" | "deny";

/**
 * One pending `permission.ask`, inline in the transcript, in the prototype's
 * card: warning-toned, or destructive-toned when `isDestructiveToolName` reads
 * the tool as data-destroying. The card never holds verdict state: `disabled`
 * and `uncertain` come from the surface's verdict ledger, and every verdict,
 * clicked or typed, goes through `onRespond`, which checks the ledger again.
 */
export function ApprovalPanel({
  approval,
  disabled,
  onRespond,
  position = 1,
  uncertain = false,
  total = 1,
}: {
  approval: ApprovalRequest;
  disabled: boolean;
  onRespond: (verdict: ApprovalVerdict) => void;
  position?: number;
  uncertain?: boolean;
  total?: number;
}) {
  const card = useRef<HTMLElement>(null);
  const destructive = isDestructiveToolName(approval.tool);
  const active = useActiveEscapeAsk(approval) && !disabled && !uncertain;
  const openDetail = useOpenApprovalDetail();
  const hasArgs = approval.args.trim().length > 0;

  return (
    <section
      aria-label={`Permission required: ${approval.tool || "Tool"}`}
      className={cn(
        "my-3 min-w-0 rounded-xl border p-4",
        destructive ? "border-destructive/40 bg-destructive/5" : "border-warning/40 bg-warning/5",
      )}
      ref={card}
    >
      <div className="flex min-w-0 items-start gap-2">
        {/* Wraps instead of truncating: the tool name is what the reader approves. */}
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
          <ShieldAlert
            aria-hidden="true"
            className={cn("size-4 shrink-0", destructive ? "text-destructive" : "text-warning")}
          />
          <span
            className={cn(
              "text-sm font-semibold",
              destructive ? "text-destructive" : "text-warning",
            )}
          >
            Permission required
          </span>
          {total > 1 && (
            <Badge className="tabular-nums" variant="outline">
              {position} of {total}
            </Badge>
          )}
          <Badge className="max-w-full font-mono" variant={destructive ? "destructive" : "warning"}>
            <span className="truncate">{approval.tool || "Tool"}</span>
          </Badge>
        </div>
        {openDetail && (
          <Button
            aria-label="Open in detail panel"
            className="-my-1 size-7 shrink-0 text-muted-foreground"
            onClick={() => openDetail(approval)}
            size="icon"
            title="Open in detail panel"
            type="button"
            variant="ghost"
          >
            <Fullscreen aria-hidden="true" className="size-3.5" />
          </Button>
        )}
      </div>
      {approval.reason && <p className="mt-2 text-sm">{approval.reason}</p>}
      {destructive && (
        <p className="mt-2 text-xs text-destructive">This action modifies or deletes data.</p>
      )}
      <AskArgsView args={approval.args} className="mt-3" tool={approval.tool} />
      {uncertain && (
        <p className="mt-3 text-sm" role="status">
          This verdict's outcome is uncertain. Refresh activity before deciding again.
        </p>
      )}
      <div className="mt-3">
        <ApprovalVerdictBar
          allowAvailable={hasArgs}
          destructive={destructive}
          disabled={disabled}
          onRespond={onRespond}
          scope={card}
          shortcuts={active}
        />
      </div>
      {active && <p className="mt-2 text-xs text-muted-foreground">Esc to Deny</p>}
    </section>
  );
}

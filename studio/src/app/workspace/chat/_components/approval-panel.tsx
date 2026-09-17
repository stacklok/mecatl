"use client";

import { Fullscreen, ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import {
  DEBUG_MCP_ASK_NOTE,
  isDebugMcpMutationAsk,
} from "@/features/agent/approval-queue";
import { isPlanAsk } from "@/features/agent/plan-ask";
import { toolDisplayName } from "@/lib/tool-names";
import { cn } from "@/lib/utils";
import { ApprovalVerdictBar } from "./approval-verdict-bar";
import { AskArgsView } from "./ask-args-view";
import { PlanReviewCard } from "./plan-review-panel";

const DELETE_WORDS = /\b(delete|remove|drop|revoke|destroy|purge|rm)\b/i;

/**
 * The daemon's verdict is three-way (allow_once / allow_always / deny), so the
 * panel offers exactly those scopes — no "for session" button, which would
 * promise a grant scope the backend does not model.
 */
export function ApprovalPanel({
  approval,
  onRespond,
  queuePosition,
  onExpand,
  debugSession = false,
}: {
  approval: ApprovalRequest;
  onRespond: (choice: ApprovalChoice) => void;
  /** This ask's place in the FIFO queue (1-based). The "1 of N" badge shows
   *  only while more than one ask is waiting. */
  queuePosition?: { index: number; total: number };
  /** Opens the ask's full-height view (the side panel); omitted = the card
   *  offers no Expand button (the thread panel keeps it inline). */
  onExpand?: (approval: ApprovalRequest) => void;
  /** True on an AI-debug session (ADR 0254): a debugger MCP ask then offers
   *  no Always allow (the daemon never learns it) and says so. */
  debugSession?: boolean;
}) {
  // A PresentPlan ask is a plan review, not a tool authorization: its own
  // surface (rendered plan, approve & run / auto-accept edits / iterate).
  if (isPlanAsk(approval.toolName)) {
    return (
      <PlanReviewCard
        approval={approval}
        onRespond={onRespond}
        queuePosition={queuePosition}
        onExpand={onExpand}
      />
    );
  }
  // ONE ask = ONE tool call. The pill names the tool; the args belong in the
  // preview block below, never badge-ified (a Write ask's args are a whole
  // file). Destructiveness is judged on the tool name alone — scanning file
  // CONTENT for the word "delete" painted harmless writes red.
  const toolName =
    approval.toolName ||
    approval.description.replace(/ needs your approval\.?$/i, "").trim() ||
    "Tool";
  // The badge shows the TUI's humanized `Server · Tool` for an MCP tool
  // (exact id on hover); destructiveness is judged on BOTH forms, because
  // `\b` never fires inside `mcp__github__delete_branch`.
  const displayName = toolDisplayName(toolName);
  const destructive =
    DELETE_WORDS.test(toolName) || DELETE_WORDS.test(displayName);
  // A child's ask names who is asking. It offers no "Always allow": a
  // persistent grant learned from a throwaway child would outlive it (the
  // TUI withholds AllowAlways for child asks for the same reason).
  const child = approval.child === true;
  // A debugger MCP call (ADR 0254) is approved one call at a time: the
  // daemon never learns Always allow for it, so the button is withheld and
  // the card says why instead of offering a grant that would not persist.
  const debugMcp = isDebugMcpMutationAsk(approval, debugSession);
  const offerAlways = !child && !debugMcp;
  const description = child
    ? `A subagent's ${toolName} needs your approval.`
    : approval.description;
  const queued =
    queuePosition && queuePosition.total > 1 ? queuePosition : null;

  return (
    <div
      className={cn(
        "my-3 rounded-xl border p-4",
        destructive
          ? "border-destructive/40 bg-destructive/5"
          : "border-warning/30 bg-warning/5",
      )}
    >
      <div className="mb-2 flex items-center gap-2">
        <ShieldAlert
          className={cn(
            "size-4",
            destructive ? "text-destructive" : "text-warning",
          )}
        />
        <span
          className={cn(
            "text-sm font-semibold",
            destructive ? "text-destructive" : "text-warning",
          )}
        >
          Permission required
        </span>
        {queued && (
          <Badge
            variant="outline"
            aria-label={`Request ${queued.index} of ${queued.total}`}
            className="tabular-nums text-xs"
          >
            {`${queued.index} of ${queued.total}`}
          </Badge>
        )}
        {onExpand && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="ml-auto size-7 text-muted-foreground hover:text-foreground"
            aria-label="Expand permission details"
            title="Expand permission details"
            onClick={() => onExpand(approval)}
          >
            <Fullscreen className="size-4" />
          </Button>
        )}
      </div>
      <p className="mb-2 text-sm">{description}</p>
      <div className="mb-2 flex flex-wrap gap-1.5">
        <Badge
          variant="secondary"
          className={cn(
            "gap-1 border-transparent font-mono text-xs",
            destructive
              ? "bg-destructive/15 text-destructive"
              : "bg-warning/15 text-warning",
          )}
          title={toolName}
        >
          {displayName}
        </Badge>
        {child && (
          <Badge variant="outline" className="text-xs">
            Subagent
          </Badge>
        )}
        {debugMcp && (
          <Badge variant="outline" className="text-xs">
            Debugger MCP
          </Badge>
        )}
      </div>
      {destructive && (
        <p className="mb-3 text-xs font-medium text-destructive">
          This action modifies or deletes data.
        </p>
      )}
      {debugMcp && (
        <p
          role="note"
          data-testid="debug-mcp-ask-note"
          className="mb-3 text-xs text-muted-foreground"
        >
          {DEBUG_MCP_ASK_NOTE}
        </p>
      )}
      <AskArgsView
        approval={approval}
        destructive={destructive}
        className="mb-4"
      />
      <ApprovalVerdictBar
        approval={approval}
        onRespond={onRespond}
        offerAlways={offerAlways}
        destructive={destructive}
      />
    </div>
  );
}

"use client";

import { ShieldAlert } from "lucide-react";
import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import {
  DEBUG_MCP_ASK_NOTE,
  isDebugMcpMutationAsk,
} from "@/features/agent/approval-queue";
import { isPlanAsk } from "@/features/agent/plan-ask";
import { toolDisplayName } from "@/lib/tool-names";
import { ApprovalVerdictBar } from "./approval-verdict-bar";
import { askToolName } from "./ask-args";
import { AskArgsView } from "./ask-args-view";
import { PlanReviewDetailPanel } from "./plan-review-panel";
import { SidePanel } from "./side-panel";

const DELETE_WORDS = /\b(delete|remove|drop|revoke|destroy|purge|rm)\b/i;

/**
 * The full-height view of one permission ask in the right-hand side panel
 * (the TUI's ctrl+t args view): the reason and the decoded args fill and
 * scroll the panel, with the Raw/Pretty toggle, and the verdict buttons stay
 * pinned in a bottom bar. A verdict answers the ask and closes the panel.
 */
export function ApprovalDetailPanel({
  approval,
  onRespond,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
  debugSession = false,
}: {
  approval: ApprovalRequest;
  onRespond: (choice: ApprovalChoice) => void;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
  /** True on an AI-debug session (ADR 0254): a debugger MCP ask then offers
   *  no Always allow (the daemon never learns it) and says so. */
  debugSession?: boolean;
}) {
  // A PresentPlan ask opens as the full-height plan review instead.
  if (isPlanAsk(approval.toolName)) {
    return (
      <PlanReviewDetailPanel
        approval={approval}
        onRespond={onRespond}
        onClose={onClose}
        maximized={maximized}
        onToggleMaximize={onToggleMaximize}
        windowControls={windowControls}
      />
    );
  }
  const toolName = askToolName(approval);
  // The header shows the humanized `Server · Tool` for an MCP tool;
  // destructiveness is judged on both forms (the inline card does the same).
  const displayName = toolDisplayName(toolName);
  const destructive =
    DELETE_WORDS.test(toolName) || DELETE_WORDS.test(displayName);
  const child = approval.child === true;
  // A debugger MCP call (ADR 0254): approved one call at a time, Always
  // allow never learned by the daemon — withheld here as on the inline card.
  const debugMcp = isDebugMcpMutationAsk(approval, debugSession);
  const offerAlways = !child && !debugMcp;
  const respond = (choice: ApprovalChoice) => {
    onRespond(choice);
    onClose();
  };
  return (
    <SidePanel
      icon={ShieldAlert}
      title={`${displayName} — permission ask`}
      closeLabel="Close permission details"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1 flex-col px-4 py-3">
          <p className="mb-2 text-sm text-muted-foreground">
            {child
              ? `A subagent's ${toolName} needs your approval.`
              : approval.description}
          </p>
          {debugMcp && (
            <p
              role="note"
              data-testid="debug-mcp-ask-note"
              className="mb-2 text-xs text-muted-foreground"
            >
              {DEBUG_MCP_ASK_NOTE}
            </p>
          )}
          <AskArgsView
            approval={approval}
            layout="full"
            destructive={destructive}
            className="min-h-0 flex-1"
          />
        </div>
        <ApprovalVerdictBar
          approval={approval}
          onRespond={respond}
          offerAlways={offerAlways}
          destructive={destructive}
          className="border-t border-border px-4 py-3"
        />
      </div>
    </SidePanel>
  );
}

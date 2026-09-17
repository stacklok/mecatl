"use client";

import { ClipboardList, Fullscreen } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import { planBodyFromArgs } from "@/features/agent/plan-ask";
import { cn } from "@/lib/utils";
import { askArgsSource, clampAskText } from "./ask-args";
import { mdComponents } from "./markdown-components";
import { SidePanel } from "./side-panel";

/**
 * What each verdict DOES for a plan. The wire mapping is the ordinary one
 * (once → allow_once, always → allow_always, deny → deny); the engine keys
 * the plan outcome on that bare verdict (dispatch.go `surfacePlanAsk`), so
 * the labels name the outcome the operator is choosing, not a grant scope.
 * A plan ask is always the main agent's, so all three are always offered.
 */
export const PLAN_VERDICTS: ReadonlyArray<{
  choice: ApprovalChoice;
  label: string;
  description: string;
}> = [
  {
    choice: "once",
    label: "Approve & run",
    description: "Switch to Manual mode and start executing",
  },
  {
    choice: "always",
    label: "Auto-accept edits",
    description: "Approve and switch to Accept edits for the execution run",
  },
  {
    choice: "deny",
    label: "Iterate",
    description: "Send the plan back for another pass",
  },
];

/** The three plan verdict buttons, in one row, plus the outcome hints. */
function PlanVerdictBar({
  onRespond,
  className,
}: {
  onRespond: (choice: ApprovalChoice) => void;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex flex-wrap gap-2">
        {PLAN_VERDICTS.map((verdict, index) => (
          <Button
            key={verdict.choice}
            size="sm"
            variant={
              index === 0 ? "default" : index === 1 ? "outline" : "ghost"
            }
            title={verdict.description}
            onClick={() => onRespond(verdict.choice)}
            className={cn(
              index === 0 && "bg-brand text-white hover:bg-brand/90",
              index === 1 && "border-brand/30 hover:bg-brand/5",
              index === 2 && "text-muted-foreground hover:text-foreground",
            )}
          >
            {verdict.label}
          </Button>
        ))}
      </div>
      <p className="text-xs text-muted-foreground">
        {PLAN_VERDICTS.map((verdict, index) => (
          <span key={verdict.choice}>
            {index > 0 && " · "}
            <span className="font-medium text-foreground/80">
              {verdict.label}
            </span>
            {`: ${verdict.description.charAt(0).toLowerCase()}${verdict.description.slice(1)}`}
          </span>
        ))}
      </p>
    </div>
  );
}

/**
 * The plan text, rendered as markdown inside a scroll region. A plan ask
 * whose args carry no readable `plan`/`note` (an ask translated without the
 * raw tier, or malformed args) shows whatever text it has verbatim so the
 * operator never approves an empty box unseen.
 */
function PlanBody({
  approval,
  className,
}: {
  approval: Pick<ApprovalRequest, "reason" | "args" | "details">;
  className?: string;
}) {
  const { args } = askArgsSource(approval);
  const body = planBodyFromArgs(args);
  return (
    <section
      className={cn(
        "rounded-lg border border-brand/20 bg-background",
        className,
      )}
      aria-label="Plan"
    >
      {body ? (
        <div className="px-4 py-3 text-sm leading-[1.75] text-foreground/90">
          <ReactMarkdown remarkPlugins={[remarkGfm]} components={mdComponents}>
            {clampAskText(body)}
          </ReactMarkdown>
        </div>
      ) : (
        <pre className="overflow-x-auto whitespace-pre-wrap break-words px-3 py-2.5 font-mono text-xs leading-relaxed">
          {clampAskText(args) || "(the plan ask carried no plan text)"}
        </pre>
      )}
    </section>
  );
}

/**
 * The in-transcript card for a PresentPlan ask (the TUI's plan review
 * layout): a "Plan review" header in the brand tint — not the warning
 * shield, nothing is being authorized — the daemon's reason line, the plan
 * rendered as markdown in a bounded scroller, and the three plan verdicts
 * pinned below it.
 */
export function PlanReviewCard({
  approval,
  onRespond,
  queuePosition,
  onExpand,
}: {
  approval: ApprovalRequest;
  onRespond: (choice: ApprovalChoice) => void;
  queuePosition?: { index: number; total: number };
  onExpand?: (approval: ApprovalRequest) => void;
}) {
  const { reason } = askArgsSource(approval);
  const queued =
    queuePosition && queuePosition.total > 1 ? queuePosition : null;
  return (
    <div className="my-3 rounded-xl border border-brand/30 bg-brand/5 p-4">
      <div className="mb-2 flex items-center gap-2">
        <ClipboardList className="size-4 text-brand" />
        <span className="text-sm font-semibold text-brand">Plan review</span>
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
            aria-label="Expand plan review"
            title="Expand plan review"
            onClick={() => onExpand(approval)}
          >
            <Fullscreen className="size-4" />
          </Button>
        )}
      </div>
      <p className="mb-2 text-sm">
        {reason || "The agent presented a plan and is waiting for your review."}
      </p>
      <PlanBody
        approval={approval}
        className="mb-4 max-h-[60vh] overflow-y-auto"
      />
      <PlanVerdictBar onRespond={onRespond} />
    </div>
  );
}

/**
 * The full-height plan review in the right-hand side panel: the plan fills
 * and scrolls the panel, the verdicts stay pinned in a bottom bar. A verdict
 * answers the ask and closes the panel.
 */
export function PlanReviewDetailPanel({
  approval,
  onRespond,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  approval: ApprovalRequest;
  onRespond: (choice: ApprovalChoice) => void;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  const { reason } = askArgsSource(approval);
  const respond = (choice: ApprovalChoice) => {
    onRespond(choice);
    onClose();
  };
  return (
    <SidePanel
      icon={ClipboardList}
      title="Plan review"
      closeLabel="Close plan review"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex min-h-0 flex-1 flex-col px-4 py-3">
          <p className="mb-2 text-sm text-muted-foreground">
            {reason ||
              "The agent presented a plan and is waiting for your review."}
          </p>
          <PlanBody
            approval={approval}
            className="min-h-0 flex-1 overflow-y-auto"
          />
        </div>
        <PlanVerdictBar
          onRespond={respond}
          className="border-t border-border px-4 py-3"
        />
      </div>
    </SidePanel>
  );
}

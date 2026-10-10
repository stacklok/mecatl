// SPDX-License-Identifier: Apache-2.0

import { ChevronRight, Plug, Server } from "lucide-react";
import { cn } from "@/lib/utils";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import { type AuthorizationHandoff, AuthorizationReviewTrigger } from "./authorization-review";
import { approvalMatchesToolCall } from "./chat-state";
import { ToolDiff } from "./edit-diff";
import { PlanReviewCard, type PlanVerdict } from "./plan-review-card";
import type { ToolActivity } from "./tool-activity";
import { friendlyToolName, summarizeArgs, summarizeResult } from "./tool-summary";
import { useDetailsOpen } from "./use-details-open";

/** Per-call status: "running" while `output` is undefined, else success or failure. */
export type ToolStatus = "running" | "failed" | "completed";

export function toolStatus(tool: ToolActivity): ToolStatus {
  if (tool.output === undefined) return "running";
  return tool.isError ? "failed" : "completed";
}

/** The status dot's colour: the quiet-dot idiom the schedule badges use. */
export function statusDotClass(status: ToolStatus): string {
  switch (status) {
    case "failed":
      return "bg-destructive";
    case "running":
      return "animate-pulse bg-brand";
    default:
      return "bg-muted-foreground/50";
  }
}

/** Colour carries the state; the word stays for hover and screen readers. */
function StatusDot({ status }: { status: ToolStatus }) {
  return (
    <span className="inline-flex shrink-0 items-center" title={status}>
      <span aria-hidden="true" className={cn("size-1.5 rounded-full", statusDotClass(status))} />
      <span className="sr-only">{status}</span>
    </span>
  );
}

/**
 * The collapsed summary's text: "N tools", plus "M failed" only when a call
 * failed (never "0 failed"), split so the failed part can take its own tint.
 */
export function activitySummary(tools: ToolActivity[]): { failed: string | null; tools: string } {
  const failed = tools.filter((tool) => toolStatus(tool) === "failed").length;
  return {
    failed: failed > 0 ? `${failed} failed` : null,
    tools: `${tools.length} tool${tools.length === 1 ? "" : "s"}`,
  };
}

interface ToolCallControls {
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  onPreview?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  onRespondToPlan?: (approval: ApprovalRequest, verdict: PlanVerdict) => void;
  onReviewAuthorization?: (authorization: AuthorizationHandoff) => void;
  planUnavailableReason?: (approval: ApprovalRequest) => string | undefined;
}

/**
 * One call: the status dot, the friendly head (`Server · Tool` for an MCP
 * tool, the raw name on hover), the clamped `key: value` arguments, and the
 * one-line result. The head opens the full input and output in the side
 * panel. A finished Edit or Write shows its diff underneath; a pending one's
 * diff is the ask card's. An authorization or an ask for this call sits in
 * the same row, so its tool context stays beside it.
 */
function ToolCallRow({
  approvals,
  authorizations,
  controls,
  tool,
}: {
  approvals: ApprovalRequest[];
  authorizations: AuthorizationHandoff[];
  controls: ToolCallControls;
  tool: ToolActivity;
}) {
  const status = toolStatus(tool);
  const head = friendlyToolName(tool.name);
  const args = summarizeArgs(tool.args);
  const result = summarizeResult(tool.output);
  const Glyph = head.mcp ? Server : Plug;
  const content = (
    <>
      <StatusDot status={status} />
      <Glyph aria-hidden="true" className="size-3 shrink-0" />
      <span className="sr-only">Tool: {tool.name}</span>
      <span
        aria-hidden="true"
        className="shrink-0 font-medium text-foreground/70"
        title={head.mcp ? head.raw : undefined}
      >
        {head.display}
      </span>
      {args && (
        <span
          className="min-w-0 max-w-[45%] truncate text-muted-foreground/80"
          data-testid="tool-args-summary"
          title={args}
        >
          {args}
        </span>
      )}
      {result && (
        <span
          className={cn("min-w-0 truncate", tool.isError && "text-destructive/80")}
          data-testid="tool-result-summary"
          title={result}
        >
          {result}
        </span>
      )}
    </>
  );
  return (
    <li className="min-w-0">
      {controls.onPreview ? (
        <button
          aria-label={`Open ${head.display} details`}
          className="-mx-1 flex w-full min-w-0 items-center gap-2 rounded-md px-1 py-0.5 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-brand"
          onClick={() => controls.onPreview?.(tool)}
          type="button"
        >
          {content}
        </button>
      ) : (
        <div className="flex min-w-0 items-center gap-2 py-0.5 text-xs text-muted-foreground">
          {content}
        </div>
      )}
      {status === "completed" && (
        <ToolDiff className="ml-5 mt-1" name={tool.name} rawArgs={tool.args} />
      )}
      {authorizations.map((authorization) => (
        <AuthorizationReviewTrigger
          authorization={authorization}
          key={authorization.authorizationId}
          onReview={controls.onReviewAuthorization ?? (() => undefined)}
        />
      ))}
      {approvals.map((approval) =>
        approval.tool === "PresentPlan" ? (
          <PlanReviewCard
            approval={approval}
            disabled={!approval.controlTarget || (controls.approvalDisabled?.(approval) ?? false)}
            key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
            onRespond={(verdict) => controls.onRespondToPlan?.(approval, verdict)}
            uncertain={controls.approvalUncertain?.(approval)}
            unavailableReason={controls.planUnavailableReason?.(approval)}
          />
        ) : (
          <ApprovalPanel
            approval={approval}
            disabled={!approval.controlTarget || (controls.approvalDisabled?.(approval) ?? false)}
            key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
            onRespond={(verdict) => controls.onRespondToApproval?.(approval, verdict)}
            uncertain={controls.approvalUncertain?.(approval)}
          />
        ),
      )}
    </li>
  );
}

/**
 * A turn's tool calls behind one disclosure, ported from the prototype: a
 * toggle row with the count, any failures, and the tools involved, then one
 * row per call. It starts open or closed per the "Expand details"
 * preference, and the reader can toggle this one. It is held open while a
 * call carries an ask or an authorization, so a decision is never folded
 * away. A closed list keeps its rows in the document, hidden, so the calls
 * stay findable by name and in the page's text.
 */
export function ToolCallList({
  approvals = [],
  authorizations = [],
  tools,
  ...controls
}: ToolCallControls & {
  /** The asks this turn owns; each renders in its call's row. */
  approvals?: ApprovalRequest[];
  /** Authorizations to show in their call's row. */
  authorizations?: AuthorizationHandoff[];
  tools: ToolActivity[];
}) {
  const [expanded, toggleExpanded] = useDetailsOpen();
  const rows = tools.map((tool) => ({
    approvals: approvals.filter((approval) => approvalMatchesToolCall(approval, tool)),
    authorizations: authorizations.filter(
      (authorization) => authorization.callId === tool.id && authorization.runId === tool.runId,
    ),
    tool,
  }));
  const held = rows.some((row) => row.approvals.length > 0 || row.authorizations.length > 0);
  const open = expanded || held;
  const summary = activitySummary(tools);
  const names = tools.map((tool) => friendlyToolName(tool.name).display).join(" · ");

  return (
    <div className="my-1 min-w-0">
      <button
        aria-expanded={open}
        className="flex w-full min-w-0 items-center gap-2 rounded-md py-1 text-left focus-visible:outline-2 focus-visible:outline-brand"
        onClick={toggleExpanded}
        type="button"
      >
        <ChevronRight
          aria-hidden="true"
          className={cn(
            "size-3 shrink-0 text-muted-foreground/50 transition-transform",
            open && "rotate-90",
          )}
        />
        <span className="min-w-0 truncate text-xs text-muted-foreground">
          <span className="font-medium">Activity: {summary.tools}</span>
          {summary.failed && (
            <span className="font-medium text-destructive"> · {summary.failed}</span>
          )}{" "}
          {names}
        </span>
      </button>
      <ol className="ml-5 mt-1 min-w-0 space-y-1" hidden={!open}>
        {rows.map((row) => (
          <ToolCallRow
            approvals={row.approvals}
            authorizations={row.authorizations}
            controls={controls}
            key={`${row.tool.runId ?? ""}:${row.tool.id}`}
            tool={row.tool}
          />
        ))}
      </ol>
    </div>
  );
}

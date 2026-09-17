"use client";

import { ChevronRight, Plug, Server } from "lucide-react";
import type { ToolCallInfo } from "@/features/agent";
import {
  friendlyToolName,
  summarizeArgs,
  summarizeResult,
  toolRawArgs,
} from "@/lib/tool-summary";
import { cn } from "@/lib/utils";
import { HookChips } from "./hook-notice";
import { isDiffTool, ToolDiff } from "./tool-diff";
import { ToolResultParts } from "./tool-result-parts";
import { useDetailsOpen } from "./use-details-open";

/**
 * The collapsed summary line's text: "3 tools" plus a "1 failed" fragment
 * only when any call failed (never "0 failed"). Split so the component can
 * tint the failed fragment destructive without re-deriving the counts.
 */
export function activitySummary(toolCalls: Pick<ToolCallInfo, "status">[]): {
  tools: string;
  failed: string | null;
} {
  const count = toolCalls.length;
  const failedCount = toolCalls.filter((t) => t.status === "failed").length;
  return {
    tools: `${count} tool${count === 1 ? "" : "s"}`,
    failed: failedCount > 0 ? `${failedCount} failed` : null,
  };
}

/** Per-call status dot color: the schedule-badges quiet-dot idiom. */
export function statusDotClass(status: ToolCallInfo["status"]): string {
  switch (status) {
    case "failed":
      return "bg-destructive";
    case "running":
      return "bg-brand animate-pulse";
    default:
      return "bg-muted-foreground/50";
  }
}

/** Quiet status dot: color carries the state, label kept for hover/SRs. */
function StatusDot({ status }: { status: ToolCallInfo["status"] }) {
  return (
    <span title={status} className="inline-flex shrink-0 items-center">
      <span
        aria-hidden="true"
        className={cn("size-1.5 rounded-full", statusDotClass(status))}
      />
      <span className="sr-only">{status}</span>
    </span>
  );
}

/**
 * One expanded row: the friendly head (`server · tool` for an MCP tool, the
 * raw name on hover), the clamped `key: value` arg summary, the bounded
 * result preview; under it the result's link/image parts, and for an
 * Edit/Write the inline diff. The row itself opens the drill-down panel.
 */
function ToolCallRow({
  call,
  onSelect,
}: {
  call: ToolCallInfo;
  onSelect?: (call: ToolCallInfo) => void;
}) {
  const head = friendlyToolName(call.name);
  const rawArgs = toolRawArgs(call);
  const args = summarizeArgs(rawArgs);
  const result = summarizeResult(call.output);
  const Glyph = head.mcp ? Server : Plug;
  const row = (
    <>
      <StatusDot status={call.status} />
      <Glyph className="size-3 shrink-0" aria-hidden="true" />
      <span
        className="shrink-0 font-medium text-foreground/70"
        title={head.mcp ? head.raw : undefined}
      >
        {head.display}
      </span>
      {args && (
        <span
          data-testid="tool-args-summary"
          className="min-w-0 max-w-[45%] truncate text-muted-foreground/80"
          title={args}
        >
          {args}
        </span>
      )}
      {result && (
        <span
          data-testid="tool-result-summary"
          className={cn(
            "min-w-0 truncate",
            call.isError && "text-destructive/80",
          )}
        >
          {result}
        </span>
      )}
      <HookChips hooks={call.hooks} />
    </>
  );
  const showDiff = rawArgs !== undefined && isDiffTool(call.name);
  return (
    <div>
      {onSelect ? (
        <button
          type="button"
          onClick={() => onSelect(call)}
          aria-label={`Open ${head.display} details`}
          className="-mx-1 flex w-full items-center gap-2 rounded-md px-1 py-0.5 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/50"
        >
          {row}
        </button>
      ) : (
        <div className="flex items-center gap-2 py-0.5 text-xs text-muted-foreground">
          {row}
        </div>
      )}
      {call.parts && call.parts.length > 0 && (
        <ToolResultParts parts={call.parts} className="ml-5 py-0.5" />
      )}
      {showDiff && (
        <ToolDiff name={call.name} rawArgs={rawArgs} className="ml-5 mt-1" />
      )}
    </div>
  );
}

export function ToolCallList({
  toolCalls,
  onSelect,
}: {
  toolCalls: ToolCallInfo[];
  /** Opens one call's full input/output in the side panel; omitted = rows
      are plain text (the thread panel keeps its inline previews only). */
  onSelect?: (call: ToolCallInfo) => void;
}) {
  // Starts open or closed per the global Expand details preference, and
  // follows the `chat.expandDetails` shortcut; this line toggles just this turn.
  const [expanded, toggleExpanded] = useDetailsOpen();
  const toolNames = toolCalls
    .map((t) => friendlyToolName(t.name).display)
    .join(" · ");
  const summary = activitySummary(toolCalls);

  return (
    <div className="my-1">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={toggleExpanded}
        className="flex items-center gap-2 w-full text-left py-1"
      >
        <ChevronRight
          className={cn(
            "size-3 text-muted-foreground/50 shrink-0 transition-transform",
            expanded && "rotate-90",
          )}
        />
        <span className="text-xs text-muted-foreground">
          <span className="font-medium">Activity: {summary.tools}</span>
          {summary.failed && (
            <span className="font-medium text-destructive">
              {" "}
              · {summary.failed}
            </span>
          )}{" "}
          {toolNames}
        </span>
      </button>
      {expanded && (
        <div className="ml-5 mt-1 space-y-1">
          {toolCalls.map((tc) => (
            <ToolCallRow key={tc.callId} call={tc} onSelect={onSelect} />
          ))}
        </div>
      )}
    </div>
  );
}

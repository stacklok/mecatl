"use client";

import { ChevronRight, Plug } from "lucide-react";
import { useState } from "react";
import type { ToolCallInfo } from "@/features/agent";
import { cn } from "@/lib/utils";

function formatPreview(output: string | undefined): string {
  if (!output) return "";
  const clean = output.replace(/\n/g, " ").trim();
  return clean.length > 120 ? `${clean.slice(0, 120)}...` : clean;
}

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

export function ToolCallList({
  toolCalls,
  onSelect,
}: {
  toolCalls: ToolCallInfo[];
  /** Opens one call's full input/output in the side panel; omitted = rows
      are plain text (the thread panel keeps its inline previews only). */
  onSelect?: (call: ToolCallInfo) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const toolNames = toolCalls.map((t) => t.name).join(" · ");
  const summary = activitySummary(toolCalls);

  return (
    <div className="my-1">
      <button
        type="button"
        onClick={() => setExpanded((o) => !o)}
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
          {toolCalls.map((tc) => {
            const row = (
              <>
                <StatusDot status={tc.status} />
                <Plug className="size-3 shrink-0" />
                <span className="font-medium text-foreground/70">
                  {tc.name}
                </span>
                {tc.output && (
                  <span className="truncate">{formatPreview(tc.output)}</span>
                )}
              </>
            );
            return onSelect ? (
              <button
                key={tc.callId}
                type="button"
                onClick={() => onSelect(tc)}
                className="-mx-1 flex w-full items-center gap-2 rounded-md px-1 py-0.5 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/50"
              >
                {row}
              </button>
            ) : (
              <div
                key={tc.callId}
                className="flex items-center gap-2 text-xs text-muted-foreground py-0.5"
              >
                {row}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

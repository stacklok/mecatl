// SPDX-License-Identifier: Apache-2.0

import { type ReactNode, useState } from "react";
import { Toggle } from "@/components/ui/toggle";
import { cn } from "@/lib/utils";
import { describeAskArgs } from "./ask-args";
import { ToolDiff } from "./edit-diff";

const PRE_CLASS = "whitespace-pre-wrap break-words p-3 font-mono text-xs";

/**
 * An approval ask's `args`, ported from the prototype's `ask-args-view.tsx`:
 * an Edit or Write renders as its diff (`edit-diff.tsx`, the transcript's own
 * diff view), a command as its text, anything else as formatted JSON, and the
 * "Raw arguments" toggle shows the exact JSON the daemon sent.
 *
 * `bounded` keeps the card compact: the box scrolls and a long diff stops at
 * its first rows. The detail panel passes `bounded={false}`.
 */
export function AskArgsView({
  args,
  bounded = true,
  className,
  formatted,
  rawLimit,
  tone = "warning",
  tool,
}: {
  args: string;
  bounded?: boolean;
  className?: string;
  /** Replaces the parsed view, e.g. the plan card's plan text. */
  formatted?: ReactNode;
  /** Truncates the raw view for display. */
  rawLimit?: number;
  /** The frame's tint: the ask card's warning, or the plan card's brand. */
  tone?: "brand" | "warning";
  tool: string;
}) {
  const [showRaw, setShowRaw] = useState(false);
  if (!args.trim() && formatted === undefined) {
    return (
      <p className={cn("text-sm", className)} role="status">
        Arguments unavailable
      </p>
    );
  }
  const parsed = describeAskArgs(tool, args);
  const truncated = rawLimit !== undefined && args.length > rawLimit;

  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <div className="flex items-center justify-end">
        <Toggle
          className="h-6 min-w-0 px-2 text-xs font-normal text-muted-foreground hover:bg-accent hover:text-foreground"
          onPressedChange={setShowRaw}
          pressed={showRaw}
          size="sm"
        >
          Raw arguments
        </Toggle>
      </div>
      <div
        className={cn(
          "min-w-0 overflow-y-auto rounded-lg border bg-background",
          tone === "brand" ? "border-brand/20" : "border-warning/20",
          bounded && "max-h-56",
        )}
      >
        {showRaw ? (
          <>
            <pre className={cn(PRE_CLASS, "break-all")}>
              {!args ? "Arguments unavailable" : truncated ? args.slice(0, rawLimit) : args}
            </pre>
            {truncated && (
              <p className="border-t px-3 py-1.5 text-xs text-muted-foreground">
                Arguments truncated for display.
              </p>
            )}
          </>
        ) : formatted !== undefined ? (
          formatted
        ) : parsed.kind === "edit" || parsed.kind === "write" ? (
          <ToolDiff
            bounded={bounded}
            className="rounded-none border-0"
            name={tool}
            rawArgs={args}
          />
        ) : parsed.kind === "shell" ? (
          <pre className={PRE_CLASS}>{parsed.command}</pre>
        ) : parsed.kind === "json" ? (
          <pre className={PRE_CLASS}>{parsed.pretty}</pre>
        ) : (
          <pre className={cn(PRE_CLASS, "break-all")}>{parsed.text}</pre>
        )}
      </div>
    </div>
  );
}

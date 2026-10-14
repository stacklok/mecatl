// SPDX-License-Identifier: Apache-2.0

import { type ReactNode, useId, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Toggle } from "@/components/ui/toggle";
import { useIsTruncated } from "@/hooks/use-is-truncated";
import { cn } from "@/lib/utils";
import { describeAskArgs } from "./ask-args";
import { ToolDiff } from "./edit-diff";
import { argsHaveInvisibleCharacters, VisibleText } from "./invisible-characters";

const PRE_CLASS = "whitespace-pre-wrap break-words p-3 font-mono text-xs";

/** Command lines the card shows before its "N more lines" toggle: what its box holds. */
export const COMMAND_LINE_CAP = 12;

function linesNote(count: number): string {
  return `${count} line${count === 1 ? "" : "s"}`;
}

/**
 * A Shell call's command. On the card (`bounded`) it shows the first
 * `COMMAND_LINE_CAP` lines in the compact box. A longer command, or one whose
 * wrapped lines overflow the box, adds its line count and a toggle that shows
 * the whole command, as the diff view's "Show all N lines" does.
 */
function CommandText({ bounded, command }: { bounded: boolean; command: string }) {
  const [expanded, setExpanded] = useState(false);
  const [pre, setPre] = useState<HTMLPreElement | null>(null);
  const bodyId = useId();
  const lines = command.split("\n");
  const long = lines.length > COMMAND_LINE_CAP;
  const collapsed = bounded && !expanded;
  const hidden = collapsed && long ? lines.length - COMMAND_LINE_CAP : 0;
  // Measured only while collapsed, so collapsing again keeps the toggle in place.
  const overflowing = useIsTruncated(collapsed ? pre : null);

  let toggleLabel = "Show full command";
  if (expanded) toggleLabel = long ? `Show first ${COMMAND_LINE_CAP} lines` : "Show less";
  else if (hidden > 0) toggleLabel = `Show ${hidden} more line${hidden === 1 ? "" : "s"}`;

  return (
    <>
      <pre
        className={cn(PRE_CLASS, collapsed && "max-h-56 overflow-y-auto")}
        id={bodyId}
        ref={setPre}
      >
        <VisibleText text={hidden > 0 ? lines.slice(0, COMMAND_LINE_CAP).join("\n") : command} />
      </pre>
      {bounded && (long || overflowing || expanded) && (
        <div className="flex flex-wrap items-center gap-x-1 border-t border-border/60 px-2 py-1">
          <span className="px-1 text-xs text-muted-foreground">{linesNote(lines.length)}</span>
          <Button
            aria-controls={bodyId}
            aria-expanded={expanded}
            className="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
            onClick={() => setExpanded((value) => !value)}
            size="sm"
            type="button"
            variant="ghost"
          >
            {toggleLabel}
          </Button>
        </div>
      )}
    </>
  );
}

/**
 * An approval ask's `args`, ported from the prototype's `ask-args-view.tsx`:
 * an Edit or Write renders as its diff (`edit-diff.tsx`, the transcript's own
 * diff view), a command as its text, anything else as formatted JSON, and the
 * "Raw arguments" toggle shows the exact JSON the daemon sent. The command,
 * JSON, and raw views show each invisible character as a badge, and a chip
 * notes when the arguments carry any (`invisible-characters.tsx`).
 *
 * `bounded` keeps the card compact: the box scrolls, a long diff stops at its
 * first rows, and a long command at its first lines until the reader expands
 * it. The detail panel passes `bounded={false}`.
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
  // The command view caps its own text, so its line count stays in view.
  const commandView = !showRaw && formatted === undefined && parsed.kind === "shell";

  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <div className="flex flex-wrap items-center justify-end gap-1.5">
        {argsHaveInvisibleCharacters(args) && (
          <Badge className="mr-auto" variant="warning">
            Contains invisible characters
          </Badge>
        )}
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
          bounded && !commandView && "max-h-56",
        )}
      >
        {showRaw ? (
          <>
            <pre className={cn(PRE_CLASS, "break-all")}>
              {!args ? (
                "Arguments unavailable"
              ) : (
                <VisibleText text={truncated ? args.slice(0, rawLimit) : args} />
              )}
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
          <>
            <CommandText bounded={bounded} command={parsed.command} />
            {(parsed.timeoutMs !== undefined ||
              parsed.background ||
              parsed.tempScope !== undefined) && (
              <dl className="flex flex-wrap gap-x-4 gap-y-1 border-t px-3 py-1.5 text-xs">
                {parsed.timeoutMs !== undefined && (
                  <div className="flex gap-1">
                    <dt className="text-muted-foreground">Timeout</dt>
                    <dd className="font-mono">{parsed.timeoutMs} ms</dd>
                  </div>
                )}
                {parsed.background && (
                  <div className="flex gap-1">
                    <dt className="text-muted-foreground">Background</dt>
                    <dd className="font-mono">true</dd>
                  </div>
                )}
                {parsed.tempScope !== undefined && (
                  <div className="flex gap-1">
                    <dt className="text-muted-foreground">Temporary storage</dt>
                    <dd className="font-mono">
                      <VisibleText text={parsed.tempScope} />
                    </dd>
                  </div>
                )}
              </dl>
            )}
          </>
        ) : parsed.kind === "json" ? (
          <pre className={PRE_CLASS}>
            <VisibleText text={parsed.pretty} />
          </pre>
        ) : (
          <pre className={cn(PRE_CLASS, "break-all")}>
            <VisibleText text={parsed.text} />
          </pre>
        )}
      </div>
    </div>
  );
}

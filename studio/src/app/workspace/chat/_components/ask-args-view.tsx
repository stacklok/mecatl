"use client";

import { useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import type { ApprovalRequest } from "@/features/agent";
import { cn } from "@/lib/utils";
import { ArgsDiff } from "./args-diff";
import {
  type AskArgs,
  askArgsSource,
  askToolName,
  clampAskText,
  describeAskArgs,
  formatTimeout,
} from "./ask-args";
import { computeLineDiff, editSizeNote, writeSizeNote } from "./tool-diff";

/** The heading over the decoded block, per kind. */
function kindLabel(described: AskArgs): string {
  switch (described.kind) {
    case "shell":
      return "Command";
    case "edit":
      return "Edit";
    case "write":
      return "Write";
    case "json":
    case "raw":
      return "Arguments";
  }
}

/** The text the decoded view shows, for the "does raw differ?" check. */
function renderedText(described: AskArgs): string | null {
  switch (described.kind) {
    case "shell":
      return described.command;
    case "json":
      return described.pretty;
    case "raw":
      return described.text;
    default:
      // A diff or a path+content pair is never byte-equal to the JSON.
      return null;
  }
}

const PRE_CLASS =
  "overflow-x-auto whitespace-pre-wrap break-words px-3 py-2.5 font-mono text-xs leading-relaxed";

function PathLine({ path, note }: { path: string; note?: string }) {
  return (
    <p className="border-b border-border/60 px-3 py-1.5 font-mono text-xs">
      <span className="text-foreground">{path}</span>
      {note && <span className="text-muted-foreground"> · {note}</span>}
    </p>
  );
}

/**
 * The reason + args of one permission ask, decoded per tool: a Shell ask
 * shows the bare command, an Edit ask a before/after diff, a Write ask the
 * path and content, anything else indented JSON. A Raw/Pretty toggle (shown
 * only when the verbatim string differs from the rendering) swaps in the
 * exact `args` string the daemon sent — what is actually being approved.
 *
 * `layout="card"` bounds the block to the in-card scroll region; `"full"`
 * lets it fill and scroll inside a full-height parent.
 */
export function AskArgsView({
  approval,
  layout = "card",
  destructive = false,
  className,
}: {
  approval: Pick<
    ApprovalRequest,
    "toolName" | "description" | "reason" | "args" | "details"
  >;
  layout?: "card" | "full";
  /** Tints the block's border to match a destructive card. */
  destructive?: boolean;
  className?: string;
}) {
  const [raw, setRaw] = useState(false);
  const { reason, args } = askArgsSource(approval);
  const toolName = askToolName(approval);
  const described = useMemo(
    () => describeAskArgs(toolName, args),
    [toolName, args],
  );
  const rendered = renderedText(described);
  const rawDiffers = described.kind !== "raw" && rendered !== args;
  const showRaw = raw && rawDiffers;

  return (
    <div className={cn("flex min-h-0 flex-col", className)}>
      {reason && <p className="mb-2 text-sm">{reason}</p>}
      <div className="mb-1 flex items-center justify-between gap-2">
        <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          {showRaw ? "Raw arguments" : kindLabel(described)}
        </span>
        {rawDiffers && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-pressed={showRaw}
            onClick={() => setRaw((value) => !value)}
            className="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
          >
            {showRaw ? "Pretty" : "Raw"}
          </Button>
        )}
      </div>
      <div
        className={cn(
          "rounded-lg border bg-background",
          layout === "card"
            ? "max-h-56 overflow-y-auto"
            : "min-h-0 flex-1 overflow-y-auto",
          destructive ? "border-destructive/20" : "border-warning/20",
        )}
      >
        {showRaw ? (
          <pre className={PRE_CLASS}>{clampAskText(args)}</pre>
        ) : (
          <DecodedArgs described={described} />
        )}
      </div>
    </div>
  );
}

function DecodedArgs({ described }: { described: AskArgs }) {
  switch (described.kind) {
    case "shell": {
      const timeout = formatTimeout(described.timeoutMs);
      return (
        <>
          <pre className={PRE_CLASS}>{clampAskText(described.command)}</pre>
          {timeout && (
            <p className="border-t border-border/60 px-3 py-1 text-[11px] text-muted-foreground">
              {timeout}
            </p>
          )}
        </>
      );
    }
    case "edit":
      // The size note (`-N +N`, plus `replace all`) is the TUI's Edit
      // header: the reader sees how much changes before scrolling the diff.
      return (
        <>
          <PathLine
            path={described.path}
            note={editSizeNote(
              computeLineDiff(described.oldText, described.newText),
              described.replaceAll,
            )}
          />
          <ArgsDiff before={described.oldText} after={described.newText} />
        </>
      );
    case "write":
      // `N lines · overwrites if it exists`: the risk caveat at the gate —
      // Write replaces whatever is at the path.
      return (
        <>
          <PathLine
            path={described.path}
            note={writeSizeNote(described.content)}
          />
          <pre className={PRE_CLASS}>
            {clampAskText(described.content) || "(empty file)"}
          </pre>
        </>
      );
    case "json":
      return <pre className={PRE_CLASS}>{clampAskText(described.pretty)}</pre>;
    case "raw":
      return (
        <pre className={PRE_CLASS}>
          {clampAskText(described.text) || "(no arguments)"}
        </pre>
      );
  }
}

"use client";

import { Plug, Server } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import type { ToolCallInfo } from "@/features/agent";
import { friendlyToolName, toolRawArgs } from "@/lib/tool-summary";
import { cn } from "@/lib/utils";
import { HookSection } from "./hook-notice";
import { SidePanel } from "./side-panel";
import { statusDotClass } from "./tool-call-list";
import { parseDiffArgs, ToolDiff } from "./tool-diff";
import { ToolResultParts } from "./tool-result-parts";

/**
 * Pretty-prints a call's input for the panel: objects as indented JSON, a
 * string that happens to BE JSON re-indented, anything else verbatim. Never
 * throws — a cyclic or unserializable input degrades to String().
 */
export function formatToolInput(input: unknown): string {
  if (input === undefined || input === null) return "";
  if (typeof input === "string") {
    try {
      return JSON.stringify(JSON.parse(input), null, 2);
    } catch {
      return input;
    }
  }
  try {
    return JSON.stringify(input, null, 2) ?? String(input);
  } catch {
    return String(input);
  }
}

/**
 * The panel's input text: the verbatim args JSON when the live stream
 * carried it (pretty-printed), else the call's `input` — which hydrated
 * history passes raw but the live reducer flattens to a one-line preview.
 */
export function toolPanelInput(
  call: Pick<ToolCallInfo, "input" | "rawArgs">,
): string {
  return formatToolInput(call.rawArgs || call.input);
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="mt-4 mb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
      {children}
    </p>
  );
}

/**
 * The full, untruncated detail of one tool call in the right-hand side
 * panel: name in the header (`server · tool` for an MCP tool, the raw name
 * beneath), status, the input — an Edit/Write as its diff with a Raw toggle
 * back to the pretty-printed JSON, anything else as that JSON — the raw
 * output, and the result's link/image parts. The inline activity list keeps
 * its one-line summaries — this is the drill-down.
 */
export function ToolCallPanel({
  call,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  call: ToolCallInfo;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  const [showRaw, setShowRaw] = useState(false);
  const input = toolPanelInput(call);
  const head = friendlyToolName(call.name);
  const rawArgs = toolRawArgs(call);
  const diffable = parseDiffArgs(call.name, rawArgs) !== null;
  const showDiff = diffable && !showRaw;
  return (
    <SidePanel
      icon={head.mcp ? Server : Plug}
      title={head.display}
      closeLabel="Close tool call"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span
            aria-hidden="true"
            className={cn("size-1.5 rounded-full", statusDotClass(call.status))}
          />
          {call.status}
          {head.mcp && (
            <span className="truncate font-mono" title="Exact tool name">
              · {head.raw}
            </span>
          )}
        </div>
        <div className="flex items-end justify-between gap-2">
          <SectionLabel>{showDiff ? "Input — diff" : "Input"}</SectionLabel>
          {diffable && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-pressed={showRaw}
              onClick={() => setShowRaw((value) => !value)}
              className="mb-1 h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
            >
              {showRaw ? "Diff" : "Raw"}
            </Button>
          )}
        </div>
        {showDiff ? (
          <ToolDiff name={call.name} rawArgs={rawArgs} />
        ) : (
          <pre className="whitespace-pre-wrap break-words rounded-lg border border-border bg-muted/30 p-3 font-mono text-xs text-foreground/80">
            {input || "(no input)"}
          </pre>
        )}
        <SectionLabel>Output</SectionLabel>
        <pre
          className={cn(
            "whitespace-pre-wrap break-words rounded-lg border p-3 font-mono text-xs",
            call.isError
              ? "border-destructive/40 bg-destructive/5 text-destructive/90"
              : "border-border bg-muted/30 text-foreground/80",
          )}
        >
          {call.output ||
            (call.status === "running" ? "(still running)" : "(no output)")}
        </pre>
        <HookSection hooks={call.hooks} />
        {call.parts && call.parts.length > 0 && (
          <>
            <SectionLabel>Result parts</SectionLabel>
            <ToolResultParts parts={call.parts} size="lg" />
          </>
        )}
      </div>
    </SidePanel>
  );
}

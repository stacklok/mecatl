// SPDX-License-Identifier: Apache-2.0

import { useId, useState } from "react";
import { Button } from "../../components/ui/button";
import { diffLines } from "../../lib/line-diff";
import { cn } from "../../lib/utils";

/**
 * The inline Edit/Write view of a tool call, ported from Mecatl Studio's
 * `tool-diff.tsx` (itself the web port of the TUI's `renderEditDiff` /
 * `renderWriteDiff`): a header naming the path and the size of the change,
 * red/green rows capped at `DIFF_LINE_CAP` with a "Show all" toggle. Pure
 * React — no Next.js coupling — so it can be wired directly into
 * `tool-activity.tsx`'s expanded tool-call rows for the Edit/Write kinds.
 */

type DiffLineKind = "context" | "removed" | "added";

interface DiffLine {
  kind: DiffLineKind;
  text: string;
}

export interface LineDiff {
  lines: DiffLine[];
  removed: number;
  added: number;
  /**
   * True when the inputs exceeded the bounded LCS and the diff degraded to
   * the removed block followed by the added block (still -N/+N accurate).
   */
  coarse: boolean;
}

/** Rows shown before the "Show all N lines" toggle. */
export const DIFF_LINE_CAP = 40;

/** Splits on "\n"; the empty text has NO lines (a deletion, not one blank line). */
function splitLines(text: string): string[] {
  return text === "" ? [] : text.split("\n");
}

/**
 * Line diff of `oldText` → `newText` with its -N/+N counts. Above the LCS
 * bound (`LINE_DIFF_MAX_LINES`) it falls back to one removed block and one
 * added block, so a huge rewrite still renders and still counts.
 */
export function computeLineDiff(oldText: string, newText: string): LineDiff {
  const ops = diffLines(oldText, newText);
  if (ops === null) {
    const removedLines = splitLines(oldText);
    const addedLines = splitLines(newText);
    return {
      lines: [
        ...removedLines.map((text) => ({ kind: "removed" as const, text })),
        ...addedLines.map((text) => ({ kind: "added" as const, text })),
      ],
      removed: removedLines.length,
      added: addedLines.length,
      coarse: true,
    };
  }
  let removed = 0;
  let added = 0;
  const lines: DiffLine[] = ops.map((op) => {
    if (op.type === "del") {
      removed += 1;
      return { kind: "removed", text: op.text };
    }
    if (op.type === "add") {
      added += 1;
      return { kind: "added", text: op.text };
    }
    return { kind: "context", text: op.text };
  });
  return { lines, removed, added, coarse: false };
}

export interface EditArgs {
  kind: "edit";
  path: string;
  oldString: string;
  newString: string;
  replaceAll: boolean;
}

export interface WriteArgs {
  kind: "write";
  path: string;
  content: string;
}

const EDIT_TOOL = /^edit$/i;
const WRITE_TOOL = /^write$/i;

/** True for the two tool names that render as a diff (Edit, Write). */
export function isDiffTool(name: string): boolean {
  return EDIT_TOOL.test(name) || WRITE_TOOL.test(name);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

/** Parses a tool call's raw `args` JSON string into a plain object, or null. */
function parseJsonRecord(rawArgs: string | undefined): Record<string, unknown> | null {
  if (!rawArgs || rawArgs.trim() === "") return null;
  try {
    const parsed = JSON.parse(rawArgs);
    return isRecord(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * An Edit call's args (`path`, `old_string`, `new_string`, `replace_all` —
 * the engine's `engine/adapter/fstools/edit.go` ToolSpec), or null when the
 * args are missing/malformed.
 */
export function parseEditArgs(rawArgs: string | undefined): EditArgs | null {
  const parsed = parseJsonRecord(rawArgs);
  if (!parsed) return null;
  const path = optionalString(parsed.path);
  const oldString = optionalString(parsed.old_string);
  const newString = optionalString(parsed.new_string);
  if (path === undefined || oldString === undefined || newString === undefined) return null;
  return {
    kind: "edit",
    path,
    oldString,
    newString,
    replaceAll: parsed.replace_all === true,
  };
}

/**
 * A Write call's args (`path`, `content` — `engine/adapter/fstools/write.go`),
 * or null when the args are missing/malformed.
 */
export function parseWriteArgs(rawArgs: string | undefined): WriteArgs | null {
  const parsed = parseJsonRecord(rawArgs);
  if (!parsed) return null;
  const path = optionalString(parsed.path);
  const content = optionalString(parsed.content);
  if (path === undefined || content === undefined) return null;
  return { kind: "write", path, content };
}

/** The diffable args of a call by tool name, or null for any other call. */
export function parseDiffArgs(
  name: string,
  rawArgs: string | undefined,
): EditArgs | WriteArgs | null {
  if (EDIT_TOOL.test(name)) return parseEditArgs(rawArgs);
  if (WRITE_TOOL.test(name)) return parseWriteArgs(rawArgs);
  return null;
}

/** "-N +N" (+ " · replace all") — the Edit header's size note. */
export function editSizeNote(diff: LineDiff, replaceAll: boolean): string {
  return `-${diff.removed} +${diff.added}${replaceAll ? " · replace all" : ""}`;
}

/** The Write header's risk caveat: the tool replaces any existing file. */
export const WRITE_OVERWRITE_NOTE = "overwrites if it exists";

/** "N lines · overwrites if it exists" — the Write header's note. */
export function writeSizeNote(content: string): string {
  const count = splitLines(content).length;
  return `${count} line${count === 1 ? "" : "s"} · ${WRITE_OVERWRITE_NOTE}`;
}

const ROW_CLASS: Record<DiffLineKind, string> = {
  context: "",
  added: "bg-success/10",
  removed: "bg-destructive/10",
};

const GUTTER: Record<DiffLineKind, string> = {
  context: " ",
  added: "+",
  removed: "-",
};

const GUTTER_CLASS: Record<DiffLineKind, string> = {
  context: "text-muted-foreground/60",
  added: "text-foreground",
  removed: "text-foreground",
};

function DiffRows({
  lines,
  cap = DIFF_LINE_CAP,
  bounded = false,
}: {
  lines: DiffLine[];
  cap?: number;
  bounded?: boolean;
}) {
  const [showAll, setShowAll] = useState(false);
  const bodyId = useId();
  if (lines.length === 0) {
    return <p className="px-3 py-1.5 text-xs text-muted-foreground">(no lines)</p>;
  }
  const shown = !bounded && showAll ? lines : lines.slice(0, cap);
  const hidden = lines.length - shown.length;
  return (
    <>
      <div id={bodyId} className="overflow-x-auto py-1 font-mono text-xs leading-relaxed">
        {shown.map((line, index) => (
          <div
            // Rows are positional and immutable for a given pair, so the
            // index is the identity.
            // biome-ignore lint/suspicious/noArrayIndexKey: positional diff rows
            key={`${index}-${line.kind}`}
            data-diff={line.kind}
            className={cn("flex min-w-max px-2", ROW_CLASS[line.kind])}
          >
            <span
              aria-hidden="true"
              className={cn("w-4 shrink-0 select-none text-center", GUTTER_CLASS[line.kind])}
            >
              {GUTTER[line.kind]}
            </span>
            {line.kind !== "context" && (
              <span className="sr-only">
                {line.kind === "added" ? "Added line: " : "Removed line: "}
              </span>
            )}
            <span className="whitespace-pre pl-1">{line.text}</span>
          </div>
        ))}
      </div>
      {bounded && hidden > 0 && (
        <p className="border-t border-border/60 px-3 py-1 text-xs text-muted-foreground">
          {hidden} more diff lines. Inspect raw arguments for the complete input.
        </p>
      )}
      {!bounded && (hidden > 0 || showAll) && lines.length > cap && (
        <div className="border-t border-border/60 px-2 py-1">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-expanded={showAll}
            aria-controls={bodyId}
            onClick={() => setShowAll((value) => !value)}
            className="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
          >
            {showAll ? `Show first ${cap} lines` : `Show all ${lines.length} lines`}
          </Button>
        </div>
      )}
    </>
  );
}

function DiffHeader({ path, note }: { path: string; note: string }) {
  return (
    <p
      data-testid="diff-header"
      className="flex min-w-0 items-baseline gap-2 border-b border-border/60 px-3 py-1.5 font-mono text-xs"
    >
      <span className="min-w-0 truncate text-foreground" title={path}>
        {path}
      </span>{" "}
      <span className="shrink-0 text-muted-foreground">{note}</span>
    </p>
  );
}

/**
 * An Edit call: `path  -N +N (replace all)` over the before → after rows.
 */
export function EditDiffBlock({
  bounded = false,
  path,
  oldText,
  newText,
  replaceAll = false,
  className,
}: {
  bounded?: boolean;
  path: string;
  oldText: string;
  newText: string;
  replaceAll?: boolean;
  className?: string;
}) {
  const diff = computeLineDiff(oldText, newText);
  return (
    <div
      data-testid="edit-diff"
      className={cn("overflow-hidden rounded-lg border border-border bg-background", className)}
    >
      <DiffHeader path={path} note={editSizeNote(diff, replaceAll)} />
      {diff.coarse && (
        <p className="border-b border-border/60 px-3 py-1 text-[11px] text-muted-foreground">
          Too many lines to align; showing the removed block, then the added block.
        </p>
      )}
      <DiffRows bounded={bounded} lines={diff.lines} />
    </div>
  );
}

/**
 * A Write call: `path · N lines (overwrites if it exists)` over an all-green
 * body — the whole file is new content as far as the reader can tell.
 */
export function WriteBlock({
  bounded = false,
  path,
  content,
  className,
}: {
  bounded?: boolean;
  path: string;
  content: string;
  className?: string;
}) {
  const lines = splitLines(content).map((text) => ({ kind: "added" as const, text }));
  return (
    <div
      data-testid="write-block"
      className={cn("overflow-hidden rounded-lg border border-border bg-background", className)}
    >
      <DiffHeader path={path} note={writeSizeNote(content)} />
      <DiffRows bounded={bounded} lines={lines} />
    </div>
  );
}

/**
 * The diff view for one tool call, chosen by tool name; renders nothing for
 * a call that is not an Edit/Write or whose args do not parse (the caller
 * keeps its raw view). Wire this in for `ToolActivity` rows whose `name` is
 * Edit/Write, passing the row's `args` as `rawArgs`.
 */
export function ToolDiff({
  bounded = false,
  name,
  rawArgs,
  className,
}: {
  bounded?: boolean;
  name: string;
  rawArgs: string | undefined;
  className?: string;
}) {
  const parsed = parseDiffArgs(name, rawArgs);
  if (!parsed) return null;
  if (parsed.kind === "edit") {
    return (
      <EditDiffBlock
        bounded={bounded}
        path={parsed.path}
        oldText={parsed.oldString}
        newText={parsed.newString}
        replaceAll={parsed.replaceAll}
        className={className}
      />
    );
  }
  return (
    <WriteBlock
      bounded={bounded}
      path={parsed.path}
      content={parsed.content}
      className={className}
    />
  );
}

// SPDX-License-Identifier: Apache-2.0

/**
 * One-line summaries for the tool-call rows (the TUI's compact tier), ported
 * from the prototype (`stack-08`): a clamped `key: value · key: value` view of
 * a call's args, a bounded result preview that names a JSON payload's shape
 * instead of dumping it, and the `server · tool` head an MCP tool gets. The
 * full input and output stay one click away, in the tool panel.
 */

import { toolDisplayParts } from "@/lib/tool-names";

const ELLIPSIS = "…";

/** Whole-file bodies read as a line count, never inline. */
const BODY_KEYS: ReadonlySet<string> = new Set(["content", "new_string", "old_string"]);

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** Clamps to `max` code points with a trailing ellipsis (never splits a pair). */
export function clampChars(text: string, max: number): string {
  const chars = [...text];
  if (chars.length <= max) return text;
  return `${chars.slice(0, Math.max(0, max - 1)).join("")}${ELLIPSIS}`;
}

/** Folds every line break and run of whitespace into one space. */
function flatten(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

/** "N lines" for a body argument — the count the TUI's card shows. */
function lineCount(text: string): number {
  if (text === "") return 0;
  return text.split("\n").length;
}

export interface SummarizeArgsOptions {
  /** Keys shown before the `+N more` rollup (default 4). */
  maxKeys?: number;
  /** Per-value clamp in code points (default 60). */
  maxValueChars?: number;
}

/**
 * A tool call's args as `key: value · key: value · +N more`: strings are
 * flattened and clamped, objects/arrays shown as compact JSON (clamped), and
 * the whole-file bodies (`content`, `new_string`, `old_string`) read as
 * `<N lines>`. Non-object or non-JSON args come back flattened and clamped.
 */
export function summarizeArgs(
  rawArgs: string | undefined,
  options: SummarizeArgsOptions = {},
): string {
  const maxKeys = options.maxKeys ?? 4;
  const maxValueChars = options.maxValueChars ?? 60;
  if (!rawArgs || rawArgs.trim() === "") return "";
  let parsed: unknown;
  try {
    parsed = JSON.parse(rawArgs);
  } catch {
    return clampChars(flatten(rawArgs), maxValueChars);
  }
  if (!isRecord(parsed)) {
    return clampChars(flatten(rawArgs), maxValueChars);
  }
  const entries = Object.entries(parsed);
  const shown = entries.slice(0, maxKeys).map(([key, value]) => {
    if (BODY_KEYS.has(key) && typeof value === "string") {
      const lines = lineCount(value);
      return `${key}: <${lines} line${lines === 1 ? "" : "s"}>`;
    }
    if (typeof value === "string") {
      return `${key}: ${clampChars(flatten(value), maxValueChars)}`;
    }
    let rendered: string;
    try {
      rendered = JSON.stringify(value) ?? String(value);
    } catch {
      rendered = String(value);
    }
    return `${key}: ${clampChars(rendered, maxValueChars)}`;
  });
  const hidden = entries.length - shown.length;
  if (hidden > 0) shown.push(`+${hidden} more`);
  return shown.join(" · ");
}

export interface SummarizeResultOptions {
  /** Clamp for the flattened text preview (default 120). */
  maxChars?: number;
  /** Keys named before the `(+N)` rollup for a JSON object (default 3). */
  maxKeys?: number;
}

/**
 * A tool result's one-line preview: a JSON object names its keys
 * (`keys: a, b, c (+N)`), a JSON array its length (`N items`), and any other
 * text is flattened onto one line and clamped.
 */
export function summarizeResult(
  output: string | undefined,
  options: SummarizeResultOptions = {},
): string {
  const maxChars = options.maxChars ?? 120;
  const maxKeys = options.maxKeys ?? 3;
  if (!output) return "";
  const trimmed = output.trim();
  if (trimmed === "") return "";
  if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    try {
      const parsed: unknown = JSON.parse(trimmed);
      if (Array.isArray(parsed)) {
        return `${parsed.length} item${parsed.length === 1 ? "" : "s"}`;
      }
      if (isRecord(parsed)) {
        const keys = Object.keys(parsed);
        if (keys.length === 0) return "empty object";
        const named = keys.slice(0, maxKeys).join(", ");
        const hidden = keys.length - Math.min(keys.length, maxKeys);
        return `keys: ${named}${hidden > 0 ? ` (+${hidden})` : ""}`;
      }
    } catch {
      // Not JSON after all — fall through to the text preview.
    }
  }
  return clampChars(flatten(trimmed), maxChars);
}

export interface FriendlyToolName {
  /** What the card shows: `Server · Tool` for an MCP tool, else the name. */
  display: string;
  /** The exact tool name (the card's hover title). */
  raw: string;
  mcp: boolean;
  /** The raw `<server>` half of an MCP name. */
  server?: string;
  /** The raw `<tool>` half of an MCP name. */
  tool?: string;
}

/**
 * The head a tool-call row shows: `mcp__github__list_issues` becomes the
 * humanized "GitHub · List issues" (`@/lib/tool-names`, with `mcp: true` so
 * the row can swap its glyph); any other name passes through unchanged.
 */
export function friendlyToolName(name: string): FriendlyToolName {
  const parts = toolDisplayParts(name);
  if (parts.mcp) {
    return {
      display: parts.title,
      raw: name,
      mcp: true,
      server: parts.server,
      tool: parts.tool,
    };
  }
  return { display: name, raw: name, mcp: false };
}

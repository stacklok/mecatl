/**
 * One-line summaries for the inline tool cards (the TUI's compact tier):
 * a clamped `key: value · key: value` view of a call's args, a bounded
 * result preview that names a JSON payload's shape instead of dumping it,
 * the `server · tool` head an MCP tool gets, and the conversation-wide
 * changed-files set the header indicator and its panel read.
 */

import type {
  AgentMessage,
  ToolCallInfo,
  ToolResultPart,
} from "@/features/agent/types";
import type { ToolCallFile } from "@/lib/file-meta";
import { toolDisplayParts } from "@/lib/tool-names";

const ELLIPSIS = "…";

/** Whole-file bodies read as a line count, never inline. */
const BODY_KEYS: ReadonlySet<string> = new Set([
  "content",
  "new_string",
  "old_string",
]);

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
 * The head an inline card shows for a tool: `mcp__github__list_issues`
 * becomes the TUI's humanized "GitHub · List issues" (`@/lib/tool-names`,
 * with `mcp: true` so the row can swap its glyph); any other name passes
 * through unchanged.
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

const MUTATING_FILE_TOOL = /^(edit|write)$/i;
const WRITE_TOOL = /^write$/i;

/**
 * The path an Edit or Write call mutates, read from its raw args JSON
 * (`path`, or `file_path` for schema variants). Undefined for every other
 * tool — Read and friends change nothing. `fileFromToolCall` stays
 * Write-only (it needs previewable content); this is the changed-files
 * tracker, and an Edit counts.
 */
export function changedFileFromToolCall(
  tool: string,
  rawArgs: string | undefined,
): string | undefined {
  if (!MUTATING_FILE_TOOL.test(tool) || !rawArgs) return undefined;
  try {
    const args: unknown = JSON.parse(rawArgs);
    if (!isRecord(args)) return undefined;
    const path =
      typeof args.path === "string" && args.path
        ? args.path
        : typeof args.file_path === "string" && args.file_path
          ? args.file_path
          : undefined;
    return path;
  } catch {
    return undefined;
  }
}

/** One path the conversation changed, with what happened to it. */
export interface ChangedFile {
  path: string;
  /** Edit calls that touched the path. */
  edits: number;
  /** Write calls that (re)wrote the path. */
  writes: number;
  /** The LAST Write's previewable content, when any Write carried one. */
  lastWrite?: ToolCallFile;
}

/**
 * Every path the transcript's Edit/Write calls touched, in first-seen order
 * (the TUI's `filesChanged` set), each with its edit/write counts and the
 * last Write's content for a preview. Reads `changedPath` off each call, so
 * a call hydrated without it (an older daemon shape) is not counted.
 */
export function changedFilesFromMessages(
  messages: readonly Pick<AgentMessage, "toolCalls">[],
): ChangedFile[] {
  const byPath = new Map<string, ChangedFile>();
  for (const message of messages) {
    for (const call of message.toolCalls ?? []) {
      if (!call.changedPath) continue;
      const entry = byPath.get(call.changedPath) ?? {
        path: call.changedPath,
        edits: 0,
        writes: 0,
      };
      if (WRITE_TOOL.test(call.name)) {
        entry.writes += 1;
        if (call.file) entry.lastWrite = call.file;
      } else {
        entry.edits += 1;
      }
      byPath.set(call.changedPath, entry);
    }
  }
  return [...byPath.values()];
}

/**
 * The args JSON a card summarises: the live stream's verbatim `rawArgs`,
 * else a hydrated call's `input` when it is the raw JSON string (the
 * transcript path stores the args there). A flattened preview string is
 * never mistaken for JSON.
 */
export function toolRawArgs(
  call: Pick<ToolCallInfo, "rawArgs" | "input">,
): string | undefined {
  if (call.rawArgs) return call.rawArgs;
  if (typeof call.input === "string") {
    const trimmed = call.input.trim();
    if (trimmed.startsWith("{")) return call.input;
  }
  return undefined;
}

/**
 * Result parts are tool-authored (an MCP server's output) and therefore
 * untrusted: a link renders as an anchor ONLY when it is http(s), and an
 * image is shown ONLY for the raster types a browser decodes safely.
 * Anything else degrades to a plain text label.
 */
export function safeResourceLinkHref(url: string): string | null {
  try {
    const parsed = new URL(url);
    if (parsed.protocol === "http:" || parsed.protocol === "https:") {
      return parsed.href;
    }
  } catch {
    // Not an absolute URL.
  }
  return null;
}

const SAFE_IMAGE_TYPES: ReadonlySet<string> = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
]);

export function safeImageDataUrl(
  part: Extract<ToolResultPart, { kind: "image" }>,
): string | null {
  const mime = part.mimeType.trim().toLowerCase();
  if (!SAFE_IMAGE_TYPES.has(mime) || !part.data) return null;
  if (!/^[A-Za-z0-9+/]+=*$/.test(part.data)) return null;
  return `data:${mime};base64,${part.data}`;
}

/** The text a resource-link part is labelled with. */
export function resourceLinkLabel(
  part: Extract<ToolResultPart, { kind: "resource_link" }>,
): string {
  return part.title || part.name || part.url;
}

import type { ApprovalRequest } from "@/features/agent";

/**
 * A permission ask's args, decoded per tool for the approval card:
 * - `shell`: the bare command text (Shell/Bash `command`, `timeout_ms`);
 * - `edit`: the file path and the before/after texts (Edit `path`,
 *   `old_string`, `new_string`, `replace_all`);
 * - `write`: the file path and the full content (Write `path`, `content`);
 * - `json`: any other well-formed JSON object, indented;
 * - `raw`: empty or non-JSON args, verbatim.
 *
 * The parameter names are the engine's ToolSpecs
 * (engine/adapter/fstools/{shell,edit,write}.go).
 */
export type AskArgs =
  | { kind: "shell"; command: string; timeoutMs?: number }
  | {
      kind: "edit";
      path: string;
      oldText: string;
      newText: string;
      replaceAll: boolean;
    }
  | { kind: "write"; path: string; content: string }
  | { kind: "json"; pretty: string }
  | { kind: "raw"; text: string };

const SHELL_TOOLS = /^(shell|bash)$/i;
const EDIT_TOOL = /^edit$/i;
const WRITE_TOOL = /^write$/i;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const optionalString = (value: unknown): string | undefined =>
  typeof value === "string" ? value : undefined;

/** Decodes an ask's raw `args` JSON string for the tool it names. */
export function describeAskArgs(
  tool: string | undefined,
  args: string | undefined,
): AskArgs {
  const raw = args ?? "";
  if (raw.trim() === "") return { kind: "raw", text: raw };
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { kind: "raw", text: raw };
  }
  if (!isRecord(parsed)) {
    return { kind: "json", pretty: JSON.stringify(parsed, null, 2) };
  }
  const name = (tool ?? "").trim();
  if (SHELL_TOOLS.test(name)) {
    const command = optionalString(parsed.command);
    if (command !== undefined) {
      const timeout = parsed.timeout_ms;
      return {
        kind: "shell",
        command,
        timeoutMs:
          typeof timeout === "number" && Number.isFinite(timeout) && timeout > 0
            ? timeout
            : undefined,
      };
    }
  } else if (EDIT_TOOL.test(name)) {
    const path = optionalString(parsed.path);
    const oldText = optionalString(parsed.old_string);
    const newText = optionalString(parsed.new_string);
    if (path !== undefined && oldText !== undefined && newText !== undefined) {
      return {
        kind: "edit",
        path,
        oldText,
        newText,
        replaceAll: parsed.replace_all === true,
      };
    }
  } else if (WRITE_TOOL.test(name)) {
    const path = optionalString(parsed.path);
    const content = optionalString(parsed.content);
    if (path !== undefined && content !== undefined) {
      return { kind: "write", path, content };
    }
  }
  return { kind: "json", pretty: JSON.stringify(parsed, null, 2) };
}

/**
 * The verbatim/decoded split for one ask. Asks translated before the raw
 * tier existed (or built by another path) carry only the joined `details`
 * string; those render it verbatim, with no separate reason paragraph.
 */
export function askArgsSource(
  approval: Pick<ApprovalRequest, "reason" | "args" | "details">,
): { reason: string; args: string } {
  if (approval.args === undefined && approval.reason === undefined) {
    return { reason: "", args: approval.details };
  }
  return { reason: approval.reason ?? "", args: approval.args ?? "" };
}

/** The tool an ask names, falling back to the description's subject. */
export function askToolName(
  approval: Pick<ApprovalRequest, "toolName" | "description">,
): string {
  return (
    approval.toolName ||
    approval.description.replace(/ needs your approval\.?$/i, "").trim() ||
    "Tool"
  );
}

/**
 * The daemon does not clamp ask args (a Write ask carries the whole file),
 * so every verbatim block the card renders is bounded here. Counted in code
 * points so the cut never splits a surrogate pair.
 */
const ASK_TEXT_MAX_CHARS = 100_000;

export function clampAskText(
  text: string,
  max: number = ASK_TEXT_MAX_CHARS,
): string {
  if (text.length <= max) return text;
  let cut = max;
  const lead = text.charCodeAt(cut - 1);
  if (lead >= 0xd800 && lead <= 0xdbff) cut -= 1;
  const hidden = text.length - cut;
  return `${text.slice(0, cut)}\n… ${hidden.toLocaleString()} more characters not shown`;
}

/** "timeout 120s" / "timeout 1.5s" for the shell caption; "" when unset. */
export function formatTimeout(timeoutMs: number | undefined): string {
  if (timeoutMs === undefined) return "";
  const seconds = timeoutMs / 1000;
  const rendered = Number.isInteger(seconds)
    ? String(seconds)
    : seconds.toFixed(1).replace(/\.0$/, "");
  return `timeout ${rendered}s`;
}

// SPDX-License-Identifier: Apache-2.0

import { parseDiffArgs } from "./edit-diff";
import { hasExactArgNames } from "./tool-arg-names";

/**
 * An approval ask's raw `args` JSON string, decoded per tool for the card's
 * formatted view. Ported from the prototype's `ask-args.ts`; Edit and Write
 * reuse `edit-diff.tsx`'s parsers so the card and the transcript read one
 * shape. The parameter names are the engine's ToolSpecs
 * (engine/adapter/fstools/{shell,edit,write}.go, and engine/agent/shelltool.go
 * for Shell's `background`). A formatted view is only
 * built from args the engine reads the same way (`hasExactArgNames`); any
 * other args show as formatted JSON, every member as sent.
 */
export type AskArgs =
  | ShellAskArgs
  | { kind: "edit" }
  | { kind: "write" }
  | { kind: "json"; pretty: string }
  | { kind: "raw"; text: string };

/** A Shell call's args, with the optional ones only when they change how it runs. */
export interface ShellAskArgs {
  kind: "shell";
  command: string;
  /** `background` when the call runs the command in the background. */
  background?: true;
  /** `temp_scope` when the call sets one. */
  tempScope?: string;
  /** `timeout_ms` when the call sets a non-zero one. */
  timeoutMs?: number;
}

/** Larger arguments are shown as sent, never parsed into a formatted view. */
export const ASK_ARGS_PARSE_LIMIT = 65_536;

const SHELL_TOOLS = /^(shell|bash)$/i;

/** The json tags of the engine's `shellArgs` struct. */
const SHELL_FIELDS: ReadonlySet<string> = new Set([
  "background",
  "command",
  "timeout_ms",
  "temp_scope",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * A Shell call's command, timeout, background flag, and temporary-storage scope
 * as the engine decodes them, or undefined when the args name anything else, repeat a name,
 * or hold a value the engine would not read as its field's type.
 */
function shellArgs(parsed: unknown, args: string): ShellAskArgs | undefined {
  if (!isRecord(parsed) || !hasExactArgNames(args, SHELL_FIELDS)) return undefined;
  const { background, command, temp_scope: tempScope, timeout_ms: timeoutMs } = parsed;
  if (typeof command !== "string") return undefined;
  if (timeoutMs != null && !Number.isSafeInteger(timeoutMs)) return undefined;
  if (background != null && typeof background !== "boolean") return undefined;
  if (tempScope != null && typeof tempScope !== "string") return undefined;
  const shell: ShellAskArgs = { command, kind: "shell" };
  if (background === true) shell.background = true;
  if (typeof tempScope === "string" && tempScope !== "") shell.tempScope = tempScope;
  if (typeof timeoutMs === "number" && timeoutMs !== 0) shell.timeoutMs = timeoutMs;
  return shell;
}

/** Decodes an ask's raw `args` JSON string for the tool it names. */
export function describeAskArgs(tool: string, args: string): AskArgs {
  if (args.trim() === "" || args.length > ASK_ARGS_PARSE_LIMIT) return { kind: "raw", text: args };
  let parsed: unknown;
  try {
    parsed = JSON.parse(args);
  } catch {
    return { kind: "raw", text: args };
  }
  const name = tool.trim();
  const diff = parseDiffArgs(name, args);
  if (diff) return { kind: diff.kind };
  const shell = SHELL_TOOLS.test(name) ? shellArgs(parsed, args) : undefined;
  if (shell) return shell;
  return { kind: "json", pretty: JSON.stringify(parsed, null, 2) };
}

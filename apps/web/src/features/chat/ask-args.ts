// SPDX-License-Identifier: Apache-2.0

import { parseDiffArgs } from "./edit-diff";

/**
 * An approval ask's raw `args` JSON string, decoded per tool for the card's
 * formatted view. Ported from the prototype's `ask-args.ts`; Edit and Write
 * reuse `edit-diff.tsx`'s parsers so the card and the transcript read one
 * shape. The parameter names are the engine's ToolSpecs
 * (engine/adapter/fstools/{shell,edit,write}.go).
 */
export type AskArgs =
  | { kind: "shell"; command: string }
  | { kind: "edit" }
  | { kind: "write" }
  | { kind: "json"; pretty: string }
  | { kind: "raw"; text: string };

/** Larger arguments are shown as sent, never parsed into a formatted view. */
export const ASK_ARGS_PARSE_LIMIT = 65_536;

const SHELL_TOOLS = /^(shell|bash)$/i;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
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
  if (isRecord(parsed) && SHELL_TOOLS.test(name) && typeof parsed.command === "string") {
    return { command: parsed.command, kind: "shell" };
  }
  return { kind: "json", pretty: JSON.stringify(parsed, null, 2) };
}

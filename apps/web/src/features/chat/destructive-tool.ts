// SPDX-License-Identifier: Apache-2.0

const destructiveVerbs: ReadonlySet<string> = new Set([
  "delete",
  "destroy",
  "drop",
  "purge",
  "remove",
  "revoke",
  "rm",
  "rmdir",
]);

const mcpPrefix = "mcp__";
/** A configured MCP server name: `[A-Za-z0-9_]+`, never containing `__`. */
const mcpServerName = /^[A-Za-z0-9_]+$/u;

/**
 * The part of a tool name that names the action. An MCP tool is
 * `mcp__<server>__<tool>`, and the server segment is configuration, not an action,
 * so `mcp__remove_bg__preview` is judged by `preview` alone. The server ends at the
 * first `__`, as the daemon splits it; a name whose server segment is not a valid
 * server name is judged whole.
 */
function actionPart(name: string): string {
  if (!name.startsWith(mcpPrefix)) return name;
  const rest = name.slice(mcpPrefix.length);
  const end = rest.indexOf("__");
  if (end < 0) return name;
  const tool = rest.slice(end + 2);
  return mcpServerName.test(rest.slice(0, end)) && tool !== "" ? tool : name;
}

/**
 * Splits a tool name into lowercase tokens. Segments are runs of ASCII letters and digits,
 * so `_` (including MCP's `server__tool`), `-`, `.`, `/`, `:`, and spaces all
 * separate them. Each segment is kept whole and also split at case boundaries
 * (`removeBranch`, `DeleteFile`, `HTTPDelete`, `v2Delete`).
 */
export function toolNameTokens(name: string): string[] {
  const tokens: string[] = [];
  for (const segment of name.split(/[^A-Za-z0-9]+/u)) {
    if (segment === "") continue;
    tokens.push(segment.toLowerCase());
    const words = segment.split(/(?<=[a-z0-9])(?=[A-Z])|(?<=[A-Z])(?=[A-Z][a-z])/u);
    if (words.length > 1) for (const word of words) tokens.push(word.toLowerCase());
  }
  return tokens;
}

/**
 * Whether a tool name reads as data-destroying, which gives its approval card the
 * destructive treatment instead of the warning one. The permission ask carries no
 * structured risk signal, so this is a presentation heuristic over whole name tokens:
 * `delete_file` and `rmdir` are destructive, `deleted_items_report`, `confirm`, and
 * `dropdown` are not. An MCP tool's server segment is skipped.
 *
 * The name is read both as written and NFKC-normalized. Normalizing makes
 * compatibility letters such as ſ (U+017F), the Kelvin sign (U+212A), and fullwidth
 * letters read as their ASCII forms; reading the raw name too keeps a character that
 * normalizes to a letter or digit (`ｘdelete`, `①delete`) from fusing into a verb.
 */
export function isDestructiveToolName(name: string): boolean {
  const action = actionPart(name);
  const destructive = (form: string) =>
    toolNameTokens(form).some((token) => destructiveVerbs.has(token));
  return destructive(action) || destructive(action.normalize("NFKC"));
}

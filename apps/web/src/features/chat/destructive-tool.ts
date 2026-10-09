// SPDX-License-Identifier: Apache-2.0

const destructiveVerbs: ReadonlySet<string> = new Set([
  "delete",
  "destroy",
  "drop",
  "purge",
  "remove",
  "revoke",
  "rm",
]);

/**
 * Splits a tool name into lowercase tokens. Segments are runs of ASCII letters and
 * digits, so `_` (including MCP's `server__tool`), `-`, `.`, `/`, `:`, and spaces all
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
 * `delete_file` is destructive, `deleted_items_report`, `confirm`, and `dropdown` are not.
 */
export function isDestructiveToolName(name: string): boolean {
  return toolNameTokens(name).some((token) => destructiveVerbs.has(token));
}

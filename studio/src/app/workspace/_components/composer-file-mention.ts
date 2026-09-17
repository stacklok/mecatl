/**
 * The file rows of the composer's `@` menu (the TUI's `@`-mention file menu,
 * web-shaped). Two synthetic rows ride the agent menu's item list:
 *
 * - "Attach a file…" — opens the native picker for a CLIENT-LOCAL file
 *   (images/audio become media parts, everything else is inlined as text).
 *   Shown for an empty query or a prefix of "file", so `@f` / `@fil` reach it.
 * - "Mention path @<query>" — for a path-shaped token (`src/`, `main.go`,
 *   `./x`, `~/y`): inserts the plain text `@<query>` so the model reads the
 *   path and can `Read` it. No completion is claimed — the daemon exposes no
 *   file-listing route, so Studio never pretends to know the tree.
 *
 * Neither row inserts a chip; the composer's select handler recognises them
 * by id and acts instead. Both are `ComposerMenuItem`s so the existing menu
 * renders them with no new kind.
 */

import type { ComposerMenuItem } from "./composer-mentions";

/** The sentinel id of the Attach-a-file row. */
export const ATTACH_FILE_MENU_ID = "__attach-file";
const PATH_MENTION_PREFIX = "__path:";

/** The attach row is offered while the query could still be typing "file". */
function offersAttachRow(query: string): boolean {
  return "file".startsWith(query.toLowerCase());
}

/** A token with a slash or a dot reads as a path; whitespace never does. */
export function isPathLikeQuery(query: string): boolean {
  return query.length > 0 && !/\s/.test(query) && /[/.]/.test(query);
}

/** The rows to PREPEND to the agent matches for this query. */
export function fileMenuRows(query: string): ComposerMenuItem[] {
  const rows: ComposerMenuItem[] = [];
  if (offersAttachRow(query)) {
    rows.push({
      id: ATTACH_FILE_MENU_ID,
      label: "file",
      primary: "Attach a file…",
      secondary:
        "pick a local file to send with this message (images and audio as media, anything else as text)",
    });
  }
  if (isPathLikeQuery(query)) {
    rows.push({
      id: `${PATH_MENTION_PREFIX}${query}`,
      label: `@${query}`,
      primary: `Mention path @${query}`,
      secondary:
        "inserts the path as text — the agent can read it from the workspace",
    });
  }
  return rows;
}

export function isAttachFileItem(item: Pick<ComposerMenuItem, "id">): boolean {
  return item.id === ATTACH_FILE_MENU_ID;
}

/** The plain text a path row inserts (`@src/main.go`), or null for any other row. */
export function pathMentionText(
  item: Pick<ComposerMenuItem, "id">,
): string | null {
  if (!item.id.startsWith(PATH_MENTION_PREFIX)) return null;
  return `@${item.id.slice(PATH_MENTION_PREFIX.length)}`;
}

/** True for either file row (the menu draws a paperclip, not the agent glyph). */
export function isFileMenuItem(item: Pick<ComposerMenuItem, "id">): boolean {
  return isAttachFileItem(item) || pathMentionText(item) !== null;
}

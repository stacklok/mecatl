import type { CSSProperties } from "react";
import { modeComposerRingClass } from "@/lib/permission-mode";
import type { SessionPermissionMode } from "@/lib/protocol";

/**
 * The composer box's two layout decisions, extracted pure (like
 * `resolveComposerAction`) so the precedence table and the row policy are
 * testable without mounting TipTap:
 *
 * - `composerFrameClass` — which border/ring the frame wears. One state wins
 *   at a time, in this order: a file dragged OVER the box (brand, strongest —
 *   the drop target must read as such), a drag anywhere in the window
 *   (brand/50, "you can drop here"), a queued draft while a run streams
 *   (warning — the text will steer or queue, not send), the permission-mode
 *   tint (the TUI recolours its input by mode: Plan → info, Accept edits →
 *   success; Manual gets none), then the plain border.
 * - `composerEditorStyle` — the resting height. The TUI's input box rests at
 *   three rows and grows to eight before it scrolls inside itself; the
 *   stylesheet (`.composer-editor .ProseMirror`, globals.css) carries that
 *   default and the eight-row cap, and a caller's `rows` / `compact` reach it
 *   through the `--composer-min-rows` custom property set on the editor's
 *   wrapper. Mobile (≤499px) pins one row in the stylesheet itself, on the
 *   editor element, so it wins over any wrapper value.
 */

/** Rows the composer rests at when a caller sets neither `rows` nor `compact`. */
export const COMPOSER_DEFAULT_MIN_ROWS = 3;

/** Rows the composer grows to before it scrolls inside itself. */
export const COMPOSER_MAX_ROWS = 8;

/** The custom property the stylesheet reads for the resting row count. */
export const COMPOSER_MIN_ROWS_VAR = "--composer-min-rows";

const PLAIN_FRAME = "border-zinc-300 dark:border-zinc-700";

export function composerFrameClass(input: {
  /** The mode as SHOWN (a held switch over the confirmed one); omit it on a
      surface with no Mode selector, which never tints. */
  mode?: SessionPermissionMode;
  isDragOver: boolean;
  isWindowDrag: boolean;
  isStreaming: boolean;
  hasText: boolean;
}): string {
  if (input.isDragOver) {
    return "border-brand bg-brand/5 dark:bg-brand/10 ring-2 ring-brand/20";
  }
  if (input.isWindowDrag) return "border-brand/50 ring-1 ring-brand/10";
  if (input.isStreaming && input.hasText) {
    return "border-warning shadow-warning/10";
  }
  const ring = input.mode ? modeComposerRingClass(input.mode) : "";
  return ring || PLAIN_FRAME;
}

/**
 * The resting row count for a composer: `compact` (a side-panel composer)
 * is one row; an explicit `rows` is clamped to [1, COMPOSER_MAX_ROWS] so a
 * minimum can never exceed the scroll cap; otherwise the TUI's three.
 */
export function composerMinRows(input: {
  rows?: number;
  compact?: boolean;
}): number {
  if (input.compact) return 1;
  const rows = input.rows;
  if (rows === undefined || !Number.isFinite(rows)) {
    return COMPOSER_DEFAULT_MIN_ROWS;
  }
  return Math.min(COMPOSER_MAX_ROWS, Math.max(1, Math.floor(rows)));
}

/**
 * The inline style for the editor's wrapper: the `--composer-min-rows`
 * custom property when a caller asked for something, undefined otherwise so
 * the stylesheet's default stands alone (no inline style to override).
 */
export function composerEditorStyle(input: {
  rows?: number;
  compact?: boolean;
}): CSSProperties | undefined {
  if (input.rows === undefined && !input.compact) return undefined;
  return {
    [COMPOSER_MIN_ROWS_VAR]: composerMinRows(input),
  } as CSSProperties;
}

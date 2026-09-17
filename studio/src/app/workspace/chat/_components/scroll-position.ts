/**
 * Pure helpers for the transcript's scroll position: the `↑ NN%` cue the TUI
 * shows in its header while the view is anchored (not following the tail),
 * and how far one keyboard page moves.
 */

/**
 * Fraction of the visible height one PgUp/PgDn press scrolls — a little short
 * of a full page so a line of context carries over between pages.
 */
export const PAGE_FRACTION = 0.9;

/**
 * Where the viewport sits in its scrollable range, 0 (top) to 100 (bottom),
 * mirroring Bubble Tea's `ScrollPercent()` that the TUI renders as `↑ NN%`.
 * Content that doesn't overflow reads as 100 (the end is in view), and an
 * over-scrolled position (rubber-banding, a mid-layout measurement) clamps
 * into range instead of leaking a negative or >100 figure.
 */
export function scrollPositionPercent(
  scrollTop: number,
  scrollHeight: number,
  clientHeight: number,
): number {
  const max = scrollHeight - clientHeight;
  if (!Number.isFinite(max) || max <= 0) return 100;
  const ratio = Math.min(1, Math.max(0, scrollTop / max));
  return Math.round(ratio * 100);
}

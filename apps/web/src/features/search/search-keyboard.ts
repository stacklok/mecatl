// SPDX-License-Identifier: Apache-2.0

/**
 * The composition-relevant slice of a keydown. `isComposing` comes from the
 * native event; `keyCode` 229 is what browsers (notably Safari and older
 * Chrome) report for keys an input method editor consumes, sometimes without
 * setting `isComposing`.
 */
export interface CompositionKeyState {
  isComposing?: boolean;
  keyCode?: number;
}

const IME_PROCESS_KEY_CODE = 229;

/** Whether an input method editor currently owns the keystroke. */
export function isImeComposing(event: CompositionKeyState): boolean {
  return event.isComposing === true || event.keyCode === IME_PROCESS_KEY_CODE;
}

/**
 * Whether an Enter keypress in the search field should open the highlighted
 * result. Enter that commits an IME composition belongs to the input method,
 * never to the palette.
 */
export function shouldActivateResult(
  event: CompositionKeyState & { key: string },
  hasActiveResult: boolean,
): boolean {
  return event.key === "Enter" && hasActiveResult && !isImeComposing(event);
}

/** Clamps a highlighted index into a result list of `count` items (0 when empty). */
export function clampActiveIndex(index: number, count: number): number {
  if (count <= 0) return 0;
  return Math.min(Math.max(index, 0), count - 1);
}

/** Moves the highlight one step, stopping at either end of the list. */
export function moveActiveIndex(current: number, step: -1 | 1, count: number): number {
  return clampActiveIndex(clampActiveIndex(current, count) + step, count);
}

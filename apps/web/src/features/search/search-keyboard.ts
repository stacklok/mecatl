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

export interface SearchKeyState extends CompositionKeyState {
  key: string;
}

const IME_PROCESS_KEY_CODE = 229;

/** Whether an input method editor currently owns the keystroke. */
export function isImeComposing(event: CompositionKeyState): boolean {
  return event.isComposing === true || event.keyCode === IME_PROCESS_KEY_CODE;
}

/**
 * Composition events and the committing keydown arrive in different orders
 * across browsers. Keep ownership with the IME until that key has passed.
 */
export function createSearchCompositionGuard() {
  let composing = false;
  let awaitingCommitKey = false;
  let pointerDuringComposition = false;

  return {
    start() {
      composing = true;
      awaitingCommitKey = false;
      pointerDuringComposition = false;
    },
    end() {
      composing = false;
      awaitingCommitKey = !pointerDuringComposition;
      pointerDuringComposition = false;
    },
    pointerChoice() {
      // An observable pointer/touch choice may end composition without a
      // committing keydown. A subsequent Enter is then a new user action.
      if (composing) pointerDuringComposition = true;
      awaitingCommitKey = false;
    },
    ownsKeyDown(event: SearchKeyState): boolean {
      if (composing || isImeComposing(event)) {
        if (awaitingCommitKey && ["Enter", "ArrowDown", "ArrowUp"].includes(event.key)) {
          awaitingCommitKey = false;
        }
        return true;
      }
      const pending = awaitingCommitKey;
      awaitingCommitKey = false;
      return pending && ["Enter", "ArrowDown", "ArrowUp"].includes(event.key);
    },
    keyUp(event: Pick<SearchKeyState, "key">) {
      if (["Enter", "ArrowDown", "ArrowUp"].includes(event.key)) awaitingCommitKey = false;
    },
  };
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

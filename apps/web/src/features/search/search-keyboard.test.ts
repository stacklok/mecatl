// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  clampActiveIndex,
  createSearchCompositionGuard,
  isImeComposing,
  moveActiveIndex,
  shouldActivateResult,
} from "./search-keyboard";

describe("isImeComposing", () => {
  it("detects composition from the native flag or the IME process key code", () => {
    expect(isImeComposing({ isComposing: true, keyCode: 13 })).toBe(true);
    expect(isImeComposing({ isComposing: false, keyCode: 229 })).toBe(true);
    expect(isImeComposing({ isComposing: false, keyCode: 13 })).toBe(false);
    expect(isImeComposing({})).toBe(false);
  });
});

describe("shouldActivateResult", () => {
  it("activates the highlighted result on a plain Enter", () => {
    expect(shouldActivateResult({ isComposing: false, key: "Enter", keyCode: 13 }, true)).toBe(
      true,
    );
  });

  it("never activates while an IME composition is committing", () => {
    expect(shouldActivateResult({ isComposing: true, key: "Enter", keyCode: 13 }, true)).toBe(
      false,
    );
    expect(shouldActivateResult({ isComposing: false, key: "Enter", keyCode: 229 }, true)).toBe(
      false,
    );

    const beforeKeyDown = createSearchCompositionGuard();
    beforeKeyDown.start();
    expect(beforeKeyDown.ownsKeyDown({ key: "ArrowDown" })).toBe(true);
    beforeKeyDown.end();
    // Safari can clear isComposing before dispatching the committing Enter.
    expect(beforeKeyDown.ownsKeyDown({ isComposing: false, key: "Enter", keyCode: 13 })).toBe(true);
    expect(beforeKeyDown.ownsKeyDown({ isComposing: false, key: "Enter", keyCode: 13 })).toBe(
      false,
    );

    const afterKeyDown = createSearchCompositionGuard();
    afterKeyDown.start();
    expect(afterKeyDown.ownsKeyDown({ isComposing: true, key: "Enter", keyCode: 13 })).toBe(true);
    afterKeyDown.end();
    afterKeyDown.keyUp({ key: "Enter" });
    expect(afterKeyDown.ownsKeyDown({ key: "Enter", keyCode: 13 })).toBe(false);

    const processCodeAfterEnd = createSearchCompositionGuard();
    processCodeAfterEnd.start();
    processCodeAfterEnd.end();
    expect(processCodeAfterEnd.ownsKeyDown({ key: "Enter", keyCode: 229 })).toBe(true);
    expect(processCodeAfterEnd.ownsKeyDown({ key: "Enter", keyCode: 13 })).toBe(false);
  });

  it("allows a distinct Enter after an observable pointer or touch candidate choice", () => {
    const pointerBeforeEnd = createSearchCompositionGuard();
    pointerBeforeEnd.start();
    pointerBeforeEnd.pointerChoice();
    pointerBeforeEnd.end();
    expect(pointerBeforeEnd.ownsKeyDown({ key: "Enter" })).toBe(false);

    const touchAfterEnd = createSearchCompositionGuard();
    touchAfterEnd.start();
    touchAfterEnd.end();
    touchAfterEnd.pointerChoice();
    expect(touchAfterEnd.ownsKeyDown({ key: "Enter" })).toBe(false);
  });

  it("ignores other keys and an empty result list", () => {
    expect(shouldActivateResult({ key: "ArrowDown" }, true)).toBe(false);
    expect(shouldActivateResult({ key: "Enter" }, false)).toBe(false);
  });
});

describe("active index movement", () => {
  it("clamps into the result list and to zero when it is empty", () => {
    expect(clampActiveIndex(5, 3)).toBe(2);
    expect(clampActiveIndex(-1, 3)).toBe(0);
    expect(clampActiveIndex(4, 0)).toBe(0);
  });

  it("steps one option at a time and stops at either end", () => {
    expect(moveActiveIndex(0, 1, 3)).toBe(1);
    expect(moveActiveIndex(2, 1, 3)).toBe(2);
    expect(moveActiveIndex(0, -1, 3)).toBe(0);
    expect(moveActiveIndex(7, -1, 3)).toBe(1);
    expect(moveActiveIndex(0, 1, 0)).toBe(0);
  });
});

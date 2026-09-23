// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  clampActiveIndex,
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

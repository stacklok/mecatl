// SPDX-License-Identifier: Apache-2.0

import { onTestFinished, vi } from "vitest";

/**
 * Spy on a `window.localStorage` method for the current test only.
 *
 * DECISION: restoration is registered here, not left to the caller. Under
 * happy-dom neither `vi.restoreAllMocks()` nor the `restoreMocks` flag restores
 * a spy on `localStorage`, so a forgotten `mockRestore()` breaks every later
 * file in the worker (`Quota exceeded` in unrelated tests). Biome bans direct
 * storage spies; see the NO-ISOLATE RULES in test-setup.ts.
 */
export function spyOnLocalStorage<Method extends "getItem" | "setItem" | "removeItem">(
  method: Method,
) {
  // biome-ignore lint/plugin: this helper is the one sanctioned storage spy.
  const spy = vi.spyOn(window.localStorage, method);
  onTestFinished(() => spy.mockRestore());
  return spy;
}

// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { toast } from "sonner";
import { expect, it } from "vitest";
import { writeUserScopedItem } from "./lib/account-storage";
import { spyOnLocalStorage } from "./test-storage";

// SPEC: the shared storage reset runs after a test's own spy restores. These two
// tests run in order: the first ends with a failing `removeItem` spy still
// registered for restoration; if the reset ran before that restore, its clear
// would fail and leave account-storage quarantined for the next test.
it("ends with a failing removeItem spy active", () => {
  writeUserScopedItem("studio.reset-order", "1");
  spyOnLocalStorage("removeItem").mockImplementation(() => {
    throw new Error("blocked");
  });
});

it("starts with account-storage unquarantined", () => {
  writeUserScopedItem("studio.reset-order", "2");
  expect(window.localStorage.getItem("studio.reset-order")).toBe("2");
});

// SPEC: sonner replays its active toasts to every Toaster that mounts, so the
// shared reset dismisses them. In order: the first test leaves a toast active.
it("ends with an active toast", () => {
  toast.success("Left over from an earlier test");
  expect(toast.getToasts()).toHaveLength(1);
});

it("starts with no active toast", () => {
  expect(toast.getToasts()).toEqual([]);
});

const initialViewport = {
  width: window.innerWidth,
  height: window.innerHeight,
  matchMedia: window.matchMedia,
};

// Direct patches in no-isolate workers must not turn a later desktop menu into
// its mobile inline-folder surface (#2603). These tests deliberately run in order.
it("ends with directly patched mobile viewport properties", () => {
  Object.defineProperties(window, {
    innerWidth: { configurable: true, value: 320 },
    innerHeight: { configurable: true, value: 480 },
  });
  window.matchMedia = (query) =>
    ({ matches: query.includes("max-width") && window.innerWidth < 500 }) as MediaQueryList;
  expect(window.matchMedia("(max-width: 499px)").matches).toBe(true);
  expect(window.innerHeight).toBe(480);
});

it("starts with the original viewport properties", () => {
  expect(window.innerWidth).toBe(initialViewport.width);
  expect(window.innerHeight).toBe(initialViewport.height);
  expect(window.matchMedia).toBe(initialViewport.matchMedia);
});

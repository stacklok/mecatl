// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { clearFailedRun, readFailedRun, saveFailedRun } from "./failed-run-storage";

function blockedStorage() {
  return {
    getItem: () => {
      throw new Error("storage blocked");
    },
    key: () => null,
    length: 0,
    removeItem: () => {
      throw new Error("storage blocked");
    },
    setItem: () => {
      throw new Error("storage blocked");
    },
  };
}

describe("failed-run recovery", () => {
  it("keeps a failed prompt in memory when session storage is blocked", () => {
    const store = blockedStorage();
    clearUserScopedStorage(store, store);
    const failure = { message: "Run interrupted", permanent: false, prompt: "Try this" };

    saveFailedRun("s1", failure, store);
    expect(readFailedRun("s1", store)).toEqual(failure);
    clearFailedRun("s1", store);
    expect(readFailedRun("s1", store)).toBeUndefined();
  });

  it("drops the in-memory failed prompt on account cleanup", () => {
    const store = blockedStorage();
    clearUserScopedStorage(store, store);
    saveFailedRun("s1", { message: "Previous account", permanent: false, prompt: "secret" }, store);
    clearUserScopedStorage(store, store);
    expect(readFailedRun("s1", store)).toBeUndefined();
  });
});

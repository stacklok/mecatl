// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { clearUserScopedStorage, reconcileAccount } from "./account-storage";

function memoryStore(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (key: string) => data.get(key) ?? null,
    key: (index: number) => [...data.keys()][index] ?? null,
    get length() {
      return data.size;
    },
    removeItem: (key: string) => void data.delete(key),
    setItem: (key: string, value: string) => void data.set(key, value),
  };
}

describe("account-scoped browser storage", () => {
  it("clears the previous account's data when a different account signs in", () => {
    const store = memoryStore({
      "mecatl-studio-theme": "dark",
      "studio.account": "alice",
      "studio.chat.folders": '["Alice\'s folder"]',
      "studio.profile.user-name": "Alice",
    });
    expect(reconcileAccount("bob", store)).toBe(true);
    expect([...store.data.entries()]).toEqual([
      ["mecatl-studio-theme", "dark"],
      ["studio.account", "bob"],
    ]);
  });

  it("keeps data for the same account and on first sight of an account", () => {
    const same = memoryStore({ "studio.account": "alice", "studio.chat.folders": "[]" });
    expect(reconcileAccount("alice", same)).toBe(false);
    expect(same.data.get("studio.chat.folders")).toBe("[]");

    const first = memoryStore({ "studio.chat.folders": "[]" });
    expect(reconcileAccount("alice", first)).toBe(false);
    expect(first.data.get("studio.account")).toBe("alice");
    expect(first.data.get("studio.chat.folders")).toBe("[]");

    expect(reconcileAccount(undefined, first)).toBe(false);
  });

  it("sign-out clears every user-scoped key but not the device theme", () => {
    const store = memoryStore({
      "mecatl-studio-theme": "light",
      "studio.account": "alice",
      "studio.chat.queue.s1": "[]",
    });
    clearUserScopedStorage(store);
    expect([...store.data.keys()]).toEqual(["mecatl-studio-theme"]);
  });
});

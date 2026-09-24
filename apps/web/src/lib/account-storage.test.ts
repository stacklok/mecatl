// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it } from "vitest";
import {
  clearUserScopedStorage,
  readUserScopedItem,
  reconcileAccount,
  writeUserScopedItem,
} from "./account-storage";

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
  beforeEach(() => clearUserScopedStorage());

  it("clears account data from both browser stores", () => {
    const local = memoryStore({
      "mecatl-studio-theme": "dark",
      "studio.account": "alice",
      "studio.chat.folders": "alice-folder",
      "studio.chat.queue.s1": "alice-queue",
    });
    const session = memoryStore({
      unrelated: "keep",
      "studio.account": "alice",
      "studio.chat.failedRun.s1": "alice-failure",
    });

    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect([...local.data.entries()]).toEqual([
      ["mecatl-studio-theme", "dark"],
      ["studio.account", "bob"],
    ]);
    expect([...session.data.entries()]).toEqual([
      ["unrelated", "keep"],
      ["studio.account", "bob"],
    ]);

    clearUserScopedStorage(local, session);
    expect([...local.data.keys()]).toEqual(["mecatl-studio-theme"]);
    expect([...session.data.keys()]).toEqual(["unrelated"]);
  });

  it("uses the session account marker when another tab updated local storage", () => {
    const local = memoryStore({ "studio.account": "bob" });
    const session = memoryStore({
      "studio.account": "alice",
      "studio.chat.failedRun.s1": "alice-failure",
    });

    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect([...session.data.entries()]).toEqual([["studio.account", "bob"]]);
  });

  it("clears the available store when the other storage area throws", () => {
    const blocked = {
      getItem: () => {
        throw new Error("storage blocked");
      },
      key: () => {
        throw new Error("storage blocked");
      },
      get length(): number {
        throw new Error("storage blocked");
      },
      removeItem: () => {
        throw new Error("storage blocked");
      },
      setItem: () => {
        throw new Error("storage blocked");
      },
    };
    const session = memoryStore({
      "studio.account": "alice",
      "studio.chat.failedRun.s1": "alice-failure",
    });

    expect(() => reconcileAccount("bob", blocked, session)).not.toThrow();
    expect([...session.data.entries()]).toEqual([["studio.account", "bob"]]);
  });

  it("keeps an in-memory fallback while storage is blocked and clears it on account change", () => {
    const blocked = {
      getItem: () => {
        throw new Error("storage blocked");
      },
      key: () => {
        throw new Error("storage blocked");
      },
      get length(): number {
        throw new Error("storage blocked");
      },
      removeItem: () => {
        throw new Error("storage blocked");
      },
      setItem: () => {
        throw new Error("storage blocked");
      },
    };

    clearUserScopedStorage(blocked, blocked);
    reconcileAccount("alice", blocked, blocked);
    writeUserScopedItem("studio.chat.queue.s1", "queued", blocked);
    expect(readUserScopedItem("studio.chat.queue.s1", blocked)).toBe("queued");
    reconcileAccount("bob", blocked, blocked);
    expect(readUserScopedItem("studio.chat.queue.s1", blocked)).toBeNull();
  });

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

  it("keeps marked data for the same account and clears unmarked data on first sight", () => {
    const same = memoryStore({ "studio.account": "alice", "studio.chat.folders": "[]" });
    expect(reconcileAccount("alice", same)).toBe(false);
    expect(same.data.get("studio.chat.folders")).toBe("[]");

    const first = memoryStore({ "studio.chat.folders": "[]" });
    expect(reconcileAccount("alice", first)).toBe(true);
    expect(first.data.get("studio.account")).toBe("alice");
    expect(first.data.get("studio.chat.folders")).toBeUndefined();

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

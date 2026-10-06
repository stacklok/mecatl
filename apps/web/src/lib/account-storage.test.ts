// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearUserScopedStorage,
  quarantinePeerAccount,
  readUserScopedItem,
  reconcileAccount,
  writeUserScopedItem,
} from "./account-storage";

interface StoreFaults {
  enumerate?: boolean;
  readAccount?: boolean;
  remove?: boolean;
  writeAccount?: boolean;
}

function memoryStore(initial: Record<string, string> = {}, faults: StoreFaults = {}) {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (key: string) => {
      if (faults.readAccount && key === "studio.account") throw new Error("account read blocked");
      return data.get(key) ?? null;
    },
    key: (index: number) => {
      if (faults.enumerate) throw new Error("storage enumeration blocked");
      return [...data.keys()][index] ?? null;
    },
    get length() {
      if (faults.enumerate) throw new Error("storage enumeration blocked");
      return data.size;
    },
    removeItem: (key: string) => {
      if (faults.remove && key.startsWith("studio.")) throw new Error("storage removal blocked");
      data.delete(key);
    },
    setItem: (key: string, value: string) => {
      if (faults.writeAccount && key === "studio.account") throw new Error("account write blocked");
      data.set(key, value);
    },
  };
}

describe("account-scoped browser storage", () => {
  beforeEach(() => clearUserScopedStorage());

  it("clears account data from both browser stores", () => {
    const local = memoryStore({
      "mecatl-studio-theme": "dark",
      "mecatl-studio.palette": "solar",
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
      ["mecatl-studio.palette", "solar"],
      ["studio.account", "bob"],
    ]);
    expect([...session.data.entries()]).toEqual([
      ["unrelated", "keep"],
      ["studio.account", "bob"],
    ]);

    clearUserScopedStorage(local, session);
    expect([...local.data.keys()]).toEqual(["mecatl-studio-theme", "mecatl-studio.palette"]);
    expect([...session.data.keys()]).toEqual(["unrelated"]);
  });

  it("uses the session account marker when another tab updated local storage", () => {
    const local = memoryStore({
      "studio.account": "bob",
      "studio.chat.folders": "Bob's folder",
      "studio.chat.queue.same": "Bob's queued prompt",
    });
    const session = memoryStore({
      "studio.account": "alice",
      "studio.chat.failedRun.s1": "alice-failure",
    });

    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect(local.data.get("studio.account")).toBe("bob");
    expect(local.data.get("studio.chat.folders")).toBe("Bob's folder");
    expect(local.data.get("studio.chat.queue.same")).toBe("Bob's queued prompt");
    expect([...session.data.entries()]).toEqual([["studio.account", "bob"]]);
  });

  it("blocks stale writes and uses memory when this tab cannot clear its session store", () => {
    const local = memoryStore({
      "studio.account": "bob",
      "studio.chat.folders": "Bob's folder",
      "studio.chat.queue.same": "Bob's queued prompt",
    });
    const session = memoryStore(
      { "studio.account": "alice", "studio.chat.failedRun.same": "Alice's failed prompt" },
      { remove: true },
    );
    quarantinePeerAccount(local, session);
    writeUserScopedItem("studio.chat.queue.same", "Alice's stale write", local);
    expect(local.data.get("studio.chat.queue.same")).toBe("Bob's queued prompt");
    expect(readUserScopedItem("studio.chat.folders", local)).toBeNull();
    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect(readUserScopedItem("studio.chat.folders", local)).toBeNull();
    expect(readUserScopedItem("studio.chat.failedRun.same", session)).toBeNull();
    writeUserScopedItem("studio.chat.queue.same", "Bob's memory draft", local);
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBe("Bob's memory draft");
    expect(local.data.get("studio.account")).toBe("bob");
    expect(local.data.get("studio.chat.folders")).toBe("Bob's folder");
    expect(session.data.get("studio.chat.failedRun.same")).toBe("Alice's failed prompt");
  });

  it("quarantines a stale tab before its account-change storage event", async () => {
    const local = memoryStore({ "mecatl-studio-theme": "dark" });
    const aliceSession = memoryStore();
    const bobSession = memoryStore();

    vi.resetModules();
    const aliceTab = await import("./account-storage");
    expect(aliceTab.reconcileAccount("alice", local, aliceSession)).toBe(false);
    aliceTab.writeUserScopedItem("studio.chat.folders", "Alice's folder", local);
    aliceTab.writeUserScopedItem("studio.chat.queue.same", "Alice's queued prompt", local);
    aliceTab.writeUserScopedItem(
      "studio.chat.failedRun.same",
      "Alice's failed prompt",
      aliceSession,
    );

    vi.resetModules();
    const bobTab = await import("./account-storage");
    expect(bobTab.reconcileAccount("bob", local, bobSession)).toBe(true);
    bobTab.writeUserScopedItem("studio.chat.folders", "Bob's folder", local);
    bobTab.writeUserScopedItem("studio.chat.queue.same", "Bob's queued prompt", local);
    bobTab.writeUserScopedItem("studio.chat.failedRun.same", "Bob's failed prompt", bobSession);

    // Alice's module has received no storage event and still believes she is signed in.
    expect(aliceTab.readUserScopedItem("studio.chat.folders", local)).toBeNull();
    expect(aliceTab.readUserScopedItem("studio.chat.queue.same", local)).toBeNull();
    expect(aliceTab.listUserScopedKeys("studio.chat.queue.", local)).toEqual([]);
    expect(aliceTab.readUserScopedItem("studio.chat.failedRun.same", aliceSession)).toBeNull();
    expect(aliceTab.listUserScopedKeys("studio.chat.failedRun.", aliceSession)).toEqual([]);
    aliceTab.writeUserScopedItem("studio.chat.folders", "Alice's replacement", local);
    aliceTab.writeUserScopedItem("studio.chat.queue.same", "Alice's replacement", local);
    aliceTab.writeUserScopedItem("studio.chat.failedRun.same", "Alice's replacement", aliceSession);

    expect(local.data.get("studio.account")).toBe("bob");
    expect(local.data.get("studio.chat.folders")).toBe("Bob's folder");
    expect(local.data.get("studio.chat.queue.same")).toBe("Bob's queued prompt");
    expect(bobSession.data.get("studio.chat.failedRun.same")).toBe("Bob's failed prompt");
    expect(aliceSession.data.get("studio.chat.failedRun.same")).toBe("Alice's failed prompt");
    expect(local.data.get("mecatl-studio-theme")).toBe("dark");
    expect(bobTab.readUserScopedItem("studio.chat.folders", local)).toBe("Bob's folder");
  });

  it("keeps current-account drafts in memory when the marker becomes unreadable", () => {
    const localFaults = { readAccount: false };
    const local = memoryStore({}, localFaults);
    const session = memoryStore();

    expect(reconcileAccount("alice", local, session)).toBe(false);
    writeUserScopedItem("studio.chat.queue.same", "saved queue", local);
    writeUserScopedItem("studio.chat.failedRun.same", "saved failure", session);

    localFaults.readAccount = true;
    writeUserScopedItem("studio.chat.queue.same", "memory queue", local);
    writeUserScopedItem("studio.chat.failedRun.same", "memory failure", session);
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBe("memory queue");
    expect(readUserScopedItem("studio.chat.failedRun.same", session)).toBe("memory failure");
    expect(local.data.get("studio.chat.queue.same")).toBe("saved queue");
    expect(session.data.get("studio.chat.failedRun.same")).toBe("saved failure");
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
    expect([...session.data.entries()]).toEqual([["studio.account", "!cleanup-pending"]]);
    expect(readUserScopedItem("studio.chat.failedRun.s1", session)).toBeNull();
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

  it("quarantines failed removals in both stores across an account switch and reload", async () => {
    const localFaults = { remove: true };
    const sessionFaults = { remove: true };
    const local = memoryStore(
      {
        "studio.account": "alice",
        "studio.chat.folders": "Alice's folder",
        "studio.chat.queue.same": "Alice's queued prompt",
      },
      localFaults,
    );
    const session = memoryStore(
      {
        "studio.account": "alice",
        "studio.chat.failedRun.same": "Alice's failed prompt",
      },
      sessionFaults,
    );

    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect(local.data.get("studio.account")).not.toBe("bob");
    expect(session.data.get("studio.account")).not.toBe("bob");
    for (const [key, store] of [
      ["studio.chat.folders", local],
      ["studio.chat.queue.same", local],
      ["studio.chat.failedRun.same", session],
    ] as const) {
      expect(readUserScopedItem(key, store)).toBeNull();
    }

    writeUserScopedItem("studio.chat.queue.same", "Bob's queued prompt", local);
    writeUserScopedItem("studio.chat.failedRun.same", "Bob's failed prompt", session);
    expect(reconcileAccount("bob", local, session)).toBe(false);
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBe("Bob's queued prompt");
    expect(readUserScopedItem("studio.chat.failedRun.same", session)).toBe("Bob's failed prompt");
    expect(local.data.get("studio.chat.queue.same")).toBe("Alice's queued prompt");

    vi.resetModules();
    const reloaded = await import("./account-storage");
    expect(reloaded.reconcileAccount("bob", local, session)).toBe(true);
    expect(reloaded.readUserScopedItem("studio.chat.folders", local)).toBeNull();
    expect(reloaded.readUserScopedItem("studio.chat.queue.same", local)).toBeNull();
    expect(reloaded.readUserScopedItem("studio.chat.failedRun.same", session)).toBeNull();

    localFaults.remove = false;
    sessionFaults.remove = false;
    vi.resetModules();
    const recovered = await import("./account-storage");
    expect(recovered.reconcileAccount("bob", local, session)).toBe(true);
    expect([...local.data.entries()]).toEqual([["studio.account", "bob"]]);
    expect([...session.data.entries()]).toEqual([["studio.account", "bob"]]);
    recovered.writeUserScopedItem("studio.chat.queue.same", "Bob's new prompt", local);
    recovered.writeUserScopedItem("studio.chat.failedRun.same", "Bob's new failure", session);

    vi.resetModules();
    const sameAccountReload = await import("./account-storage");
    expect(sameAccountReload.reconcileAccount("bob", local, session)).toBe(false);
    expect(sameAccountReload.readUserScopedItem("studio.chat.queue.same", local)).toBe(
      "Bob's new prompt",
    );
    expect(sameAccountReload.readUserScopedItem("studio.chat.failedRun.same", session)).toBe(
      "Bob's new failure",
    );
  });

  it("fails closed when storage enumeration or account-marker reads throw", async () => {
    const localFaults = { enumerate: true };
    const sessionFaults = { readAccount: true };
    const local = memoryStore(
      { "studio.account": "alice", "studio.chat.queue.same": "Alice's queued prompt" },
      localFaults,
    );
    const session = memoryStore(
      { "studio.account": "alice", "studio.chat.failedRun.same": "Alice's failed prompt" },
      sessionFaults,
    );

    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBeNull();
    expect(readUserScopedItem("studio.chat.failedRun.same", session)).toBeNull();
    writeUserScopedItem("studio.chat.queue.same", "Bob's draft", local);
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBe("Bob's draft");

    vi.resetModules();
    const reloaded = await import("./account-storage");
    expect(reloaded.reconcileAccount("bob", local, session)).toBe(true);
    expect(reloaded.readUserScopedItem("studio.chat.queue.same", local)).toBeNull();
    expect(reloaded.readUserScopedItem("studio.chat.failedRun.same", session)).toBeNull();

    localFaults.enumerate = false;
    sessionFaults.readAccount = false;
    vi.resetModules();
    const recovered = await import("./account-storage");
    expect(recovered.reconcileAccount("bob", local, session)).toBe(true);
    expect(recovered.readUserScopedItem("studio.chat.queue.same", local)).toBeNull();
    expect(recovered.readUserScopedItem("studio.chat.failedRun.same", session)).toBeNull();
  });

  it("keeps a failed sign-out clear pending when the same account returns", async () => {
    const faults = { remove: true };
    const local = memoryStore(
      { "studio.account": "alice", "studio.chat.folders": "Alice's folder" },
      faults,
    );
    const session = memoryStore({ "studio.account": "alice" });

    clearUserScopedStorage(local, session);
    vi.resetModules();
    const reloaded = await import("./account-storage");
    expect(reloaded.reconcileAccount("alice", local, session)).toBe(true);
    expect(reloaded.readUserScopedItem("studio.chat.folders", local)).toBeNull();
  });

  it("does not trust unmarked data when account-marker writes are blocked", async () => {
    const faults = { remove: true, writeAccount: true };
    const local = memoryStore(
      { "studio.chat.folders": "Alice's folder", "studio.chat.queue.same": "Alice's prompt" },
      faults,
    );
    const session = memoryStore({ "studio.chat.failedRun.same": "Alice's failure" }, faults);

    expect(reconcileAccount("bob", local, session)).toBe(true);
    expect(local.data.has("studio.account")).toBe(false);
    expect(session.data.has("studio.account")).toBe(false);
    expect(readUserScopedItem("studio.chat.folders", local)).toBeNull();
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBeNull();
    expect(readUserScopedItem("studio.chat.failedRun.same", session)).toBeNull();
    writeUserScopedItem("studio.chat.queue.same", "Bob's draft", local);
    writeUserScopedItem("studio.chat.failedRun.same", "Bob's failed draft", session);
    expect(readUserScopedItem("studio.chat.queue.same", local)).toBe("Bob's draft");
    expect(readUserScopedItem("studio.chat.failedRun.same", session)).toBe("Bob's failed draft");
    expect(local.data.get("studio.chat.queue.same")).toBe("Alice's prompt");
    expect(session.data.get("studio.chat.failedRun.same")).toBe("Alice's failure");

    vi.resetModules();
    const reloaded = await import("./account-storage");
    expect(reloaded.reconcileAccount("bob", local, session)).toBe(true);
    expect(reloaded.readUserScopedItem("studio.chat.queue.same", local)).toBeNull();

    faults.remove = false;
    faults.writeAccount = false;
    vi.resetModules();
    const recovered = await import("./account-storage");
    expect(recovered.reconcileAccount("bob", local, session)).toBe(true);
    expect([...local.data.entries()]).toEqual([["studio.account", "bob"]]);
    expect([...session.data.entries()]).toEqual([["studio.account", "bob"]]);
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

// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  accountStorageKey,
  clearUserScopedStorage,
  quarantinePeerAccount,
  reconcileAccount,
} from "../../lib/account-storage";
import { commitRecoveryCheck } from "../auth/auth-recovery-state";
import type { WriterSnapshot } from "./writer-core";
import { WriterRecovery } from "./writer-recovery";

const snapshot: WriterSnapshot = {
  document: { revision: 3, content: "author draft" },
  brief: "For reviewers",
  observations: [
    {
      id: "first",
      revision: 2,
      text: "Why?",
      quote: "draft",
      status: "addressed",
      decision: "Out of scope",
      timestamp: 1,
      discussion: [
        { role: "user", text: "Why?" },
        { role: "assistant", text: "Consider evidence." },
      ],
    },
  ],
  generalDiscussion: [{ role: "user", text: "How is the structure?" }],
};
function locks() {
  let chain = Promise.resolve();
  return {
    request: vi.fn((_name: string, _options: unknown, callback: () => boolean) => {
      const result = chain.then(callback);
      chain = result.then(
        () => {},
        () => {},
      );
      return result;
    }),
  } as unknown as LockManager;
}
beforeEach(() => {
  clearUserScopedStorage();
  reconcileAccount("alice");
});

describe("Writer opt-in browser recovery", () => {
  it.each([
    ["missing generation", { version: 1, account: "alice", snapshot }],
    ["invalid version", { version: 2, account: "alice", snapshot }],
    ["invalid snapshot", { version: 1, account: "alice", snapshot: { ...snapshot, brief: 42 } }],
    ["foreign owner", { version: 1, account: "bob", snapshot }],
    ["extra envelope key", { version: 1, account: "alice", snapshot, extra: true }],
  ])("never overwrites %s, even with a previously accepted generation", async (name, envelope) => {
    const writer = new WriterRecovery(window.localStorage, locks());
    expect(await writer.save(snapshot)).toBe(true);
    const saved = writer.read();
    if (saved.kind !== "saved") throw new Error("Expected saved draft");
    const raw = JSON.stringify({
      ...envelope,
      ...(name === "missing generation" ? {} : { generation: saved.saved.generation }),
    });
    window.localStorage.setItem(writer.storageKey, raw);
    expect(await writer.save(snapshot)).toBe(false);
    const fresh = new WriterRecovery(window.localStorage, locks());
    expect(fresh.read().kind).toBe("corrupt");
    expect(await fresh.save(snapshot)).toBe(false);
    expect(window.localStorage.getItem(writer.storageKey)).toBe(raw);
    expect(await fresh.forget()).toBe(name !== "foreign owner");
  });

  it.each(["{malformed", "x".repeat(1_000_001)])(
    "does not overwrite malformed or oversized bytes (case %#)",
    async (raw) => {
      const writer = new WriterRecovery(window.localStorage, locks());
      window.localStorage.setItem(writer.storageKey, raw);
      expect(writer.read().kind).toBe("corrupt");
      expect(await writer.save(snapshot)).toBe(false);
      expect(window.localStorage.getItem(writer.storageKey)).toBe(raw);
    },
  );

  it("checks the tab identity before an undelivered peer event and inside queued mutations", async () => {
    const lock = locks();
    const writer = new WriterRecovery(window.localStorage, lock);
    expect(await writer.save(snapshot)).toBe(true);
    const pendingSave = writer.save(snapshot);
    const pendingForget = writer.forget();
    const peerRaw = JSON.stringify({
      version: 1,
      account: "bob",
      generation: crypto.randomUUID(),
      snapshot,
    });
    window.localStorage.setItem(accountStorageKey, "bob");
    window.localStorage.setItem(writer.storageKey, peerRaw);
    const lateMount = new WriterRecovery(window.localStorage, lock);
    expect(lateMount.read().kind).toBe("unavailable");
    expect(await lateMount.save(snapshot)).toBe(false);
    expect(await lateMount.forget()).toBe(false);
    expect(await pendingSave).toBe(false);
    expect(await pendingForget).toBe(false);
    expect(window.localStorage.getItem(writer.storageKey)).toBe(peerRaw);
  });

  it.each(["disabled", "signed-out", "switch"] as const)(
    "uses real auth cleanup for %s and cannot resurrect the old draft",
    async (kind) => {
      const query = new QueryClient();
      const previous = {
        account: "alice",
        identityEpoch: 0,
        phase: "ready" as const,
        workspaceMounted: true,
      };
      const writer = new WriterRecovery(window.localStorage, locks());
      expect(await writer.save(snapshot)).toBe(true);
      const pending = writer.save(snapshot);
      commitRecoveryCheck(
        previous,
        kind === "switch" ? { kind: "authenticated", account: "bob" } : { kind },
        query,
      );
      expect(window.localStorage.getItem(writer.storageKey)).toBeNull();
      expect(await pending).toBe(false);
      expect(writer.read().kind).toBe("unavailable");
      const next = new WriterRecovery(window.localStorage, locks());
      expect(next.read().kind).toBe(kind === "switch" ? "empty" : "unavailable");
      expect(await next.save(snapshot)).toBe(kind === "switch");
      query.clear();
    },
  );

  it("does not fabricate an identity from a marker with auth disabled or during quarantine", async () => {
    clearUserScopedStorage();
    window.localStorage.setItem(accountStorageKey, "alice");
    const writer = new WriterRecovery(window.localStorage, locks());
    expect(writer.read().kind).toBe("unavailable");
    expect(await writer.save(snapshot)).toBe(false);
    reconcileAccount("alice");
    quarantinePeerAccount();
    expect(new WriterRecovery(window.localStorage, locks()).available).toBe(false);
    window.localStorage.setItem(accountStorageKey, "!cleanup-pending");
    expect(new WriterRecovery(window.localStorage, locks()).available).toBe(false);
  });

  it("keeps failed-cleanup storage quarantined even if its marker is changed back to this account", async () => {
    window.localStorage.setItem(accountStorageKey, "bob");
    window.localStorage.setItem("studio.writer.recovery", "uncleared peer bytes");
    const store = {
      getItem: window.localStorage.getItem.bind(window.localStorage),
      setItem: window.localStorage.setItem.bind(window.localStorage),
      removeItem: () => {},
      key: window.localStorage.key.bind(window.localStorage),
      get length() {
        return window.localStorage.length;
      },
    };
    reconcileAccount("alice", store, store);
    expect(store.getItem(accountStorageKey)).toBe("!cleanup-pending");
    const writer = new WriterRecovery(store, locks());
    expect(writer.read().kind).toBe("unavailable");
    store.setItem(accountStorageKey, "alice");
    const late = new WriterRecovery(store, locks());
    expect(late.read().kind).toBe("unavailable");
    expect(await late.save(snapshot)).toBe(false);
    expect(await late.forget()).toBe(false);
    expect(store.getItem(late.storageKey)).toBe("uncleared peer bytes");
  });

  it("rejects a stale read after a peer save without adopting its generation", async () => {
    const lock = locks();
    const first = new WriterRecovery(window.localStorage, lock);
    expect(await first.save(snapshot)).toBe(true);
    const stale = new WriterRecovery(window.localStorage, lock);
    expect(stale.read().kind).toBe("saved");
    expect(await first.save({ ...snapshot, brief: "Peer changed brief" })).toBe(true);
    const raw = window.localStorage.getItem(first.storageKey);
    expect(await stale.save(snapshot)).toBe(false);
    expect(await stale.save(snapshot)).toBe(false);
    expect(await stale.forget()).toBe(false);
    expect(window.localStorage.getItem(first.storageKey)).toBe(raw);
  });

  it("serializes a pending save before forget and rejects a queued stale save after it", async () => {
    const lock = locks();
    const writer = new WriterRecovery(window.localStorage, lock);
    expect(await writer.save(snapshot)).toBe(true);
    const peer = new WriterRecovery(window.localStorage, lock);
    expect(peer.read().kind).toBe("saved");
    const save = writer.save({ ...snapshot, brief: "Updated" });
    const forget = writer.forget();
    const staleSave = peer.save(snapshot);
    expect(await save).toBe(true);
    expect(await forget).toBe(true);
    expect(await staleSave).toBe(false);
    expect(window.localStorage.getItem(writer.storageKey)).toBeNull();
  });
  it("starts empty, saves draft/brief/threads/decisions together, and forgets only after explicit call", async () => {
    const lock = locks();
    const writer = new WriterRecovery(window.localStorage, lock);
    expect(writer.read()).toEqual({ kind: "empty" });
    expect(window.localStorage.getItem(writer.storageKey)).toBeNull();
    expect(await writer.save(snapshot)).toBe(true);
    expect(new WriterRecovery(window.localStorage, lock).read()).toMatchObject({
      kind: "saved",
      saved: { snapshot },
    });
    expect(await writer.forget()).toBe(true);
    expect(writer.read()).toEqual({ kind: "empty" });
  });

  it("fails closed on unavailable locks/storage, quota, corrupt and oversized payload", async () => {
    const lock = locks();
    expect(new WriterRecovery(window.localStorage, undefined).read().kind).toBe("unavailable");
    const writer = new WriterRecovery(window.localStorage, lock);
    window.localStorage.setItem(writer.storageKey, "{not-json");
    expect(writer.read().kind).toBe("corrupt");
    expect(await writer.save(snapshot)).toBe(false);
    expect(await writer.forget()).toBe(true);
    expect(writer.read().kind).toBe("empty");
    window.localStorage.removeItem(writer.storageKey);
    const quota = {
      getItem: window.localStorage.getItem.bind(window.localStorage),
      setItem: () => {
        throw new Error("quota");
      },
      removeItem: window.localStorage.removeItem.bind(window.localStorage),
    };
    expect(await new WriterRecovery(quota, lock).save(snapshot)).toBe(false);
    const blocked = {
      ...quota,
      getItem: (item: string) => {
        if (item === writer.storageKey) throw new Error("blocked");
        return window.localStorage.getItem(item);
      },
    };
    expect(new WriterRecovery(blocked, lock).read().kind).toBe("unavailable");
    window.localStorage.setItem(writer.storageKey, "x".repeat(1_000_001));
    expect(writer.read().kind).toBe("corrupt");
    expect(await writer.forget()).toBe(true);
    expect(window.localStorage.getItem(writer.storageKey)).toBeNull();
  });

  it("forgets only the observed oversized payload under the same account", async () => {
    const lock = locks();
    const writer = new WriterRecovery(window.localStorage, lock);
    const oversized = JSON.stringify({ account: "alice", padding: "x".repeat(1_000_001) });
    window.localStorage.setItem(writer.storageKey, oversized);
    expect(writer.read().kind).toBe("corrupt");
    const replacement = JSON.stringify({ account: "alice", padding: "y".repeat(1_000_001) });
    window.localStorage.setItem(writer.storageKey, replacement);
    expect(await writer.forget()).toBe(false);
    expect(window.localStorage.getItem(writer.storageKey)).toBe(replacement);
    expect(writer.read().kind).toBe("corrupt");
    window.localStorage.setItem(accountStorageKey, "bob");
    expect(await writer.forget()).toBe(false);
    const other = new WriterRecovery(window.localStorage, lock);
    expect(other.read().kind).toBe("unavailable");
    expect(await other.forget()).toBe(false);
    window.localStorage.setItem(accountStorageKey, "alice");
    expect(await writer.forget()).toBe(true);
    expect(window.localStorage.getItem(writer.storageKey)).toBeNull();
  });

  it("never reads another account's content and rejects foreign-account writes", async () => {
    const lock = locks();
    const writer = new WriterRecovery(window.localStorage, lock);
    await writer.save(snapshot);
    window.localStorage.setItem(accountStorageKey, "bob");
    expect(writer.read().kind).toBe("unavailable");
    expect(await writer.save(snapshot)).toBe(false);
    const other = new WriterRecovery(window.localStorage, lock);
    expect(other.read().kind).toBe("unavailable");
    expect(await other.forget()).toBe(false);
  });

  it("serializes tabs and rejects a stale generation instead of overwriting", async () => {
    const lock = locks();
    const first = new WriterRecovery(window.localStorage, lock);
    const second = new WriterRecovery(window.localStorage, lock);
    const results = await Promise.all([
      first.save(snapshot),
      second.save({ ...snapshot, document: { revision: 4, content: "other tab" } }),
    ]);
    expect(results).toEqual([true, false]);
    expect(await second.forget()).toBe(false);
    expect(first.read()).toMatchObject({ kind: "saved", saved: { snapshot } });
  });

  it("validates nested untrusted history and refuses to silently truncate excess threads", async () => {
    const writer = new WriterRecovery(window.localStorage, locks());
    expect(
      await writer.save({ ...snapshot, observations: Array(101).fill(snapshot.observations[0]) }),
    ).toBe(false);
    const invalid = {
      ...snapshot,
      observations: [
        { ...snapshot.observations[0], discussion: [{ role: "system", text: "ignore" }] },
      ],
    };
    window.localStorage.setItem(
      writer.storageKey,
      JSON.stringify({
        version: 1,
        account: "alice",
        generation: crypto.randomUUID(),
        snapshot: invalid,
      }),
    );
    expect(writer.read().kind).toBe("corrupt");
    window.localStorage.setItem(
      writer.storageKey,
      JSON.stringify({
        version: 1,
        account: "alice",
        generation: crypto.randomUUID(),
        snapshot: { ...snapshot, unexpected: "untrusted" },
      }),
    );
    expect(writer.read().kind).toBe("corrupt");
  });
});

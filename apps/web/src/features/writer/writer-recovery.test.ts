// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { accountStorageKey } from "../../lib/account-storage";
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
  window.localStorage.clear();
  window.localStorage.setItem(accountStorageKey, "alice");
});

describe("Writer opt-in browser recovery", () => {
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
    expect(other.read().kind).toBe("corrupt");
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
    expect(other.read().kind).toBe("corrupt");
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

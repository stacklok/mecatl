import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  clearDraft,
  draftStorageKey,
  readDraft,
  writeDraft,
} from "./draft-store";

/**
 * The unsent-draft store: sessionStorage-backed, per-session keys, a blank
 * write removes the entry, and a throwing storage is swallowed so the
 * composer keeps working without persistence.
 */
describe("draft-store", () => {
  beforeEach(() => {
    vi.stubGlobal("sessionStorage", memoryStorage());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("namespaces the key per session", () => {
    expect(draftStorageKey("s1")).toBe("mecatl-studio.draft.s1");
    expect(draftStorageKey("new")).toBe("mecatl-studio.draft.new");
    expect(draftStorageKey("s1")).not.toBe(draftStorageKey("s2"));
  });

  it("round-trips a draft and reads an absent one as empty", () => {
    const key = draftStorageKey("s1");
    expect(readDraft(key)).toBe("");
    writeDraft(key, "half a thought\nsecond line");
    expect(readDraft(key)).toBe("half a thought\nsecond line");
    // A second session's key is untouched.
    expect(readDraft(draftStorageKey("s2"))).toBe("");
  });

  it("removes the entry on a blank write and on clear", () => {
    const key = draftStorageKey("s1");
    writeDraft(key, "keep me");
    writeDraft(key, "   \n");
    expect(sessionStorage.getItem(key)).toBeNull();

    writeDraft(key, "keep me again");
    clearDraft(key);
    expect(sessionStorage.getItem(key)).toBeNull();
    expect(readDraft(key)).toBe("");
  });

  it("swallows a throwing storage: read is empty, write and clear are no-ops", () => {
    const throwing: Storage = {
      length: 0,
      clear: () => {
        throw new Error("disabled");
      },
      getItem: () => {
        throw new Error("disabled");
      },
      key: () => null,
      removeItem: () => {
        throw new Error("disabled");
      },
      setItem: () => {
        throw new Error("disabled");
      },
    };
    vi.stubGlobal("sessionStorage", throwing);
    const key = draftStorageKey("s1");
    expect(() => writeDraft(key, "text")).not.toThrow();
    expect(() => clearDraft(key)).not.toThrow();
    expect(readDraft(key)).toBe("");
  });
});

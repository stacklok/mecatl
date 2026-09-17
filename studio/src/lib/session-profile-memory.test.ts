import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  MAX_REMEMBERED,
  recallSessionProfile,
  rememberSessionProfile,
} from "./session-profile-memory";

/**
 * The browser-local record of which tool profile a Studio-minted chat was
 * created with: the daemon never reports `profile` back, so this memory is
 * the ONLY source for a live chat's display-only Tools line. Unknown is
 * null, never a default; the store is bounded and fail-soft. A real Storage
 * is stubbed per test (the global afterEach unstubs it).
 */

const KEY = "mecatl-studio.session-tool-profiles";

describe("session profile memory", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("recalls the profile a chat was created with, and null for an unknown chat", () => {
    rememberSessionProfile("s-nofs", "no-fs");
    rememberSessionProfile("s-all", "");
    expect(recallSessionProfile("s-nofs")).toBe("no-fs");
    // A remembered default is KNOWN (""), distinct from unknown (null).
    expect(recallSessionProfile("s-all")).toBe("");
    expect(recallSessionProfile("s-tui")).toBeNull();
    expect(recallSessionProfile("")).toBeNull();
  });

  it("overwrites a re-remembered id instead of duplicating it", () => {
    rememberSessionProfile("s1", "no-fs");
    rememberSessionProfile("s1", "");
    expect(recallSessionProfile("s1")).toBe("");
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toHaveLength(
      1,
    );
  });

  it("keeps only the newest MAX_REMEMBERED ids", () => {
    for (let i = 0; i < MAX_REMEMBERED + 5; i++) {
      rememberSessionProfile(`s-${i}`, "no-fs");
    }
    expect(recallSessionProfile("s-0")).toBeNull();
    expect(recallSessionProfile("s-4")).toBeNull();
    expect(recallSessionProfile("s-5")).toBe("no-fs");
    expect(recallSessionProfile(`s-${MAX_REMEMBERED + 4}`)).toBe("no-fs");
  });

  it("reads junk in storage as unknown and narrows odd profile values", () => {
    window.localStorage.setItem(KEY, "not json");
    expect(recallSessionProfile("s1")).toBeNull();
    window.localStorage.setItem(
      KEY,
      JSON.stringify([["s1", "NO-FS"], ["s2", "no-fs"], "bogus", [3, "no-fs"]]),
    );
    expect(recallSessionProfile("s1")).toBe("");
    expect(recallSessionProfile("s2")).toBe("no-fs");
    // Junk is dropped on the next write rather than preserved.
    rememberSessionProfile("s3", "");
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual([
      ["s1", ""],
      ["s2", "no-fs"],
      ["s3", ""],
    ]);
  });

  it("is a no-op when storage throws, and reads unknown when it is missing", () => {
    const broken = memoryStorage();
    broken.setItem = () => {
      throw new Error("quota");
    };
    broken.getItem = () => {
      throw new Error("disabled");
    };
    vi.stubGlobal("localStorage", broken);
    expect(() => rememberSessionProfile("s1", "no-fs")).not.toThrow();
    expect(recallSessionProfile("s1")).toBeNull();

    vi.stubGlobal("localStorage", undefined);
    expect(() => rememberSessionProfile("s1", "no-fs")).not.toThrow();
    expect(recallSessionProfile("s1")).toBeNull();
  });
});

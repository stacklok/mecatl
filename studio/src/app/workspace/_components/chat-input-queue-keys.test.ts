import { describe, expect, it } from "vitest";
import { resolveEmptyComposerKey } from "./chat-input";

/**
 * The empty-composer queue gestures, tested through the pure decision table
 * the composer's capture-phase keydown handler calls (driving the TipTap
 * editor in jsdom is impractical — see chat-input.test.tsx). The handler
 * only consults the table once it has established the composer is empty.
 */
describe("resolveEmptyComposerKey", () => {
  const resolve = (
    key: string,
    overrides: Partial<{
      queuedCount: number;
      isStreaming: boolean;
      menuOpen: boolean;
    }> = {},
  ) =>
    resolveEmptyComposerKey({
      key,
      queuedCount: 2,
      isStreaming: false,
      menuOpen: false,
      ...overrides,
    });

  it("Enter on an idle composer with a held queue resumes it", () => {
    expect(resolve("Enter")).toBe("resume");
  });

  it("Enter while streaming does nothing to the queue (Enter keeps its queue/steer meaning)", () => {
    expect(resolve("Enter", { isStreaming: true })).toBeNull();
  });

  it("ArrowUp pulls the queue back for editing whether or not a run is live", () => {
    expect(resolve("ArrowUp")).toBe("edit");
    expect(resolve("ArrowUp", { isStreaming: true })).toBe("edit");
  });

  it("Escape on an idle composer clears the held queue", () => {
    expect(resolve("Escape")).toBe("clear");
  });

  it("Escape while streaming is left to the run-cancel shortcut", () => {
    expect(resolve("Escape", { isStreaming: true })).toBeNull();
  });

  it("does nothing with an open autocomplete menu or an empty queue", () => {
    expect(resolve("Enter", { menuOpen: true })).toBeNull();
    expect(resolve("ArrowUp", { menuOpen: true })).toBeNull();
    expect(resolve("Escape", { menuOpen: true })).toBeNull();
    expect(resolve("Enter", { queuedCount: 0 })).toBeNull();
    expect(resolve("ArrowUp", { queuedCount: 0 })).toBeNull();
    expect(resolve("Escape", { queuedCount: 0 })).toBeNull();
  });

  it("ignores every other key", () => {
    expect(resolve("ArrowDown")).toBeNull();
    expect(resolve("a")).toBeNull();
    expect(resolve("Tab")).toBeNull();
  });
});

import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  APP_TITLE,
  clampTitle,
  composeDocumentTitle,
  TITLE_MAX_CHARS,
  useDocumentTitle,
} from "./document-title";

/**
 * The tab title: chat title first, a static status word second, the app name
 * last — and a hook that writes it only on change and hands the tab back to
 * the route metadata on unmount without clobbering the next route's title.
 */
describe("composeDocumentTitle", () => {
  it("leads with the chat title and trails with the phase word", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "Fix the flaky scheduler test",
        phase: "running",
        connection: "connected",
      }),
    ).toBe("Fix the flaky scheduler test — Working · Mecatl Studio");
    expect(
      composeDocumentTitle({
        chatTitle: "Fix the flaky scheduler test",
        phase: "awaiting",
        connection: "connected",
      }),
    ).toBe("Fix the flaky scheduler test — ⚠ Approval · Mecatl Studio");
  });

  it("an idle chat shows just the title and the app name", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "Fix the flaky scheduler test",
        phase: "idle",
        connection: "connected",
      }),
    ).toBe("Fix the flaky scheduler test — Mecatl Studio");
  });

  it("a status word without a title reads as word · app", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "",
        phase: "idle",
        connection: "offline",
      }),
    ).toBe("Offline · Mecatl Studio");
    expect(
      composeDocumentTitle({
        chatTitle: "   ",
        phase: "running",
        connection: "connected",
      }),
    ).toBe("Working · Mecatl Studio");
  });

  it("nothing to say is the bare app name, matching the root metadata", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "",
        phase: "idle",
        connection: "connected",
      }),
    ).toBe(APP_TITLE);
    expect(APP_TITLE).toBe("Mecatl Studio");
  });

  it("the connection state wins over the phase", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "Deploy",
        phase: "running",
        connection: "offline",
      }),
    ).toBe("Deploy — Offline · Mecatl Studio");
    expect(
      composeDocumentTitle({
        chatTitle: "Deploy",
        phase: "awaiting",
        connection: "connecting",
      }),
    ).toBe("Deploy — Connecting · Mecatl Studio");
  });

  it("truncates a long title to 40 characters with an ellipsis", () => {
    const long = "x".repeat(TITLE_MAX_CHARS + 1);
    const result = composeDocumentTitle({
      chatTitle: long,
      phase: "idle",
      connection: "connected",
    });
    expect(result).toBe(`${"x".repeat(TITLE_MAX_CHARS - 1)}… — Mecatl Studio`);
    // Exactly at the cap is left alone.
    const exact = "y".repeat(TITLE_MAX_CHARS);
    expect(
      composeDocumentTitle({
        chatTitle: exact,
        phase: "idle",
        connection: "connected",
      }),
    ).toBe(`${exact} — Mecatl Studio`);
  });

  it("strips control characters and collapses newlines to a space", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "Fix[31m the flaky\n\nscheduler\ttest  ",
        phase: "idle",
        connection: "connected",
      }),
    ).toBe("Fix[31m the flaky scheduler test — Mecatl Studio");
  });

  it("a control-only title falls back to the app name", () => {
    expect(
      composeDocumentTitle({
        chatTitle: "\n",
        phase: "idle",
        connection: "connected",
      }),
    ).toBe(APP_TITLE);
  });
});

describe("clampTitle", () => {
  it("counts code points, never splitting an emoji or CJK title", () => {
    const emoji = "🚀".repeat(TITLE_MAX_CHARS + 5);
    const clamped = clampTitle(emoji);
    expect(Array.from(clamped)).toHaveLength(TITLE_MAX_CHARS);
    expect(clamped.endsWith("…")).toBe(true);
    expect(clamped.startsWith("🚀".repeat(TITLE_MAX_CHARS - 1))).toBe(true);
  });

  it("keeps a short clean title byte-identical", () => {
    expect(clampTitle("Rename the CLI flags")).toBe("Rename the CLI flags");
  });
});

describe("useDocumentTitle", () => {
  afterEach(() => {
    document.title = "";
    vi.restoreAllMocks();
  });

  it("sets document.title on mount", () => {
    document.title = "Mecatl Studio";
    renderHook(() => useDocumentTitle("Deploy — Working · Mecatl Studio"));
    expect(document.title).toBe("Deploy — Working · Mecatl Studio");
  });

  it("updates when the title changes", () => {
    const { rerender } = renderHook(
      ({ title }: { title: string }) => useDocumentTitle(title),
      { initialProps: { title: "Deploy — Working · Mecatl Studio" } },
    );
    rerender({ title: "Deploy — Mecatl Studio" });
    expect(document.title).toBe("Deploy — Mecatl Studio");
  });

  it("does not write when the title is unchanged", () => {
    document.title = "Deploy — Mecatl Studio";
    const setter = vi.spyOn(Document.prototype, "title", "set");
    const { rerender } = renderHook(
      ({ title }: { title: string }) => useDocumentTitle(title),
      { initialProps: { title: "Deploy — Mecatl Studio" } },
    );
    rerender({ title: "Deploy — Mecatl Studio" });
    expect(setter).not.toHaveBeenCalled();
  });

  it("restores the previous title on unmount", () => {
    document.title = "Mecatl Studio";
    const { unmount } = renderHook(() =>
      useDocumentTitle("Deploy — Working · Mecatl Studio"),
    );
    expect(document.title).toBe("Deploy — Working · Mecatl Studio");
    unmount();
    expect(document.title).toBe("Mecatl Studio");
  });

  it("leaves a title another writer set before the cleanup alone", () => {
    // React 19 hoists route <title> metadata and applies the next route's
    // title in the same commit, before this hook's passive cleanup runs.
    document.title = "Mecatl Studio";
    const { unmount } = renderHook(() =>
      useDocumentTitle("Deploy — Working · Mecatl Studio"),
    );
    document.title = "Keyboard shortcuts — Mecatl Studio";
    unmount();
    expect(document.title).toBe("Keyboard shortcuts — Mecatl Studio");
  });
});

import { afterEach, describe, expect, it, vi } from "vitest";
import { PENDING_DRAFT_KEY, takePendingDraft } from "./pending-draft";

/**
 * The cross-route composer handoff: a stored text is taken exactly once
 * (take clears), an empty value reads as nothing pending, and a browser
 * without usable storage degrades to "nothing pending" instead of throwing.
 */

afterEach(() => {
  // Restore the accessor spy FIRST — clearing through a throwing getter
  // would fail the teardown itself.
  vi.restoreAllMocks();
  window.sessionStorage.clear();
});

describe("pending draft", () => {
  it("takes the stored text exactly once", () => {
    window.sessionStorage.setItem(
      PENDING_DRAFT_KEY,
      "Mecatl diagnostics:\nplatform: macOS",
    );
    expect(takePendingDraft()).toBe("Mecatl diagnostics:\nplatform: macOS");
    expect(takePendingDraft()).toBeNull();
    expect(window.sessionStorage.getItem(PENDING_DRAFT_KEY)).toBeNull();
  });

  it("reads null when nothing is pending", () => {
    expect(takePendingDraft()).toBeNull();
  });

  it("reads an empty stored value as nothing pending", () => {
    window.sessionStorage.setItem(PENDING_DRAFT_KEY, "");
    expect(takePendingDraft()).toBeNull();
    expect(window.sessionStorage.getItem(PENDING_DRAFT_KEY)).toBeNull();
  });

  it("degrades when storage is unusable", () => {
    // A browser with site data blocked throws on the accessor itself.
    vi.spyOn(window, "sessionStorage", "get").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    expect(takePendingDraft()).toBeNull();
  });
});

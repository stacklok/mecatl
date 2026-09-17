import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useBeforeUnloadGuard } from "./use-beforeunload-guard";

/**
 * The leave guard registers a `beforeunload` canceller only while active:
 * an unsent draft or a run this tab drives gets the browser's confirm; an
 * idle tab, or an unmounted workspace, closes silently.
 */
function fireBeforeUnload(): boolean {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

describe("useBeforeUnloadGuard", () => {
  it("cancels beforeunload while active", () => {
    renderHook(() => useBeforeUnloadGuard(true));
    expect(fireBeforeUnload()).toBe(true);
  });

  it("registers nothing while inactive", () => {
    renderHook(() => useBeforeUnloadGuard(false));
    expect(fireBeforeUnload()).toBe(false);
  });

  it("follows the flag: arms when it turns on, disarms when it turns off", () => {
    const { rerender } = renderHook(
      ({ active }: { active: boolean }) => useBeforeUnloadGuard(active),
      { initialProps: { active: false } },
    );
    expect(fireBeforeUnload()).toBe(false);
    rerender({ active: true });
    expect(fireBeforeUnload()).toBe(true);
    rerender({ active: false });
    expect(fireBeforeUnload()).toBe(false);
  });

  it("removes the listener on unmount", () => {
    const { unmount } = renderHook(() => useBeforeUnloadGuard(true));
    expect(fireBeforeUnload()).toBe(true);
    unmount();
    expect(fireBeforeUnload()).toBe(false);
  });
});

import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useExpandDetails } from "@/lib/profile-preferences";
import { memoryStorage } from "@/test/memory-storage";
import { useDetailsOpen } from "./use-details-open";

/**
 * One disclosure's open state follows the global Expand details preference
 * (so the `chat.expandDetails` shortcut opens or closes every disclosure at
 * once) until the reader toggles it by hand, and re-follows on the next
 * global flip.
 */
describe("useDetailsOpen", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("starts collapsed by default and toggles locally", () => {
    const { result } = renderHook(() => useDetailsOpen());
    expect(result.current[0]).toBe(false);
    act(() => result.current[1]());
    expect(result.current[0]).toBe(true);
    act(() => result.current[1]());
    expect(result.current[0]).toBe(false);
  });

  it("honours a caller's forceOpen", () => {
    const { result } = renderHook(() => useDetailsOpen(true));
    expect(result.current[0]).toBe(true);
    act(() => result.current[1]());
    expect(result.current[0]).toBe(false);
  });

  it("starts expanded when the global preference is on", () => {
    window.localStorage.setItem("mecatl-studio.expand-details", "1");
    const { result } = renderHook(() => useDetailsOpen());
    expect(result.current[0]).toBe(true);
  });

  it("follows a global flip even after a local toggle, then toggles locally again", () => {
    const pref = renderHook(() => useExpandDetails());
    const first = renderHook(() => useDetailsOpen());
    const second = renderHook(() => useDetailsOpen());
    // The reader opens one by hand; the other stays closed.
    act(() => first.result.current[1]());
    expect(first.result.current[0]).toBe(true);
    expect(second.result.current[0]).toBe(false);
    // The shortcut expands everything: the hand-opened one stays open, the
    // closed one opens.
    act(() => pref.result.current.setExpandDetails(true));
    expect(first.result.current[0]).toBe(true);
    expect(second.result.current[0]).toBe(true);
    // A hand close on one, then the shortcut collapses everything.
    act(() => second.result.current[1]());
    expect(second.result.current[0]).toBe(false);
    act(() => pref.result.current.setExpandDetails(false));
    expect(first.result.current[0]).toBe(false);
    expect(second.result.current[0]).toBe(false);
    // Local toggling still works after the global flip.
    act(() => second.result.current[1]());
    expect(second.result.current[0]).toBe(true);
  });
});

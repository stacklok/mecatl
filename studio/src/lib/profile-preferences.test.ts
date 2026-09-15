import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useShowToolCalls } from "./profile-preferences";

const KEY = "mecatl-studio.show-tool-calls";

/** This vitest environment ships a method-less localStorage shim (Node's
 *  --localstorage-file stub shadows jsdom's), so storage tests stub a real
 *  in-memory Storage; the global afterEach unstubs it. */
function memoryStorage(): Storage {
  let store = new Map<string, string>();
  return {
    get length() {
      return store.size;
    },
    clear: () => {
      store = new Map();
    },
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => [...store.keys()][index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
  };
}

/**
 * The Show Tools preference is GLOBAL and persisted: it must round-trip
 * through localStorage (survive a "reload" = a fresh hook mount), keep two
 * simultaneously mounted instances in sync (the chat view and the thread
 * panel both show the toggle), and store nothing while off.
 */
describe("useShowToolCalls", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("defaults off and stores nothing until enabled", () => {
    const { result } = renderHook(() => useShowToolCalls());
    expect(result.current.showToolCalls).toBe(false);
    expect(window.localStorage.getItem(KEY)).toBeNull();
  });

  it("round-trips through storage across mounts", () => {
    const first = renderHook(() => useShowToolCalls());
    act(() => first.result.current.setShowToolCalls(true));
    expect(window.localStorage.getItem(KEY)).toBe("1");
    first.unmount();

    // A fresh mount (a reload, a different session's chat) reads it back.
    const second = renderHook(() => useShowToolCalls());
    expect(second.result.current.showToolCalls).toBe(true);

    // Turning it off removes the key rather than storing "0" forever.
    act(() => second.result.current.setShowToolCalls(false));
    expect(window.localStorage.getItem(KEY)).toBeNull();
    expect(second.result.current.showToolCalls).toBe(false);
  });

  it("keeps two mounted instances in sync (chat menu + thread panel)", () => {
    const chat = renderHook(() => useShowToolCalls());
    const thread = renderHook(() => useShowToolCalls());
    act(() => thread.result.current.setShowToolCalls(true));
    expect(chat.result.current.showToolCalls).toBe(true);
    expect(thread.result.current.showToolCalls).toBe(true);
  });
});

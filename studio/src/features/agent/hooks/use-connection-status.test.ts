import type { ConnectionStatus } from "@stacklok-oss/mecatl-sdk";
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useConnectionStatus } from "./use-connection-status";

/**
 * The connection-status hook mirrors the SDK client's multicast status store
 * into React: it reads the current snapshot, re-renders on every change, and
 * follows a client reset onto the replacement client's store instead of
 * staying subscribed to the forgotten one.
 */

type Listener = (status: ConnectionStatus) => void;

/** A stand-in for the SDK's ConnectionStatusStore, driven by the test. */
function fakeStore(initial: ConnectionStatus) {
  let snapshot = initial;
  const listeners = new Set<Listener>();
  return {
    getSnapshot: () => snapshot,
    subscribe: vi.fn((listener: Listener) => {
      listeners.add(listener);
      listener(snapshot);
      return () => listeners.delete(listener);
    }),
    set(next: ConnectionStatus) {
      snapshot = next;
      for (const listener of [...listeners]) listener(next);
    },
    listenerCount: () => listeners.size,
  };
}

const sdk = vi.hoisted(() => ({
  store: null as ReturnType<typeof fakeStore> | null,
  resetListeners: new Set<() => void>(),
}));

vi.mock("@/lib/harness/sdk", () => ({
  getHarnessClient: () => ({ status: sdk.store }),
  onHarnessClientReset: (listener: () => void) => {
    sdk.resetListeners.add(listener);
    return () => sdk.resetListeners.delete(listener);
  },
}));

afterEach(() => {
  sdk.store = null;
  sdk.resetListeners.clear();
});

describe("useConnectionStatus", () => {
  it("reads the store's snapshot and re-renders on every change", () => {
    const store = fakeStore("online");
    sdk.store = store;
    const { result } = renderHook(() => useConnectionStatus());
    expect(result.current).toBe("online");

    act(() => store.set("reconnecting"));
    expect(result.current).toBe("reconnecting");

    act(() => store.set("unauthorized"));
    expect(result.current).toBe("unauthorized");
  });

  it("unsubscribes on unmount", () => {
    const store = fakeStore("online");
    sdk.store = store;
    const { unmount } = renderHook(() => useConnectionStatus());
    expect(store.listenerCount()).toBe(1);
    unmount();
    expect(store.listenerCount()).toBe(0);
    expect(sdk.resetListeners.size).toBe(0);
  });

  it("follows a client reset onto the replacement client's store", () => {
    const first = fakeStore("online");
    sdk.store = first;
    const { result } = renderHook(() => useConnectionStatus());
    expect(result.current).toBe("online");

    // resetHarnessClient(): the client is forgotten and a later
    // getHarnessClient() mints a new one with a fresh status store.
    const second = fakeStore("reconnecting");
    act(() => {
      sdk.store = second;
      for (const listener of [...sdk.resetListeners]) listener();
    });
    expect(first.listenerCount()).toBe(0);
    expect(second.listenerCount()).toBe(1);
    expect(result.current).toBe("reconnecting");

    act(() => second.set("online"));
    expect(result.current).toBe("online");
  });
});

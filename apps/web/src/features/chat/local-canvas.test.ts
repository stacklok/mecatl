// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearUserScopedStorage,
  readUserScopedItem,
  reconcileAccount,
} from "../../lib/account-storage";
import { appendCanvasQuote, useLocalCanvas } from "./local-canvas";

beforeEach(() => clearUserScopedStorage());

describe("local canvas", () => {
  it("appends selected text as a markdown quote", () => {
    expect(appendCanvasQuote("# Notes\n", "one\ntwo")).toBe("# Notes\n\n> one\n> two\n");
  });

  it("keeps canvas local and account scoped", () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    reconcileAccount("alice");
    const first = renderHook(({ sessionId }) => useLocalCanvas(sessionId), {
      initialProps: { sessionId: "session/a" },
    });
    act(() => first.result.current.setValue("# Alice notes"));
    first.rerender({ sessionId: "session/a" }); // a normal delivery keeps local state
    expect(first.result.current.value).toBe("# Alice notes");
    expect(readUserScopedItem("studio.chat.canvas.session%2Fa")).toBe("# Alice notes");
    first.unmount();

    const reloaded = renderHook(() => useLocalCanvas("session/a"));
    expect(reloaded.result.current.value).toBe("# Alice notes");
    reloaded.unmount();

    expect(reconcileAccount("bob")).toBe(true);
    const bob = renderHook(() => useLocalCanvas("session/a"));
    expect(bob.result.current.value).toBe("");
    act(() => bob.result.current.setValue("Bob's notes"));
    expect(readUserScopedItem("studio.chat.canvas.session%2Fa")).toBe("Bob's notes");
    expect(fetch).not.toHaveBeenCalled();
    bob.unmount();

    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    const memory = renderHook(() => useLocalCanvas("session/b"));
    act(() => memory.result.current.setValue("In-memory notes"));
    expect(memory.result.current.value).toBe("In-memory notes");
    expect(readUserScopedItem("studio.chat.canvas.session%2Fb")).toBe("In-memory notes");
    expect(fetch).not.toHaveBeenCalled();
    memory.unmount();
    setItem.mockRestore();
    vi.unstubAllGlobals();
  });
});

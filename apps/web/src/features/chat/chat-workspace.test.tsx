// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { takeNextQueuedMessage, useQueuedMessages } from "./chat-queue";
import { controlTarget, drainsQueue, isStaleRunControl } from "./run-stream";

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
});

describe("chat workspace run ownership", () => {
  it("drains only the viewed session queue after settlement", () => {
    const { result, rerender } = renderHook(({ id }) => useQueuedMessages(id), {
      initialProps: { id: "chat/a" },
    });
    act(() => {
      result.current.add("first");
      result.current.add("second");
    });
    expect(result.current.items.map((item) => item.text)).toEqual(["first", "second"]);
    expect(window.localStorage.getItem("studio.chat.queue.chat%2Fa")).toContain("first");

    rerender({ id: "chat-b" });
    expect(result.current.items).toEqual([]);
    expect(drainsQueue({ kind: "settled" }, "chat/a", "chat-b", false)).toBe(false);
    expect(drainsQueue({ kind: "unfollowed" }, "chat/a", "chat/a", false)).toBe(false);
    expect(drainsQueue({ kind: "uncertain" }, "chat/a", "chat/a", false)).toBe(false);

    rerender({ id: "chat/a" });
    expect(result.current.items).toHaveLength(2);
    expect(drainsQueue({ kind: "settled" }, "chat/a", "chat/a", false)).toBe(true);
    act(() => {
      expect(takeNextQueuedMessage("chat/a")?.text).toBe("first");
    });
    expect(result.current.items.map((item) => item.text)).toEqual(["second"]);
  });

  it("addresses steer cancel and retry to the exact run", () => {
    const current = { runId: "run-a", sessionId: "chat-a" };
    expect(controlTarget(current, "chat-a")).toEqual(current);
    expect(controlTarget(current, "chat-b")).toBeUndefined();
    expect(isStaleRunControl({ code: "stale_run_control", status: 409 })).toBe(true);
    expect(isStaleRunControl({ code: "runtime_unavailable", status: 503 })).toBe(false);
  });
});

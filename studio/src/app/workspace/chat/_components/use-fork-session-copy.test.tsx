import { act, renderHook } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "@/lib/harness/sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "@/lib/harness/sdk-test-stub";
import {
  FORK_SOURCE_BUSY,
  FORKED_TOAST,
  useForkSessionCopy,
} from "./use-fork-session-copy";

/**
 * Fork-as-is through the SDK: the hook POSTs the copy-titled fork, hands the
 * new id to the owner BEFORE toasting success (the UI has moved when the
 * toast reads "continuing"), reads a 412 as "wait for the run to finish"
 * with no retry, and offers a Retry action on every other failure.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("useForkSessionCopy", () => {
  it("forks with the copy title, then moves the UI and toasts", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.method === "POST" && request.path === "/v1/sessions/src/fork")
        return jsonResponse(200, { session_id: "fork-1" });
      return undefined;
    });
    const order: string[] = [];
    const onForked = vi.fn(async (id: string) => {
      order.push(`forked:${id}`);
    });
    vi.mocked(toast.success).mockImplementation((message) => {
      order.push(`toast:${String(message)}`);
      return "";
    });
    const { result } = renderHook(() => useForkSessionCopy({ onForked }));
    await act(async () => {
      await result.current("src", "Fix the flaky test");
    });
    expect(requests[0].body).toEqual({ title: "Fix the flaky test (copy)" });
    expect(onForked).toHaveBeenCalledWith("fork-1");
    expect(order).toEqual(["forked:fork-1", `toast:${FORKED_TOAST}`]);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("reads a busy source (412) as wait, with no retry action", async () => {
    stubHarnessFetch(() =>
      problemResponse(412, "failed_precondition", "source session is running"),
    );
    const onForked = vi.fn();
    const { result } = renderHook(() => useForkSessionCopy({ onForked }));
    await act(async () => {
      await result.current("src", "Title");
    });
    expect(onForked).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith(FORK_SOURCE_BUSY);
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("offers Retry on any other failure, and the retry re-forks", async () => {
    let attempts = 0;
    stubHarnessFetch(() => {
      attempts += 1;
      return attempts === 1
        ? problemResponse(500, "internal", "store unavailable")
        : jsonResponse(200, { session_id: "fork-2" });
    });
    const onForked = vi.fn();
    const { result } = renderHook(() => useForkSessionCopy({ onForked }));
    await act(async () => {
      await result.current("src", "Title");
    });
    expect(onForked).not.toHaveBeenCalled();
    const [message, options] = vi.mocked(toast.error).mock.calls[0] as [
      string,
      { action: { label: string; onClick: () => void } },
    ];
    expect(message).toContain("store unavailable");
    expect(options.action.label).toBe("Retry");
    await act(async () => {
      options.action.onClick();
      // The retry is fire-and-forget; let the SDK round-trip settle.
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(onForked).toHaveBeenCalledWith("fork-2");
    expect(toast.success).toHaveBeenCalledWith(FORKED_TOAST);
  });

  it("returns a stable callback across renders", () => {
    const { result, rerender } = renderHook(
      ({ onForked }) => useForkSessionCopy({ onForked }),
      { initialProps: { onForked: vi.fn() } },
    );
    const first = result.current;
    rerender({ onForked: vi.fn() });
    expect(result.current).toBe(first);
  });
});

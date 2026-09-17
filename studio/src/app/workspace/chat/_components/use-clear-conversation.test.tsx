import { act, renderHook, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import { ThreadSourceBusyError } from "@/lib/harness/sessions";
import {
  CLEAR_SOURCE_BUSY,
  CLEAR_UNSUPPORTED,
  CLEARED_TOAST,
  NOTHING_TO_CLEAR,
} from "./clear-conversation";
import {
  type ClearConversationDeps,
  useClearConversation,
} from "./use-clear-conversation";

/**
 * Pins the /clear handoff: the composer is blocked (`clearing`) from the
 * request until the successor is adopted, the scrollback moves ONLY after
 * the daemon answered, the held queue is dropped, a failure keeps the source
 * selected and offers Retry, a daemon without the route says so (no retry),
 * and a second call while one is pending is a no-op.
 */

const harness = vi.hoisted(() => ({ clear: vi.fn() }));
vi.mock("@/lib/harness/sessions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/sessions")>()),
  clearHarnessSession: harness.clear,
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeDeps(
  overrides: Partial<ClearConversationDeps> = {},
): ClearConversationDeps {
  return {
    sessionId: "s1",
    onClearQueue: vi.fn(),
    onSessionCleared: vi.fn(),
    ...overrides,
  };
}

const errorToast = toast.error as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => {
  harness.clear.mockReset();
  harness.clear.mockResolvedValue("s2");
});

describe("useClearConversation", () => {
  it("on a draft says there is nothing to clear and calls no daemon", async () => {
    const deps = makeDeps({ sessionId: null });
    const { result } = renderHook(() => useClearConversation(deps));
    await act(() => result.current.clearConversation());
    expect(toast.info).toHaveBeenCalledWith(NOTHING_TO_CLEAR);
    expect(harness.clear).not.toHaveBeenCalled();
    expect(result.current.clearing).toBe(false);
  });

  it("blocks the composer until the successor is adopted, moving only after the daemon answered", async () => {
    const clear = deferred<string>();
    harness.clear.mockReturnValue(clear.promise);
    const adopt = deferred<void>();
    const deps = makeDeps({
      onSessionCleared: vi.fn(() => adopt.promise),
    });
    const { result } = renderHook(() => useClearConversation(deps));

    let done: Promise<void> | undefined;
    act(() => {
      done = result.current.clearConversation();
    });
    expect(result.current.clearing).toBe(true);
    expect(harness.clear).toHaveBeenCalledWith("s1");
    // Nothing moves before the daemon answers.
    expect(deps.onSessionCleared).not.toHaveBeenCalled();
    expect(deps.onClearQueue).not.toHaveBeenCalled();

    await act(async () => {
      clear.resolve("s2");
      await Promise.resolve();
    });
    await waitFor(() =>
      expect(deps.onSessionCleared).toHaveBeenCalledWith("s2"),
    );
    expect(deps.onClearQueue).toHaveBeenCalledTimes(1);
    // Still blocked while the list refreshes and the UI switches.
    expect(result.current.clearing).toBe(true);
    expect(toast.success).not.toHaveBeenCalled();

    await act(async () => {
      adopt.resolve();
      await done;
    });
    expect(result.current.clearing).toBe(false);
    expect(toast.success).toHaveBeenCalledWith(CLEARED_TOAST);
  });

  it("keeps the source on a failure and offers Retry, which runs the handoff again", async () => {
    harness.clear.mockRejectedValueOnce(
      new HarnessApiError(500, "internal", "successor creation failed"),
    );
    const deps = makeDeps();
    const { result } = renderHook(() => useClearConversation(deps));
    await act(() => result.current.clearConversation());
    expect(deps.onSessionCleared).not.toHaveBeenCalled();
    expect(deps.onClearQueue).not.toHaveBeenCalled();
    expect(result.current.clearing).toBe(false);
    expect(errorToast).toHaveBeenCalledWith(
      "successor creation failed",
      expect.objectContaining({
        action: expect.objectContaining({ label: "Retry" }),
      }),
    );
    // The Retry action re-drives the same handoff (the second attempt
    // resolves with the default "s2").
    const options = errorToast.mock.calls[0]?.[1] as {
      action: { onClick: () => void };
    };
    await act(async () => {
      options.action.onClick();
      await Promise.resolve();
    });
    await waitFor(() =>
      expect(deps.onSessionCleared).toHaveBeenCalledWith("s2"),
    );
    expect(harness.clear).toHaveBeenCalledTimes(2);
  });

  it("frames a busy source (412 or the typed busy error) as wait-then-clear, with Retry", async () => {
    harness.clear.mockRejectedValueOnce(
      new HarnessApiError(412, "failed_precondition", "session is running"),
    );
    const { result } = renderHook(() => useClearConversation(makeDeps()));
    await act(() => result.current.clearConversation());
    expect(errorToast).toHaveBeenLastCalledWith(
      CLEAR_SOURCE_BUSY,
      expect.objectContaining({ action: expect.anything() }),
    );

    harness.clear.mockRejectedValueOnce(new ThreadSourceBusyError(""));
    await act(() => result.current.clearConversation());
    expect(errorToast).toHaveBeenLastCalledWith(
      CLEAR_SOURCE_BUSY,
      expect.objectContaining({ action: expect.anything() }),
    );
  });

  it("says the daemon cannot clear sessions on a 404, with no Retry", async () => {
    harness.clear.mockRejectedValueOnce(
      new HarnessApiError(404, "", "not found"),
    );
    const { result } = renderHook(() => useClearConversation(makeDeps()));
    await act(() => result.current.clearConversation());
    expect(errorToast).toHaveBeenLastCalledWith(CLEAR_UNSUPPORTED);
    expect(result.current.clearing).toBe(false);
  });

  it("ignores a second call while one handoff is pending", async () => {
    const clear = deferred<string>();
    harness.clear.mockReturnValue(clear.promise);
    const deps = makeDeps();
    const { result } = renderHook(() => useClearConversation(deps));
    let first: Promise<void> | undefined;
    act(() => {
      first = result.current.clearConversation();
      void result.current.clearConversation();
    });
    expect(harness.clear).toHaveBeenCalledTimes(1);
    await act(async () => {
      clear.resolve("s2");
      await first;
    });
    expect(deps.onSessionCleared).toHaveBeenCalledTimes(1);
  });
});

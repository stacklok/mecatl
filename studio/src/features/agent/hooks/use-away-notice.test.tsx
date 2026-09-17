import { act, renderHook } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type UseAwayNoticeInput, useAwayNotice } from "./use-away-notice";

/**
 * The resume notice's trigger: the tab going hidden records the phase, the
 * tab coming back after MIN_AWAY_MS refreshes the daemon state and then — on
 * the render that carries the refreshed state, never in the visibility
 * handler — composes one toast. `toast` is the global sonner mock from
 * vitest.setup.ts.
 */

const KEPT_RUNNING_AND_FINISHED =
  "While you were away, “Fix it” kept running and finished.";
const STILL_WORKING =
  "“Fix it” is still working — it kept running while you were away.";
const TOAST_OPTIONS = { id: "away-notice", duration: 8_000 };

let visibility: DocumentVisibilityState = "visible";
const T0 = 1_700_000_000_000;

function deferred<T = void>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function baseInput(overrides: Partial<UseAwayNoticeInput> = {}) {
  return {
    phase: "running",
    chatTitle: "Fix it",
    connected: true,
    ...overrides,
  } satisfies UseAwayNoticeInput;
}

async function goHidden(at: number) {
  visibility = "hidden";
  vi.setSystemTime(at);
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

async function comeBack(at: number) {
  visibility = "visible";
  vi.setSystemTime(at);
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(T0);
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibility,
  });
});

afterEach(() => {
  Reflect.deleteProperty(document, "visibilityState");
  vi.useRealTimers();
});

describe("useAwayNotice — the return after a real absence", () => {
  it("refreshes first, then composes from the phase the refresh landed", async () => {
    const refreshed = deferred();
    const refresh = vi.fn(() => refreshed.promise);
    const input = baseInput({ refresh });
    const { rerender } = renderHook((props) => useAwayNotice(props), {
      initialProps: input,
    });

    await goHidden(T0);
    await comeBack(T0 + 30_000);
    // The refresh is forced on return; nothing is said until it settles.
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(toast.info).not.toHaveBeenCalled();

    // The refresh lands the run's terminal state, THEN settles.
    rerender({ ...input, phase: "idle" });
    await act(async () => {
      refreshed.resolve();
    });
    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(toast.info).toHaveBeenCalledWith(
      KEPT_RUNNING_AND_FINISHED,
      TOAST_OPTIONS,
    );
  });

  it("reads the phase at leaving from the hidden-time snapshot, not the current props", async () => {
    const input = baseInput({ phase: "awaiting", refresh: async () => {} });
    const { rerender } = renderHook((props) => useAwayNotice(props), {
      initialProps: input,
    });
    await goHidden(T0);
    rerender({ ...input, phase: "running" });
    await comeBack(T0 + 25_000);
    expect(toast.info).toHaveBeenCalledWith(
      "The approval on “Fix it” was resolved while you were away; it is working again.",
      TOAST_OPTIONS,
    );
  });

  it("composes without a refresh callback (the phase is whatever the props say)", async () => {
    renderHook(() => useAwayNotice(baseInput()));
    await goHidden(T0);
    await comeBack(T0 + 20_000);
    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(toast.info).toHaveBeenCalledWith(STILL_WORKING, TOAST_OPTIONS);
  });

  it("still speaks when the refresh rejects (best-effort refresh)", async () => {
    const refresh = vi.fn(() => Promise.reject(new Error("probe failed")));
    renderHook(() => useAwayNotice(baseInput({ refresh })));
    await goHidden(T0);
    await comeBack(T0 + 30_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(toast.info).toHaveBeenCalledWith(STILL_WORKING, TOAST_OPTIONS);
  });

  it("reports going offline when the refreshed connection is down", async () => {
    const input = baseInput({ refresh: async () => {} });
    const { rerender } = renderHook((props) => useAwayNotice(props), {
      initialProps: input,
    });
    await goHidden(T0);
    rerender({ ...input, connected: false });
    await comeBack(T0 + 30_000);
    expect(toast.info).toHaveBeenCalledWith(
      "Mecatl went offline while you were away.",
      TOAST_OPTIONS,
    );
  });

  it("treats a bfcache restore (persisted pageshow) as the return", async () => {
    renderHook(() => useAwayNotice(baseInput()));
    await goHidden(T0);
    visibility = "visible";
    vi.setSystemTime(T0 + 45_000);
    await act(async () => {
      const event = new Event("pageshow");
      Object.defineProperty(event, "persisted", { value: true });
      window.dispatchEvent(event);
    });
    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(toast.info).toHaveBeenCalledWith(STILL_WORKING, TOAST_OPTIONS);
  });

  it("ignores a fresh-load pageshow (not persisted) — there was no absence", async () => {
    renderHook(() => useAwayNotice(baseInput()));
    await act(async () => {
      const event = new Event("pageshow");
      Object.defineProperty(event, "persisted", { value: false });
      window.dispatchEvent(event);
    });
    expect(toast.info).not.toHaveBeenCalled();
  });
});

describe("useAwayNotice — when it stays quiet", () => {
  it("says nothing for a short absence, and forces no refresh for it", async () => {
    const refresh = vi.fn(async () => {});
    renderHook(() => useAwayNotice(baseInput({ refresh })));
    await goHidden(T0);
    await comeBack(T0 + 5_000);
    expect(refresh).not.toHaveBeenCalled();
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("says nothing when an idle chat stayed idle", async () => {
    renderHook(() => useAwayNotice(baseInput({ phase: "idle" })));
    await goHidden(T0);
    await comeBack(T0 + 60_000);
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("drops a pending return when the tab is hidden again before the refresh settles", async () => {
    const first = deferred();
    const refresh = vi.fn(() => first.promise);
    renderHook(() => useAwayNotice(baseInput({ refresh })));
    await goHidden(T0);
    await comeBack(T0 + 30_000);
    // Gone again before the slow refresh answered.
    await goHidden(T0 + 31_000);
    await act(async () => {
      first.resolve();
    });
    expect(toast.info).not.toHaveBeenCalled();

    // The later return composes from the later snapshot, on its own refresh.
    refresh.mockImplementation(async () => {});
    await comeBack(T0 + 90_000);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(toast.info).toHaveBeenCalledWith(STILL_WORKING, TOAST_OPTIONS);
  });

  it("removes its listeners on unmount", async () => {
    const { unmount } = renderHook(() => useAwayNotice(baseInput()));
    unmount();
    await goHidden(T0);
    await comeBack(T0 + 60_000);
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("does not speak after an unmount that raced the refresh", async () => {
    const refreshed = deferred();
    const { unmount } = renderHook(() =>
      useAwayNotice(baseInput({ refresh: () => refreshed.promise })),
    );
    await goHidden(T0);
    await comeBack(T0 + 60_000);
    unmount();
    await act(async () => {
      refreshed.resolve();
    });
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("is inert when disabled", async () => {
    renderHook(() => useAwayNotice(baseInput({ enabled: false })));
    await goHidden(T0);
    await comeBack(T0 + 60_000);
    expect(toast.info).not.toHaveBeenCalled();
  });
});

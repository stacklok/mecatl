import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  useEnterSendBehavior,
  useExpandDetails,
  useLaunchTarget,
  useShowStarterPrompts,
  useShowToolCalls,
} from "./profile-preferences";

const KEY = "mecatl-studio.show-tool-calls";
const EXPAND_KEY = "mecatl-studio.expand-details";

/**
 * The Expand details preference (the TUI's ctrl+t): GLOBAL and persisted
 * like Show Tools — default off, "1" only while on, two mounted instances
 * (the chat menu and every disclosure) in sync through the shared store.
 */
describe("useExpandDetails", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("defaults to collapsed details and stores nothing", () => {
    const { result } = renderHook(() => useExpandDetails());
    expect(result.current.expandDetails).toBe(false);
    expect(window.localStorage.getItem(EXPAND_KEY)).toBeNull();
  });

  it('persists an on choice as "1" and clears the key when turned off', () => {
    const first = renderHook(() => useExpandDetails());
    act(() => first.result.current.setExpandDetails(true));
    expect(window.localStorage.getItem(EXPAND_KEY)).toBe("1");
    const second = renderHook(() => useExpandDetails());
    expect(second.result.current.expandDetails).toBe(true);
    act(() => second.result.current.setExpandDetails(false));
    expect(window.localStorage.getItem(EXPAND_KEY)).toBeNull();
    expect(first.result.current.expandDetails).toBe(false);
  });

  it("keeps two mounted instances in sync", () => {
    const menu = renderHook(() => useExpandDetails());
    const row = renderHook(() => useExpandDetails());
    act(() => menu.result.current.setExpandDetails(true));
    expect(row.result.current.expandDetails).toBe(true);
    act(() => row.result.current.setExpandDetails(false));
    expect(menu.result.current.expandDetails).toBe(false);
  });
});

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

/**
 * Starter prompts are SHOWN by default (the --no-banner analogue is an
 * opt-out): the key exists only while hidden, and turning them back on
 * removes it.
 */
describe("useShowStarterPrompts", () => {
  const HIDE_KEY = "mecatl-studio.hide-starter-prompts";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("defaults to shown and stores nothing", () => {
    const { result } = renderHook(() => useShowStarterPrompts());
    expect(result.current.show).toBe(true);
    expect(window.localStorage.getItem(HIDE_KEY)).toBeNull();
  });

  it("round-trips hidden across mounts and removes the key when shown again", () => {
    const first = renderHook(() => useShowStarterPrompts());
    act(() => first.result.current.setShow(false));
    expect(first.result.current.show).toBe(false);
    expect(window.localStorage.getItem(HIDE_KEY)).toBe("1");
    first.unmount();

    const second = renderHook(() => useShowStarterPrompts());
    expect(second.result.current.show).toBe(false);

    act(() => second.result.current.setShow(true));
    expect(second.result.current.show).toBe(true);
    expect(window.localStorage.getItem(HIDE_KEY)).toBeNull();
  });
});

/**
 * The Enter preference has THREE values: the two-way queue/steer default
 * pair, and "queue-only" — the client-level never-steer switch (the web form
 * of `mecatui --no-steer`). Only the non-default values are stored; a stale
 * or unknown stored value reads as the "queue" default.
 */
describe("useEnterSendBehavior", () => {
  const KEY = "mecatl-studio.enter-send-behavior";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("defaults to queue and stores nothing for it", () => {
    const { result } = renderHook(() => useEnterSendBehavior());
    expect(result.current.behavior).toBe("queue");
    act(() => result.current.setBehavior("steer"));
    expect(window.localStorage.getItem(KEY)).toBe("steer");
    act(() => result.current.setBehavior("queue"));
    expect(result.current.behavior).toBe("queue");
    expect(window.localStorage.getItem(KEY)).toBeNull();
  });

  it("round-trips queue-only across mounts", () => {
    const first = renderHook(() => useEnterSendBehavior());
    act(() => first.result.current.setBehavior("queue-only"));
    expect(first.result.current.behavior).toBe("queue-only");
    expect(window.localStorage.getItem(KEY)).toBe("queue-only");
    first.unmount();

    const second = renderHook(() => useEnterSendBehavior());
    expect(second.result.current.behavior).toBe("queue-only");
  });

  it("treats an unknown stored value as queue", () => {
    window.localStorage.setItem(KEY, "never-heard-of-it");
    const { result } = renderHook(() => useEnterSendBehavior());
    expect(result.current.behavior).toBe("queue");
  });
});

/**
 * The launch preference (the `--resume-latest` analogue): a new draft by
 * default with nothing stored, "latest" persisted only while chosen, and
 * the key removed again on the way back — so a fresh browser always lands
 * on a draft.
 */
describe("useLaunchTarget", () => {
  const LAUNCH_KEY = "mecatl-studio.launch-target";

  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("defaults to a new draft and stores nothing", () => {
    const { result } = renderHook(() => useLaunchTarget());
    expect(result.current.target).toBe("draft");
    expect(window.localStorage.getItem(LAUNCH_KEY)).toBeNull();
  });

  it("persists 'latest' across mounts and clears the key on draft", () => {
    const first = renderHook(() => useLaunchTarget());
    act(() => first.result.current.setTarget("latest"));
    expect(first.result.current.target).toBe("latest");
    expect(window.localStorage.getItem(LAUNCH_KEY)).toBe("latest");
    first.unmount();

    const second = renderHook(() => useLaunchTarget());
    expect(second.result.current.target).toBe("latest");

    act(() => second.result.current.setTarget("draft"));
    expect(second.result.current.target).toBe("draft");
    expect(window.localStorage.getItem(LAUNCH_KEY)).toBeNull();
  });

  it("treats an unknown stored value as draft", () => {
    window.localStorage.setItem(LAUNCH_KEY, "elsewhere");
    const { result } = renderHook(() => useLaunchTarget());
    expect(result.current.target).toBe("draft");
  });
});

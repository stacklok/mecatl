import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  EXPLICIT_DRAFT_KEY,
  markExplicitDraft,
} from "@/features/agent/latest-chat";
import type { LaunchTarget } from "@/lib/profile-preferences";
import { memoryStorage } from "@/test/memory-storage";
import { useLatestChatAutoOpen } from "./use-latest-chat-auto-open";

type Inputs = {
  selectedId: string;
  launchTarget: LaunchTarget;
  connected: boolean;
  sessionsLoading: boolean;
  hold: boolean;
  latestChatId: string | null;
};

const landing: Inputs = {
  selectedId: "",
  launchTarget: "latest",
  connected: true,
  sessionsLoading: false,
  hold: false,
  latestChatId: "chat-newest",
};

function mount(initial: Inputs) {
  const onOpen = vi.fn();
  const hook = renderHook(
    (inputs: Inputs) => useLatestChatAutoOpen({ ...inputs, onOpen }),
    { initialProps: initial },
  );
  return { ...hook, onOpen };
}

/**
 * The `--resume-latest` landing decision: once per mount, only on a draft
 * landing under the "Most recent chat" preference, only once the daemon is
 * connected and the inventory has loaded, and never over work the user has
 * already started on the draft. "New chat" settles it and marks the tab.
 */
describe("useLatestChatAutoOpen", () => {
  beforeEach(() => {
    vi.stubGlobal("sessionStorage", memoryStorage());
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("opens the newest eligible chat on a draft landing", () => {
    const { onOpen } = mount(landing);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith("chat-newest");
  });

  it("does nothing under the default (draft) preference", () => {
    const { onOpen, rerender } = mount({ ...landing, launchTarget: "draft" });
    expect(onOpen).not.toHaveBeenCalled();
    // The preference hydrates after the first frame: a later flip to
    // "latest" is still a landing decision, not a missed one.
    rerender({ ...landing, launchTarget: "latest" });
    expect(onOpen).toHaveBeenCalledWith("chat-newest");
  });

  it("waits for the connection and the first inventory load, then decides once", () => {
    const { onOpen, rerender } = mount({
      ...landing,
      connected: false,
      sessionsLoading: true,
      latestChatId: null,
    });
    expect(onOpen).not.toHaveBeenCalled();
    rerender({ ...landing, connected: true, sessionsLoading: true });
    expect(onOpen).not.toHaveBeenCalled();
    rerender({ ...landing, connected: true, sessionsLoading: false });
    expect(onOpen).toHaveBeenCalledTimes(1);
    // Settled: a newer pick arriving later never re-opens.
    rerender({ ...landing, latestChatId: "chat-newer-still" });
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("never fires when a chat is already selected (deep link, click, minted draft)", () => {
    const { onOpen, rerender } = mount({ ...landing, selectedId: "chat-1" });
    expect(onOpen).not.toHaveBeenCalled();
    rerender({ ...landing, selectedId: "chat-2" });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("stays on the draft when nothing is eligible (start fresh)", () => {
    const { onOpen, rerender } = mount({ ...landing, latestChatId: null });
    expect(onOpen).not.toHaveBeenCalled();
    // Settled on "fresh": a chat appearing later does not pull the user off
    // the draft they have been looking at.
    rerender({ ...landing, latestChatId: "chat-late" });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("never yanks the user off a draft that already holds work", () => {
    const { onOpen } = mount({ ...landing, hold: true });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("honours a fresh 'New chat' mark from the previous mount and clears it", () => {
    markExplicitDraft();
    const { onOpen } = mount(landing);
    expect(onOpen).not.toHaveBeenCalled();
    expect(window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY)).toBeNull();
  });

  it("consumes the mark under the draft preference too, so it cannot linger", () => {
    markExplicitDraft();
    const first = mount({ ...landing, launchTarget: "draft" });
    expect(window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY)).toBeNull();
    first.unmount();
    // The next landing under "latest" is a genuine one.
    const second = mount(landing);
    expect(second.onOpen).toHaveBeenCalledWith("chat-newest");
  });

  it("requestDraft settles this mount and marks the tab for a remount", () => {
    const { result, onOpen, rerender } = mount({
      ...landing,
      connected: false,
    });
    act(() => result.current.requestDraft());
    expect(window.sessionStorage.getItem(EXPLICIT_DRAFT_KEY)).not.toBeNull();
    // The daemon connecting afterwards must not open anything: the user
    // asked for a draft in this very mount.
    rerender({ ...landing, connected: true });
    expect(onOpen).not.toHaveBeenCalled();
  });
});

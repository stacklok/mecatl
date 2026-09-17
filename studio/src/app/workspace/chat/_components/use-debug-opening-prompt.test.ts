import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
  type DebugOpeningPromptDeps,
  useDebugOpeningPrompt,
} from "./use-debug-opening-prompt";

/**
 * Pins the one-shot auto-submit of a debug session's opening objective (ADR
 * 0254): it fires exactly once, only into the armed session, only while the
 * daemon is live and the chat hook reads idle; it never fires into a draft
 * (null id) or another chat; leaving the armed chat after it was seen drops
 * the message; a fresh arm replaces the pending one; cancel drops it.
 */

function setup(initial: Partial<DebugOpeningPromptDeps> = {}) {
  const sendMessage = vi.fn<(text: string) => void>();
  const deps: DebugOpeningPromptDeps = {
    sessionId: "target-1",
    live: true,
    status: "idle",
    sendMessage,
    ...initial,
  };
  const hook = renderHook(
    (props: DebugOpeningPromptDeps) => useDebugOpeningPrompt(props),
    { initialProps: deps },
  );
  const set = (next: Partial<DebugOpeningPromptDeps>) => {
    Object.assign(deps, next);
    hook.rerender({ ...deps });
  };
  return { hook, sendMessage, set };
}

describe("useDebugOpeningPrompt", () => {
  it("sends once, when the hook is keyed to the armed session, live and idle", () => {
    const { hook, sendMessage, set } = setup();
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    // Still keyed to the target: the selection has not landed yet — wait.
    expect(sendMessage).not.toHaveBeenCalled();
    expect(hook.result.current.pending).toBe(true);
    set({ sessionId: "debug-1" });
    expect(sendMessage).toHaveBeenCalledTimes(1);
    expect(sendMessage).toHaveBeenCalledWith("Diagnose it.");
    expect(hook.result.current.pending).toBe(false);
    // The send's own status flip and later idles never re-fire it.
    set({ status: "streaming" });
    set({ status: "idle" });
    expect(sendMessage).toHaveBeenCalledTimes(1);
  });

  it("fires immediately when already keyed, live and idle on the armed session", () => {
    const { hook, sendMessage } = setup({ sessionId: "debug-1" });
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    expect(sendMessage).toHaveBeenCalledTimes(1);
  });

  it("waits for the daemon to be live and the hook to be idle", () => {
    const { hook, sendMessage, set } = setup({
      sessionId: "debug-1",
      live: false,
      status: "idle",
    });
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    expect(sendMessage).not.toHaveBeenCalled();
    set({ live: true, status: "error" });
    expect(sendMessage).not.toHaveBeenCalled();
    set({ status: "idle" });
    expect(sendMessage).toHaveBeenCalledTimes(1);
  });

  it("never sends into a draft: a null id is not a match and cannot mint a session", () => {
    const { hook, sendMessage, set } = setup({ sessionId: null });
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    set({ sessionId: null, live: true, status: "idle" });
    expect(sendMessage).not.toHaveBeenCalled();
    expect(hook.result.current.pending).toBe(true);
  });

  it("drops the message when the user leaves the armed chat before it fired", () => {
    const { hook, sendMessage, set } = setup({ live: false });
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    // The selection lands while the daemon is not yet live: matched, unsent.
    set({ sessionId: "debug-1" });
    expect(sendMessage).not.toHaveBeenCalled();
    // The user opens another chat — the stale objective must never fire on
    // return, and never against the other chat.
    set({ sessionId: "other-9" });
    expect(hook.result.current.pending).toBe(false);
    set({ sessionId: "other-9", live: true });
    set({ sessionId: "debug-1", live: true });
    expect(sendMessage).not.toHaveBeenCalled();
  });

  it("keeps waiting while the hook is on another chat BEFORE the selection landed", () => {
    // arm() runs on the target chat; the workspace selects the debug chat a
    // render later. That intermediate render must not count as leaving.
    const { hook, sendMessage, set } = setup({ sessionId: "target-1" });
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    set({ sessionId: "target-1", status: "idle" });
    expect(hook.result.current.pending).toBe(true);
    set({ sessionId: "debug-1" });
    expect(sendMessage).toHaveBeenCalledWith("Diagnose it.");
  });

  it("a fresh arm replaces the pending message; cancel drops it", () => {
    const { hook, sendMessage, set } = setup();
    act(() => hook.result.current.arm("debug-1", "first"));
    act(() => hook.result.current.arm("debug-2", "second"));
    set({ sessionId: "debug-1" });
    expect(sendMessage).not.toHaveBeenCalled();
    set({ sessionId: "debug-2" });
    expect(sendMessage).toHaveBeenCalledTimes(1);
    expect(sendMessage).toHaveBeenCalledWith("second");

    act(() => hook.result.current.arm("debug-3", "third"));
    act(() => hook.result.current.cancel());
    expect(hook.result.current.pending).toBe(false);
    set({ sessionId: "debug-3" });
    expect(sendMessage).toHaveBeenCalledTimes(1);
  });

  it("reads the latest sendMessage at fire time", () => {
    const { hook, set } = setup({ sessionId: "target-1" });
    act(() => hook.result.current.arm("debug-1", "Diagnose it."));
    const replacement = vi.fn<(text: string) => void>();
    set({ sessionId: "debug-1", sendMessage: replacement });
    expect(replacement).toHaveBeenCalledWith("Diagnose it.");
  });
});

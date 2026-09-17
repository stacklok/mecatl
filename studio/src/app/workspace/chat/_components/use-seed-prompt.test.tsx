import { act, render, renderHook, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatSeed } from "@/lib/chat-seed";
import {
  SEED_PROMPT_EDIT_LABEL,
  SEED_PROMPT_SEND_LABEL,
} from "./seed-prompt-dialog";
import {
  SEED_WAIT_BUSY,
  SEED_WAIT_MOCK,
  SEED_WAIT_OFFLINE,
  type SeedPromptDeps,
  seedSendWaitReason,
  useSeedPrompt,
} from "./use-seed-prompt";

/**
 * The `?prompt=` arrival is consumed exactly once per mount: the query is
 * stripped from the URL (path kept), a prefill-only seed goes to the
 * composer, a `send=1` seed is parked behind the confirmation dialog whose
 * Send fires the chat hook's send once and whose Edit first prefills
 * instead; a seed that changes later is ignored; no seed does nothing.
 */

function setup(initial: Partial<SeedPromptDeps> = {}) {
  const onPrefill = vi.fn<(text: string) => void>();
  const onSend = vi.fn<(text: string) => void>();
  const deps: SeedPromptDeps = {
    seed: null,
    sessionId: undefined,
    onPrefill,
    onSend,
    waitReason: null,
    ...initial,
  };
  const hook = renderHook((props: SeedPromptDeps) => useSeedPrompt(props), {
    initialProps: deps,
  });
  const set = (next: Partial<SeedPromptDeps>) => {
    Object.assign(deps, next);
    hook.rerender({ ...deps });
  };
  return { hook, onPrefill, onSend, set };
}

afterEach(() => {
  window.history.replaceState(null, "", "/");
});

describe("useSeedPrompt", () => {
  it("does nothing without a seed", () => {
    window.history.replaceState(null, "", "/workspace/chat?inspect=run-1");
    const { hook, onPrefill, onSend } = setup();
    expect(hook.result.current.pendingSeed).toBeNull();
    expect(hook.result.current.seedPromptDialog).toBeNull();
    expect(onPrefill).not.toHaveBeenCalled();
    expect(onSend).not.toHaveBeenCalled();
    // Somebody else's query is left alone.
    expect(window.location.search).toBe("?inspect=run-1");
  });

  it("prefills the composer once and strips the query, keeping the path", () => {
    window.history.replaceState(
      null,
      "",
      "/workspace/chat/session-9?prompt=Hello%20fixture",
    );
    const seed: ChatSeed = { prompt: "Hello fixture", autoSend: false };
    const { hook, onPrefill, onSend, set } = setup({
      seed,
      sessionId: "session-9",
    });
    expect(onPrefill).toHaveBeenCalledTimes(1);
    expect(onPrefill).toHaveBeenCalledWith("Hello fixture");
    expect(onSend).not.toHaveBeenCalled();
    expect(hook.result.current.pendingSeed).toBeNull();
    expect(window.location.pathname).toBe("/workspace/chat/session-9");
    expect(window.location.search).toBe("");
    // A re-render with the same seed, and even a new seed object, is not a
    // second arrival: the mount was the one.
    set({ seed: { ...seed } });
    set({ seed: { prompt: "Another", autoSend: true } });
    expect(onPrefill).toHaveBeenCalledTimes(1);
    expect(hook.result.current.pendingSeed).toBeNull();
  });

  it("parks a send=1 seed behind the confirmation and Send fires the chat send once", async () => {
    const user = userEvent.setup();
    window.history.replaceState(
      null,
      "",
      "/workspace/chat?prompt=Hello%20fixture&send=1",
    );
    const { hook, onPrefill, onSend } = setup({
      seed: { prompt: "Hello fixture", autoSend: true },
    });
    expect(window.location.pathname).toBe("/workspace/chat");
    expect(window.location.search).toBe("");
    expect(onPrefill).not.toHaveBeenCalled();
    expect(onSend).not.toHaveBeenCalled();
    expect(hook.result.current.pendingSeed).toEqual({
      prompt: "Hello fixture",
      autoSend: true,
    });
    render(<div>{hook.result.current.seedPromptDialog}</div>);
    expect(screen.getByTestId("seed-prompt-text")).toHaveTextContent(
      "Hello fixture",
    );
    const send = screen.getByRole("button", { name: SEED_PROMPT_SEND_LABEL });
    await user.click(send);
    // A second click before the dialog unmounts is a no-op, never a resend.
    await act(async () => {
      send.click();
    });
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledWith("Hello fixture");
    expect(onPrefill).not.toHaveBeenCalled();
    expect(hook.result.current.pendingSeed).toBeNull();
    expect(hook.result.current.seedPromptDialog).toBeNull();
  });

  it("Edit first prefills the composer instead of sending", async () => {
    const user = userEvent.setup();
    const { hook, onPrefill, onSend } = setup({
      seed: { prompt: "Hello fixture", autoSend: true },
    });
    render(<div>{hook.result.current.seedPromptDialog}</div>);
    await user.click(
      screen.getByRole("button", { name: SEED_PROMPT_EDIT_LABEL }),
    );
    expect(onPrefill).toHaveBeenCalledWith("Hello fixture");
    expect(onSend).not.toHaveBeenCalled();
    expect(hook.result.current.pendingSeed).toBeNull();
  });

  it("the dialog's Send waits on the wait reason and reads the latest send", async () => {
    const user = userEvent.setup();
    const { hook, set } = setup({
      seed: { prompt: "Hello fixture", autoSend: true },
      waitReason: SEED_WAIT_OFFLINE,
    });
    const view = render(<div>{hook.result.current.seedPromptDialog}</div>);
    expect(
      screen.getByRole("button", { name: SEED_PROMPT_SEND_LABEL }),
    ).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent(SEED_WAIT_OFFLINE);
    const replacement = vi.fn<(text: string) => void>();
    set({ waitReason: null, onSend: replacement });
    view.rerender(<div>{hook.result.current.seedPromptDialog}</div>);
    await user.click(
      screen.getByRole("button", { name: SEED_PROMPT_SEND_LABEL }),
    );
    expect(replacement).toHaveBeenCalledWith("Hello fixture");
  });

  it("names the target from the route: a draft starts a new chat, an id posts into it", () => {
    const draft = setup({ seed: { prompt: "x", autoSend: true } });
    const draftView = render(
      <div>{draft.hook.result.current.seedPromptDialog}</div>,
    );
    expect(screen.getByRole("dialog")).toHaveTextContent(
      "it starts a new chat",
    );
    draftView.unmount();
    const open = setup({
      seed: { prompt: "x", autoSend: true },
      sessionId: "session-9",
    });
    render(<div>{open.hook.result.current.seedPromptDialog}</div>);
    expect(screen.getByRole("dialog")).toHaveTextContent(
      "it goes to this chat",
    );
  });
});

describe("seedSendWaitReason", () => {
  const idle = {
    connected: true,
    isStreaming: false,
    status: "idle",
    isMock: false,
  };

  it("lets an idle or errored chat on a connected daemon send", () => {
    expect(seedSendWaitReason(idle)).toBeNull();
    expect(seedSendWaitReason({ ...idle, status: "error" })).toBeNull();
  });

  it("holds while the mock tour is open, first of all", () => {
    expect(
      seedSendWaitReason({ ...idle, isMock: true, connected: false }),
    ).toBe(SEED_WAIT_MOCK);
  });

  it("holds while the daemon is not reachable", () => {
    expect(seedSendWaitReason({ ...idle, connected: false })).toBe(
      SEED_WAIT_OFFLINE,
    );
  });

  it("holds while a run streams or the chat is parked on an ask", () => {
    expect(seedSendWaitReason({ ...idle, isStreaming: true })).toBe(
      SEED_WAIT_BUSY,
    );
    for (const status of [
      "streaming",
      "waiting_approval",
      "waiting_authorization",
      "waiting_clarification",
    ]) {
      expect(seedSendWaitReason({ ...idle, status })).toBe(SEED_WAIT_BUSY);
    }
  });
});

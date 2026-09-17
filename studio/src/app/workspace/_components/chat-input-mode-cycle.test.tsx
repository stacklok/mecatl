import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatInput, ModeSelector } from "./chat-input";

/**
 * The composer's permission-mode affordances, driven through the rendered
 * ChatInput (the paste test proved a synthetic event on the ProseMirror DOM
 * reaches the editor's capture-phase listener):
 *
 * - ⇧Tab in the editor cycles Manual → Plan → Accept edits → Manual and
 *   claims the key (preventDefault), so focus never leaves the editor; a bare
 *   Tab, a disabled composer and a streaming run without the deferred-switch
 *   contract all leave the key to the browser.
 * - The Mode pill stays ENABLED while streaming under `modeSwitchDeferred`,
 *   reads "<target> · pending" for a held switch, and carries the mode's
 *   colour cue; the composer box gets the matching tint.
 *
 * The decision table itself (`resolveModeCycleKey`) is unit tested in
 * lib/permission-mode.test.ts; this file proves the wiring.
 */

// jsdom lays nothing out: ProseMirror's scroll-into-view after a focus asks
// the selection for its rects, so give it empty ones.
const zeroRect = () =>
  ({
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    width: 0,
    height: 0,
    toJSON: () => ({}),
  }) as DOMRect;
for (const proto of [Element.prototype, Range.prototype]) {
  if (!proto.getClientRects) {
    proto.getClientRects = () => [] as unknown as DOMRectList;
  }
  if (!proto.getBoundingClientRect) {
    proto.getBoundingClientRect = zeroRect;
  }
}

async function renderComposer(props: Parameters<typeof ChatInput>[0] = {}) {
  const utils = render(<ChatInput {...props} />);
  // useEditor with immediatelyRender:false mounts the ProseMirror view in an
  // effect, so the DOM arrives a tick after render.
  const dom = await waitFor(() => {
    const el = utils.container.querySelector<HTMLElement>(".ProseMirror");
    if (!el) throw new Error("editor not mounted yet");
    return el;
  });
  return { ...utils, dom };
}

/** Dispatch a keydown on the editor DOM; returns the event for its flags. */
function keydown(
  dom: HTMLElement,
  key: string,
  mods: Partial<{
    shift: boolean;
    meta: boolean;
    ctrl: boolean;
    alt: boolean;
  }> = {},
): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key,
    shiftKey: mods.shift ?? false,
    metaKey: mods.meta ?? false,
    ctrlKey: mods.ctrl ?? false,
    altKey: mods.alt ?? false,
    bubbles: true,
    cancelable: true,
  });
  act(() => {
    dom.dispatchEvent(event);
  });
  return event;
}

// The pill's accessible name is its label text; the full selection rides
// its title, which is the stable handle across "Plan" / "Plan · pending".
const modePill = () =>
  screen.getByTitle(/^Permission mode:/) as HTMLButtonElement;

afterEach(() => {
  vi.restoreAllMocks();
});

describe("⇧Tab cycles the permission mode from the editor", () => {
  it("advances Manual → Plan and claims the key so focus stays put", async () => {
    const onModeChange = vi.fn();
    const { dom } = await renderComposer({ mode: "default", onModeChange });
    const event = keydown(dom, "Tab", { shift: true });
    expect(onModeChange).toHaveBeenCalledWith("plan");
    expect(event.defaultPrevented).toBe(true);
  });

  it("continues the cycle from the shown mode: Plan → Accept edits → Manual", async () => {
    const onModeChange = vi.fn();
    const { dom, rerender } = await renderComposer({
      mode: "plan",
      onModeChange,
    });
    keydown(dom, "Tab", { shift: true });
    expect(onModeChange).toHaveBeenLastCalledWith("acceptEdits");
    rerender(<ChatInput mode="acceptEdits" onModeChange={onModeChange} />);
    keydown(dom, "Tab", { shift: true });
    expect(onModeChange).toHaveBeenLastCalledWith("default");
  });

  it("cycles onward from a HELD switch, not the confirmed mode", async () => {
    const onModeChange = vi.fn();
    const { dom } = await renderComposer({
      mode: "default",
      pendingMode: "plan",
      onModeChange,
      isStreaming: true,
      modeSwitchDeferred: true,
    });
    keydown(dom, "Tab", { shift: true });
    expect(onModeChange).toHaveBeenCalledWith("acceptEdits");
  });

  it("leaves a bare Tab, ⌘⇧Tab and ⌥⇧Tab to the browser", async () => {
    const onModeChange = vi.fn();
    const { dom } = await renderComposer({ mode: "default", onModeChange });
    expect(keydown(dom, "Tab").defaultPrevented).toBe(false);
    expect(
      keydown(dom, "Tab", { shift: true, meta: true }).defaultPrevented,
    ).toBe(false);
    expect(
      keydown(dom, "Tab", { shift: true, ctrl: true }).defaultPrevented,
    ).toBe(false);
    expect(
      keydown(dom, "Tab", { shift: true, alt: true }).defaultPrevented,
    ).toBe(false);
    expect(onModeChange).not.toHaveBeenCalled();
  });

  it("is inert where the pill is disabled: streaming without deferral, or a disabled composer", async () => {
    const onModeChange = vi.fn();
    const streaming = await renderComposer({
      mode: "default",
      onModeChange,
      isStreaming: true,
    });
    expect(modePill()).toBeDisabled();
    expect(
      keydown(streaming.dom, "Tab", { shift: true }).defaultPrevented,
    ).toBe(false);
    expect(onModeChange).not.toHaveBeenCalled();
    streaming.unmount();

    const disabled = await renderComposer({
      mode: "default",
      onModeChange,
      disabled: true,
    });
    expect(keydown(disabled.dom, "Tab", { shift: true }).defaultPrevented).toBe(
      false,
    );
    expect(onModeChange).not.toHaveBeenCalled();
  });

  it("is inert on a surface with no Mode selector", async () => {
    const { dom } = await renderComposer({});
    expect(keydown(dom, "Tab", { shift: true }).defaultPrevented).toBe(false);
  });
});

describe("the Mode pill under the deferred-switch contract", () => {
  it("stays enabled while streaming and reads the held target as pending, with the mode's colour", async () => {
    await renderComposer({
      mode: "default",
      pendingMode: "plan",
      onModeChange: vi.fn(),
      isStreaming: true,
      modeSwitchDeferred: true,
    });
    const pill = modePill();
    expect(pill).toBeEnabled();
    expect(pill).toHaveTextContent("Plan · pending");
    expect(pill.title).toContain("Permission mode: Plan (pending");
    expect(pill.className).toContain("text-info");
  });

  it("reads the confirmed mode plainly once nothing is held", async () => {
    await renderComposer({ mode: "acceptEdits", onModeChange: vi.fn() });
    const pill = modePill();
    expect(pill).toHaveTextContent("Accept edits");
    expect(pill).not.toHaveTextContent("pending");
    expect(pill.className).toContain("text-success");
  });

  it("gives Manual no colour cue at all", async () => {
    await renderComposer({ mode: "default", onModeChange: vi.fn() });
    const pill = modePill();
    expect(pill).toHaveTextContent("Manual");
    expect(pill.className).not.toContain("text-info");
    expect(pill.className).not.toContain("text-success");
  });

  it("tints the composer box for Plan / Accept edits and not for Manual", async () => {
    const plan = await renderComposer({ mode: "plan", onModeChange: vi.fn() });
    expect(
      plan.container.querySelector('[class*="border-info/50"]'),
    ).not.toBeNull();
    plan.unmount();

    const manual = await renderComposer({
      mode: "default",
      onModeChange: vi.fn(),
    });
    expect(manual.container.querySelector('[class*="border-info/50"]')).toBe(
      null,
    );
    expect(
      manual.container.querySelector('[class*="border-success/50"]'),
    ).toBeNull();
  });
});

describe("ModeSelector", () => {
  it("labels a pending switch and keeps the full selection on the title", () => {
    render(<ModeSelector mode="acceptEdits" onModeChange={vi.fn()} pending />);
    const pill = modePill();
    expect(pill).toHaveTextContent("Accept edits · pending");
    expect(pill.title).toBe(
      "Permission mode: Accept edits (pending — applies when the run ends) — ⇧Tab cycles",
    );
    expect(pill.className).toContain("text-success");
  });
});

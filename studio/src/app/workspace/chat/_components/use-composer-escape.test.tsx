import { fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ShortcutsProvider } from "@/lib/shortcuts/use-shortcuts";
import { memoryStorage } from "@/test/memory-storage";
import {
  type ComposerEscapeInput,
  useComposerEscape,
} from "./use-composer-escape";

/**
 * The `close.esc` owner for a chat surface: one Esc resolves to exactly one
 * arm — selection, panel, run — and only when nothing else claims the key
 * does it reach the composer as an `escapePress` tick. Dispatched through
 * the REAL ShortcutsProvider so the registry's Esc binding is exercised.
 */

function Surface(props: Omit<ComposerEscapeInput, "hasSelectionIn">) {
  const transcript = useRef<HTMLElement | null>(null);
  const { escapePress } = useComposerEscape({
    ...props,
    hasSelectionIn: transcript,
  });
  return (
    <div>
      <section ref={transcript} data-testid="transcript">
        <p>selectable transcript text</p>
      </section>
      <output data-testid="press">{escapePress}</output>
    </div>
  );
}

function mount(props: Omit<ComposerEscapeInput, "hasSelectionIn">) {
  return render(
    <ShortcutsProvider>
      <Surface {...props} />
    </ShortcutsProvider>,
  );
}

const pressEsc = () => fireEvent.keyDown(document.body, { key: "Escape" });
const presses = () => Number(screen.getByTestId("press").textContent);

const idle = {
  hasDraft: false,
  panelOpen: false,
  isStreaming: false,
};

describe("useComposerEscape", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  afterEach(() => {
    window.getSelection()?.removeAllRanges();
  });

  it("denies a pending ask instead of closing the panel or cancelling the run", () => {
    const onDenyAsk = vi.fn();
    const onClosePanel = vi.fn();
    const onCancelRun = vi.fn();
    mount({
      ...idle,
      hasDraft: true,
      pendingAsk: true,
      panelOpen: true,
      isStreaming: true,
      onDenyAsk,
      onClosePanel,
      onCancelRun,
    });
    pressEsc();
    expect(onDenyAsk).toHaveBeenCalledTimes(1);
    expect(onClosePanel).not.toHaveBeenCalled();
    expect(onCancelRun).not.toHaveBeenCalled();
    expect(presses()).toBe(0);
  });

  it("falls back to the run arm once the ask is answered", () => {
    const onDenyAsk = vi.fn();
    const onCancelRun = vi.fn();
    const { rerender } = mount({
      ...idle,
      pendingAsk: true,
      isStreaming: true,
      onDenyAsk,
      onCancelRun,
    });
    pressEsc();
    expect(onDenyAsk).toHaveBeenCalledTimes(1);
    rerender(
      <ShortcutsProvider>
        <Surface
          {...idle}
          pendingAsk={false}
          isStreaming
          onDenyAsk={onDenyAsk}
          onCancelRun={onCancelRun}
        />
      </ShortcutsProvider>,
    );
    pressEsc();
    expect(onCancelRun).toHaveBeenCalledTimes(1);
    expect(onDenyAsk).toHaveBeenCalledTimes(1);
  });

  it("closes an open panel and never forwards the press to the composer", () => {
    const onClosePanel = vi.fn();
    const onCancelRun = vi.fn();
    mount({
      ...idle,
      hasDraft: true,
      panelOpen: true,
      isStreaming: true,
      onClosePanel,
      onCancelRun,
    });
    pressEsc();
    expect(onClosePanel).toHaveBeenCalledTimes(1);
    expect(onCancelRun).not.toHaveBeenCalled();
    expect(presses()).toBe(0);
  });

  it("cancels a streaming run ahead of the draft, without forwarding", () => {
    const onCancelRun = vi.fn();
    mount({ ...idle, hasDraft: true, isStreaming: true, onCancelRun });
    pressEsc();
    expect(onCancelRun).toHaveBeenCalledTimes(1);
    expect(presses()).toBe(0);
  });

  it("forwards each press on an idle draft as an escapePress tick", () => {
    const onCancelRun = vi.fn();
    const onClosePanel = vi.fn();
    mount({ ...idle, hasDraft: true, onCancelRun, onClosePanel });
    pressEsc();
    expect(presses()).toBe(1);
    pressEsc();
    expect(presses()).toBe(2);
    expect(onCancelRun).not.toHaveBeenCalled();
    expect(onClosePanel).not.toHaveBeenCalled();
  });

  it("does nothing on an idle, empty composer with no panel", () => {
    const onCancelRun = vi.fn();
    const onClosePanel = vi.fn();
    mount({ ...idle, onCancelRun, onClosePanel });
    pressEsc();
    expect(presses()).toBe(0);
    expect(onCancelRun).not.toHaveBeenCalled();
    expect(onClosePanel).not.toHaveBeenCalled();
  });

  it("drops a transcript selection first — an Esc meant to clear a selection never touches the draft or the run", () => {
    const onCancelRun = vi.fn();
    const onClearSelection = vi.fn();
    mount({
      ...idle,
      hasDraft: true,
      isStreaming: true,
      onCancelRun,
      onClearSelection,
    });
    const text = screen.getByText("selectable transcript text");
    const range = document.createRange();
    range.selectNodeContents(text);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);

    pressEsc();
    expect(onClearSelection).toHaveBeenCalledTimes(1);
    expect(onCancelRun).not.toHaveBeenCalled();
    expect(presses()).toBe(0);
  });

  it("leaves an Escape another layer already consumed alone", () => {
    const onCancelRun = vi.fn();
    mount({ ...idle, isStreaming: true, onCancelRun });
    const event = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    });
    event.preventDefault();
    document.body.dispatchEvent(event);
    expect(onCancelRun).not.toHaveBeenCalled();
  });
});

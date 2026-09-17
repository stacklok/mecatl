import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import { KEYMAP_STORAGE_KEY, writeOverrides } from "./keymap";
import { ShortcutsProvider, useShortcut } from "./use-shortcuts";

/** A component that registers one shortcut handler, like ChatView does. */
function Bind({ id, onFire }: { id: string; onFire: () => void }) {
  useShortcut(id, onFire);
  return null;
}

function mount(bindings: Record<string, () => void>) {
  return render(
    <ShortcutsProvider>
      {Object.entries(bindings).map(([id, fn]) => (
        <Bind key={id} id={id} onFire={fn} />
      ))}
      <textarea aria-label="composer" />
    </ShortcutsProvider>,
  );
}

function focusComposer(): HTMLTextAreaElement {
  const composer = screen.getByLabelText("composer") as HTMLTextAreaElement;
  composer.focus();
  expect(document.activeElement).toBe(composer);
  return composer;
}

describe("ShortcutsProvider dispatch while typing", () => {
  it("fires transcript.pageUp on PageUp while the caret sits in a text field", () => {
    const pageUp = vi.fn();
    mount({ "transcript.pageUp": pageUp });
    const composer = focusComposer();

    // fireEvent returns false when a listener called preventDefault — the
    // dispatcher claims the key so the field's caret doesn't also move.
    const notPrevented = fireEvent.keyDown(composer, { key: "PageUp" });

    expect(pageUp).toHaveBeenCalledTimes(1);
    expect(notPrevented).toBe(false);
  });

  it("fires transcript.pageDown on PageDown while typing", () => {
    const pageDown = vi.fn();
    mount({ "transcript.pageDown": pageDown });
    fireEvent.keyDown(focusComposer(), { key: "PageDown" });
    expect(pageDown).toHaveBeenCalledTimes(1);
  });

  it("routes ⇧PageUp / ⇧PageDown to the top/bottom jumps, not the page steps", () => {
    const pageUp = vi.fn();
    const pageDown = vi.fn();
    const top = vi.fn();
    const bottom = vi.fn();
    mount({
      "transcript.pageUp": pageUp,
      "transcript.pageDown": pageDown,
      "transcript.top": top,
      "transcript.bottom": bottom,
    });
    const composer = focusComposer();

    fireEvent.keyDown(composer, { key: "PageUp", shiftKey: true });
    fireEvent.keyDown(composer, { key: "PageDown", shiftKey: true });

    expect(top).toHaveBeenCalledTimes(1);
    expect(bottom).toHaveBeenCalledTimes(1);
    expect(pageUp).not.toHaveBeenCalled();
    expect(pageDown).not.toHaveBeenCalled();
  });

  it("leaves the browser's Ctrl+PageUp tab-switch chord alone", () => {
    const pageUp = vi.fn();
    const top = vi.fn();
    mount({ "transcript.pageUp": pageUp, "transcript.top": top });
    const notPrevented = fireEvent.keyDown(focusComposer(), {
      key: "PageUp",
      ctrlKey: true,
    });
    expect(pageUp).not.toHaveBeenCalled();
    expect(top).not.toHaveBeenCalled();
    expect(notPrevented).toBe(true);
  });

  it("keeps a plain letter as text while typing, but fires it elsewhere", () => {
    const next = vi.fn();
    mount({ "chat.next.vim": next });

    fireEvent.keyDown(focusComposer(), { key: "j" });
    expect(next).not.toHaveBeenCalled();

    (document.activeElement as HTMLElement | null)?.blur();
    fireEvent.keyDown(document.body, { key: "j" });
    expect(next).toHaveBeenCalledTimes(1);
  });

  it("treats a contenteditable editor (the TipTap composer) as typing", () => {
    const next = vi.fn();
    const pageUp = vi.fn();
    const { container } = mount({
      "chat.next.vim": next,
      "transcript.pageUp": pageUp,
    });
    const editor = document.createElement("div");
    editor.setAttribute("contenteditable", "true");
    editor.tabIndex = 0;
    // jsdom's ElementContentEditable is a stub, so `isContentEditable` is
    // undefined there; the dispatcher reads exactly that property.
    Object.defineProperty(editor, "isContentEditable", { value: true });
    container.appendChild(editor);
    editor.focus();
    expect(document.activeElement).toBe(editor);

    fireEvent.keyDown(editor, { key: "j" });
    fireEvent.keyDown(editor, { key: "PageUp" });

    expect(next).not.toHaveBeenCalled();
    expect(pageUp).toHaveBeenCalledTimes(1);
  });

  it("ignores an event a closer layer already consumed", () => {
    const pageUp = vi.fn();
    mount({ "transcript.pageUp": pageUp });
    const composer = focusComposer();

    const consumed = new KeyboardEvent("keydown", {
      key: "PageUp",
      bubbles: true,
      cancelable: true,
    });
    consumed.preventDefault();
    composer.dispatchEvent(consumed);

    expect(pageUp).not.toHaveBeenCalled();
  });

  it("does nothing for an id without a registered handler", () => {
    const pageUp = vi.fn();
    mount({ "transcript.pageUp": pageUp });
    const notPrevented = fireEvent.keyDown(focusComposer(), {
      key: "PageDown",
    });
    expect(pageUp).not.toHaveBeenCalled();
    expect(notPrevented).toBe(true);
  });

  it("withdraws a handler registered with enabled: false — the key keeps its default", () => {
    function Gated({
      enabled,
      onFire,
    }: {
      enabled: boolean;
      onFire: () => void;
    }) {
      useShortcut("approval.allow", onFire, { enabled });
      return null;
    }
    const allow = vi.fn();
    const { rerender } = render(
      <ShortcutsProvider>
        <Gated enabled={false} onFire={allow} />
      </ShortcutsProvider>,
    );
    // Not registered: nothing fires and the press is not prevented.
    expect(fireEvent.keyDown(document.body, { key: "y" })).toBe(true);
    expect(allow).not.toHaveBeenCalled();

    rerender(
      <ShortcutsProvider>
        <Gated enabled={true} onFire={allow} />
      </ShortcutsProvider>,
    );
    expect(fireEvent.keyDown(document.body, { key: "y" })).toBe(false);
    expect(allow).toHaveBeenCalledTimes(1);

    rerender(
      <ShortcutsProvider>
        <Gated enabled={false} onFire={allow} />
      </ShortcutsProvider>,
    );
    expect(fireEvent.keyDown(document.body, { key: "y" })).toBe(true);
    expect(allow).toHaveBeenCalledTimes(1);
  });

  it("never claims ⌘A / Ctrl+A globally — the transcript select-all is component-owned", () => {
    // `transcript.selectAll` is a FIXED registry row: no handler is ever
    // registered for it, so a select-all in the composer (or anywhere else)
    // keeps the browser's native meaning even with other handlers live.
    const esc = vi.fn();
    const search = vi.fn();
    mount({ "close.esc": esc, "search.open": search });
    const composer = focusComposer();

    expect(fireEvent.keyDown(composer, { key: "a", metaKey: true })).toBe(true);
    (document.activeElement as HTMLElement | null)?.blur();
    expect(fireEvent.keyDown(document.body, { key: "a", ctrlKey: true })).toBe(
      true,
    );
    expect(esc).not.toHaveBeenCalled();
    expect(search).not.toHaveBeenCalled();
  });
});

describe("ShortcutsProvider with a user keymap (Settings → Keyboard)", () => {
  it("dispatches the remapped chord and no longer the registry default", () => {
    vi.stubGlobal("localStorage", memoryStorage());
    window.localStorage.setItem(
      KEYMAP_STORAGE_KEY,
      JSON.stringify({ "chat.new": "mod+shift+k" }),
    );
    const newChat = vi.fn();
    mount({ "chat.new": newChat });
    (document.activeElement as HTMLElement | null)?.blur();

    fireEvent.keyDown(document.body, {
      key: "K",
      metaKey: true,
      shiftKey: true,
    });
    expect(newChat).toHaveBeenCalledTimes(1);

    fireEvent.keyDown(document.body, {
      key: "O",
      metaKey: true,
      shiftKey: true,
    });
    expect(newChat).toHaveBeenCalledTimes(1);
  });

  it("keeps the while-typing suppression for a plain-letter override", () => {
    vi.stubGlobal("localStorage", memoryStorage());
    // `x`: a letter no live shortcut uses (`n` became Deny's when the
    // approval verdicts joined the registry — an override on it would be
    // dropped as a collision).
    window.localStorage.setItem(
      KEYMAP_STORAGE_KEY,
      JSON.stringify({ "chat.new": "x" }),
    );
    const newChat = vi.fn();
    mount({ "chat.new": newChat });

    // In the composer the letter stays text — the remapped shortcut is
    // suppressed exactly as a plain-letter default would be.
    const composer = focusComposer();
    fireEvent.keyDown(composer, { key: "x" });
    expect(newChat).not.toHaveBeenCalled();

    // Outside a text field the letter fires, and the default no longer does.
    composer.blur();
    fireEvent.keyDown(document.body, { key: "x" });
    expect(newChat).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document.body, {
      key: "O",
      metaKey: true,
      shiftKey: true,
    });
    expect(newChat).toHaveBeenCalledTimes(1);
  });

  it("picks up a keymap change made while mounted", () => {
    vi.stubGlobal("localStorage", memoryStorage());
    const newChat = vi.fn();
    mount({ "chat.new": newChat });
    (document.activeElement as HTMLElement | null)?.blur();

    fireEvent.keyDown(document.body, {
      key: "O",
      metaKey: true,
      shiftKey: true,
    });
    expect(newChat).toHaveBeenCalledTimes(1);

    act(() => writeOverrides({ "chat.new": "mod+shift+k" }));
    fireEvent.keyDown(document.body, {
      key: "K",
      metaKey: true,
      shiftKey: true,
    });
    expect(newChat).toHaveBeenCalledTimes(2);
    fireEvent.keyDown(document.body, {
      key: "O",
      metaKey: true,
      shiftKey: true,
    });
    expect(newChat).toHaveBeenCalledTimes(2);
  });
});

import { act, renderHook } from "@testing-library/react";
import type { Editor } from "@tiptap/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ESCAPE_ARM_MS } from "@/app/workspace/chat/_components/escape-layering";
import { draftStorageKey } from "@/lib/draft-store";
import { memoryStorage } from "@/test/memory-storage";
import {
  DRAFT_WRITE_DEBOUNCE_MS,
  useComposerDraftGuards,
} from "./composer-draft-guards";

/**
 * The composer's draft guards over a fake editor (driving TipTap keydowns
 * in jsdom is impractical; the hook reads the plain-text mirror ChatInput
 * keeps in sync, so a `setText` here stands in for a keystroke): the
 * double-Esc arm machine, sessionStorage persistence with its restore /
 * flush points, and the has-text signal the surfaces lift.
 */

/** A minimal Editor: a doc that is a plain string. */
function fakeEditor(initial = "") {
  let doc = initial;
  const editor = {
    get isEmpty() {
      return doc === "";
    },
    getText: () => doc,
    commands: {
      setContent: (content: { content: { content: { text: string }[] }[] }) => {
        doc = content.content
          .map((p) => p.content.map((t) => t.text).join(""))
          .join("\n");
        return true;
      },
      clearContent: () => {
        doc = "";
        return true;
      },
    },
  };
  return editor as unknown as Editor & { readonly isEmpty: boolean };
}

interface HarnessProps {
  editor: Editor | null;
  escapePress?: number;
  draftKey?: string;
  initialTextPending?: boolean;
  onDraftChange?: (hasText: boolean) => void;
  clearComposer?: () => void;
}

/** ChatInput's shape around the hook: the `text` mirror plus its setter. */
function useHarness(props: HarnessProps) {
  const [text, setText] = useState("");
  const guards = useComposerDraftGuards({
    editor: props.editor,
    text,
    setText,
    escapePress: props.escapePress,
    draftKey: props.draftKey,
    initialTextPending: props.initialTextPending ?? false,
    onDraftChange: props.onDraftChange,
    clearComposer: () => {
      props.clearComposer?.();
      props.editor?.commands.clearContent();
      setText("");
    },
  });
  return { text, setText, ...guards };
}

const KEY = draftStorageKey("s1");

describe("useComposerDraftGuards", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("sessionStorage", memoryStorage());
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("draft persistence", () => {
    it("restores a stored draft into an empty editor once it mounts", () => {
      sessionStorage.setItem(KEY, "left over\nfrom before");
      const editor = fakeEditor();
      const { result, rerender } = renderHook(useHarness, {
        // The editor is null on the first render (immediatelyRender: false).
        initialProps: { editor: null, draftKey: "s1" } as HarnessProps,
      });
      expect(result.current.text).toBe("");
      rerender({ editor, draftKey: "s1" });
      expect(result.current.text).toBe("left over\nfrom before");
      expect(editor.getText()).toBe("left over\nfrom before");
    });

    it("never restores over a pending seed, and not into a non-empty editor", () => {
      sessionStorage.setItem(KEY, "stored");
      const seeded = renderHook(useHarness, {
        initialProps: {
          editor: fakeEditor(),
          draftKey: "s1",
          initialTextPending: true,
        } as HarnessProps,
      });
      expect(seeded.result.current.text).toBe("");

      const typed = renderHook(useHarness, {
        initialProps: {
          editor: fakeEditor("already typing"),
          draftKey: "s1",
        } as HarnessProps,
      });
      expect(typed.result.current.text).toBe("");
    });

    it("leaves a stored draft alone when the composer mounts empty and idle", () => {
      // The initial (empty) text must not be persisted as a removal before
      // the restore reads the entry — a mount is not an edit.
      sessionStorage.setItem(KEY, "stored");
      renderHook(useHarness, {
        initialProps: { editor: null, draftKey: "s1" } as HarnessProps,
      });
      expect(sessionStorage.getItem(KEY)).toBe("stored");
    });

    it("writes an edit after the debounce and removes the entry at once when the composer empties", () => {
      const { result } = renderHook(useHarness, {
        initialProps: { editor: fakeEditor(), draftKey: "s1" } as HarnessProps,
      });
      act(() => result.current.setText("hel"));
      act(() => result.current.setText("hello"));
      expect(sessionStorage.getItem(KEY)).toBeNull();
      act(() => vi.advanceTimersByTime(DRAFT_WRITE_DEBOUNCE_MS));
      expect(sessionStorage.getItem(KEY)).toBe("hello");

      act(() => result.current.setText(""));
      expect(sessionStorage.getItem(KEY)).toBeNull();
    });

    it("does not persist without a draftKey (the thread panel)", () => {
      const { result } = renderHook(useHarness, {
        initialProps: { editor: fakeEditor() } as HarnessProps,
      });
      act(() => result.current.setText("ephemeral"));
      act(() => vi.advanceTimersByTime(DRAFT_WRITE_DEBOUNCE_MS));
      expect(sessionStorage.length).toBe(0);
    });

    it("flushes a pending write on pagehide and on unmount", () => {
      const first = renderHook(useHarness, {
        initialProps: { editor: fakeEditor(), draftKey: "s1" } as HarnessProps,
      });
      act(() => first.result.current.setText("not yet written"));
      expect(sessionStorage.getItem(KEY)).toBeNull();
      act(() => {
        window.dispatchEvent(new Event("pagehide"));
      });
      expect(sessionStorage.getItem(KEY)).toBe("not yet written");
      first.unmount();

      sessionStorage.clear();
      const second = renderHook(useHarness, {
        initialProps: { editor: fakeEditor(), draftKey: "s1" } as HarnessProps,
      });
      act(() => second.result.current.setText("leaving"));
      second.unmount();
      expect(sessionStorage.getItem(KEY)).toBe("leaving");
    });

    it("flushes the previous key when the draftKey changes and keeps the in-memory text (no restore over it)", () => {
      sessionStorage.setItem(draftStorageKey("s2"), "s2 draft");
      const editor = fakeEditor();
      const { result, rerender } = renderHook(useHarness, {
        initialProps: { editor, draftKey: "s1" } as HarnessProps,
      });
      // Typing lands in the editor doc; ChatInput mirrors it into `text`.
      editor.commands.setContent({
        type: "doc",
        content: [
          { type: "paragraph", content: [{ type: "text", text: "s1 draft" }] },
        ],
      });
      act(() => result.current.setText("s1 draft"));
      // Switching chats before the debounce fired: s1's text lands under s1.
      rerender({ editor, draftKey: "s2" });
      expect(sessionStorage.getItem(KEY)).toBe("s1 draft");
      // The composer still shows s1's text (the in-memory carry-over), so s2's
      // stored draft is neither restored over it nor touched.
      expect(result.current.text).toBe("s1 draft");
      expect(sessionStorage.getItem(draftStorageKey("s2"))).toBe("s2 draft");
    });

    it("restores the new key's draft into an EMPTY composer on a key change, without deleting anything on the way", () => {
      sessionStorage.setItem(draftStorageKey("s2"), "s2 draft");
      const editor = fakeEditor();
      const { result, rerender } = renderHook(useHarness, {
        initialProps: { editor, draftKey: "s1" } as HarnessProps,
      });
      expect(result.current.text).toBe("");
      rerender({ editor, draftKey: "s2" });
      expect(result.current.text).toBe("s2 draft");
      // A blank composer flushes nothing: switching away from a never-typed
      // key leaves storage alone.
      expect(sessionStorage.getItem(KEY)).toBeNull();
      expect(sessionStorage.getItem(draftStorageKey("s2"))).toBe("s2 draft");
    });
  });

  describe("has-text signal", () => {
    it("fires only when the has-text state flips, and false on unmount", () => {
      const onDraftChange = vi.fn();
      const { result, unmount } = renderHook(useHarness, {
        initialProps: { editor: fakeEditor(), onDraftChange } as HarnessProps,
      });
      expect(onDraftChange).not.toHaveBeenCalled();
      act(() => result.current.setText("a"));
      act(() => result.current.setText("ab"));
      expect(onDraftChange).toHaveBeenCalledTimes(1);
      expect(onDraftChange).toHaveBeenLastCalledWith(true);
      act(() => result.current.setText("   "));
      expect(onDraftChange).toHaveBeenCalledTimes(2);
      expect(onDraftChange).toHaveBeenLastCalledWith(false);
      act(() => result.current.setText("back"));
      expect(onDraftChange).toHaveBeenCalledTimes(3);
      unmount();
      expect(onDraftChange).toHaveBeenCalledTimes(4);
      expect(onDraftChange).toHaveBeenLastCalledWith(false);
    });
  });

  describe("double-Esc clear", () => {
    it("arms on the first press, clears on a second inside the window, and drops the stored draft", () => {
      const clearComposer = vi.fn();
      const editor = fakeEditor();
      const { result, rerender } = renderHook(useHarness, {
        initialProps: {
          editor,
          draftKey: "s1",
          escapePress: 0,
          clearComposer,
        } as HarnessProps,
      });
      act(() => result.current.setText("draft"));
      act(() => vi.advanceTimersByTime(DRAFT_WRITE_DEBOUNCE_MS));
      expect(sessionStorage.getItem(KEY)).toBe("draft");

      rerender({ editor, draftKey: "s1", escapePress: 1, clearComposer });
      expect(result.current.escapeArmed).toBe(true);
      expect(clearComposer).not.toHaveBeenCalled();
      expect(result.current.text).toBe("draft");

      act(() => vi.advanceTimersByTime(ESCAPE_ARM_MS - 1));
      rerender({ editor, draftKey: "s1", escapePress: 2, clearComposer });
      expect(clearComposer).toHaveBeenCalledTimes(1);
      expect(result.current.text).toBe("");
      expect(result.current.escapeArmed).toBe(false);
      expect(sessionStorage.getItem(KEY)).toBeNull();
    });

    it("re-arms instead of clearing once the window has lapsed", () => {
      const clearComposer = vi.fn();
      const editor = fakeEditor();
      const { result, rerender } = renderHook(useHarness, {
        initialProps: {
          editor,
          escapePress: 0,
          clearComposer,
        } as HarnessProps,
      });
      act(() => result.current.setText("draft"));
      rerender({ editor, escapePress: 1, clearComposer });
      expect(result.current.escapeArmed).toBe(true);
      act(() => vi.advanceTimersByTime(ESCAPE_ARM_MS));
      // The hint has gone; the next press is a first press again.
      expect(result.current.escapeArmed).toBe(false);
      rerender({ editor, escapePress: 2, clearComposer });
      expect(clearComposer).not.toHaveBeenCalled();
      expect(result.current.escapeArmed).toBe(true);
      expect(result.current.text).toBe("draft");
    });

    it("disarms on an edit, and ignores presses on an empty composer", () => {
      const clearComposer = vi.fn();
      const editor = fakeEditor();
      const { result, rerender } = renderHook(useHarness, {
        initialProps: {
          editor,
          escapePress: 0,
          clearComposer,
        } as HarnessProps,
      });
      // Empty: nothing to clear, nothing arms.
      rerender({ editor, escapePress: 1, clearComposer });
      expect(result.current.escapeArmed).toBe(false);

      act(() => result.current.setText("draft"));
      rerender({ editor, escapePress: 2, clearComposer });
      expect(result.current.escapeArmed).toBe(true);
      // Typing disarms: the following press is a first press.
      act(() => result.current.setText("draft!"));
      expect(result.current.escapeArmed).toBe(false);
      rerender({ editor, escapePress: 3, clearComposer });
      expect(clearComposer).not.toHaveBeenCalled();
      expect(result.current.escapeArmed).toBe(true);
    });

    it("ignores the counter's initial value on mount", () => {
      const clearComposer = vi.fn();
      const { result } = renderHook(useHarness, {
        initialProps: {
          editor: fakeEditor("typed"),
          escapePress: 7,
          clearComposer,
        } as HarnessProps,
      });
      expect(result.current.escapeArmed).toBe(false);
      expect(clearComposer).not.toHaveBeenCalled();
    });
  });
});

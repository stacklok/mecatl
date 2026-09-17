"use client";

import type { Editor } from "@tiptap/react";
import { type RefObject, useEffect, useRef, useState } from "react";
import {
  DISARMED_ESCAPE,
  ESCAPE_ARM_MS,
  type EscapeArm,
  pressEscapeToClear,
} from "@/app/workspace/chat/_components/escape-layering";
import {
  clearDraft,
  draftStorageKey,
  readDraft,
  writeDraft,
} from "@/lib/draft-store";
import { setComposerText } from "./composer-mentions";

/** How long an edit sits before it is persisted (a keystroke burst writes once). */
export const DRAFT_WRITE_DEBOUNCE_MS = 300;

/** The armed hint under the field after the first Esc. */
export const ESCAPE_ARMED_HINT = "Press Esc again to clear the draft";

export interface ComposerDraftGuardsInput {
  editor: Editor | null;
  /** The composer's plain-text mirror of the editor (every edit flows
      through it, so it is the one signal this hook needs). */
  text: string;
  /** Updates that mirror after this hook rewrites the editor (a restore). */
  setText: (text: string) => void;
  /** The forwarded double-Esc counter (`useComposerEscape`); each change is
      one Esc press that nothing else claimed. Absent = no Esc arm. */
  escapePress?: number;
  /** The draft's persistence identity (a session id, or `"new"` for the
      draft view); absent = the draft is not persisted (the thread panel). */
  draftKey?: string;
  /** A seed (`initialText`) is about to land: never restore over it. */
  initialTextPending: boolean;
  /** Fires when "the composer holds text" flips, so the surface can arm the
      leave guard and the Esc layering; also fires `false` on unmount. */
  onDraftChange?: (hasText: boolean) => void;
  /** Empties the composer (editor content, text mirror, staged files). */
  clearComposer: () => void;
}

type TimerRef = RefObject<ReturnType<typeof setTimeout> | null>;

function clearTimer(ref: TimerRef) {
  if (ref.current !== null) {
    clearTimeout(ref.current);
    ref.current = null;
  }
}

/**
 * The composer's draft guards, three arms over one text mirror:
 *
 * 1. Double-Esc clear — each forwarded `escapePress` runs the pure arm
 *    machine (`pressEscapeToClear`): the first press arms (a hint shows
 *    under the field), a second within `ESCAPE_ARM_MS` empties the composer,
 *    any edit disarms. An empty composer ignores the press.
 * 2. Draft persistence — under `draftKey`, the text is written to
 *    sessionStorage (debounced; a blank composer removes the entry at once),
 *    flushed on `pagehide`, on unmount and when the key changes, and
 *    restored into an EMPTY editor once it mounts. A pending seed wins over
 *    a stored draft.
 * 3. `onDraftChange` — the has-text signal the surface lifts into the
 *    `beforeunload` guard and the Esc layering.
 */
export function useComposerDraftGuards(input: ComposerDraftGuardsInput): {
  escapeArmed: boolean;
} {
  const {
    editor,
    text,
    setText,
    escapePress,
    draftKey,
    initialTextPending,
    onDraftChange,
    clearComposer,
  } = input;
  const [escapeArmed, setEscapeArmed] = useState(false);

  const textRef = useRef(text);
  const prevTextRef = useRef(text);
  const keyRef = useRef<string | null>(null);
  const writeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const armRef = useRef<EscapeArm>(DISARMED_ESCAPE);
  const armTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastEscapePressRef = useRef(escapePress);
  const hadTextRef = useRef(false);
  const onDraftChangeRef = useRef(onDraftChange);
  const clearComposerRef = useRef(clearComposer);

  // Latest callbacks, read from effects and the pagehide listener (kept off
  // the render phase for the React Compiler).
  useEffect(() => {
    onDraftChangeRef.current = onDraftChange;
    clearComposerRef.current = clearComposer;
  });

  // Every edit: the has-text signal, disarm a pending Esc, persist.
  useEffect(() => {
    textRef.current = text;
    const hasText = text.trim() !== "";
    if (hasText !== hadTextRef.current) {
      hadTextRef.current = hasText;
      onDraftChangeRef.current?.(hasText);
    }
    // The initial run (and a StrictMode replay) carries no edit: persisting
    // here would delete a stored draft before the restore below reads it.
    if (prevTextRef.current === text) return;
    prevTextRef.current = text;
    if (armRef.current.armedUntil > 0) {
      armRef.current = DISARMED_ESCAPE;
      clearTimer(armTimerRef);
      setEscapeArmed(false);
    }
    const key = keyRef.current;
    if (!key) return;
    clearTimer(writeTimerRef);
    if (!hasText) {
      clearDraft(key);
      return;
    }
    writeTimerRef.current = setTimeout(() => {
      writeTimerRef.current = null;
      if (keyRef.current === key) writeDraft(key, textRef.current);
    }, DRAFT_WRITE_DEBOUNCE_MS);
  }, [text]);

  // The persistence identity: a key change flushes the previous draft, and
  // an empty editor takes the stored draft for the new key (unless a seed is
  // about to land, which wins).
  useEffect(() => {
    const nextKey = draftKey ? draftStorageKey(draftKey) : null;
    const prevKey = keyRef.current;
    if (prevKey && prevKey !== nextKey) {
      // Flush what is typed under the key it was typed under. A blank
      // composer has nothing to flush — an edit that emptied it already
      // removed the entry, and a never-edited blank must not delete a draft
      // the user has not seen.
      clearTimer(writeTimerRef);
      if (textRef.current.trim() !== "") writeDraft(prevKey, textRef.current);
    }
    keyRef.current = nextKey;
    if (!editor || !nextKey || initialTextPending || !editor.isEmpty) return;
    const stored = readDraft(nextKey);
    if (!stored) return;
    setComposerText(editor, stored);
    setText(editor.getText({ blockSeparator: "\n" }));
  }, [editor, draftKey, initialTextPending, setText]);

  // One forwarded Esc: arm, or clear inside the window.
  useEffect(() => {
    if (escapePress === undefined || escapePress === lastEscapePressRef.current)
      return;
    lastEscapePressRef.current = escapePress;
    if (textRef.current.trim() === "") return;
    const { state, clear } = pressEscapeToClear(armRef.current, Date.now());
    armRef.current = state;
    clearTimer(armTimerRef);
    if (clear) {
      clearComposerRef.current();
      if (keyRef.current) clearDraft(keyRef.current);
      setEscapeArmed(false);
      return;
    }
    armTimerRef.current = setTimeout(() => {
      armTimerRef.current = null;
      armRef.current = DISARMED_ESCAPE;
      setEscapeArmed(false);
    }, ESCAPE_ARM_MS);
    setEscapeArmed(true);
  }, [escapePress]);

  // Flush a pending write when the page hides and when the composer leaves;
  // the surface's has-text signal drops with it (the draft is stored, so a
  // leave no longer loses it).
  useEffect(() => {
    const flush = () => {
      clearTimer(writeTimerRef);
      if (keyRef.current && textRef.current.trim() !== "")
        writeDraft(keyRef.current, textRef.current);
    };
    window.addEventListener("pagehide", flush);
    return () => {
      window.removeEventListener("pagehide", flush);
      flush();
      clearTimer(armTimerRef);
      if (hadTextRef.current) {
        hadTextRef.current = false;
        onDraftChangeRef.current?.(false);
      }
    };
  }, []);

  return { escapeArmed };
}

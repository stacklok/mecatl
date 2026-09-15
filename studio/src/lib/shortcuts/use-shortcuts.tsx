"use client";

import { createContext, useContext, useEffect, useRef } from "react";
import { comboFiresWhileTyping, matchCombo, SHORTCUTS } from "./registry";

type Registry = {
  register: (id: string, handler: () => void) => void;
  unregister: (id: string) => void;
};

const ShortcutContext = createContext<Registry | null>(null);

function isTyping(el: Element | null): boolean {
  const node = el as HTMLElement | null;
  return (
    !!node &&
    (node.tagName === "INPUT" ||
      node.tagName === "TEXTAREA" ||
      node.isContentEditable)
  );
}

/**
 * Owns the single global keydown listener. Resolves each event against the
 * shortcut registry and calls the handler a component registered for that id.
 * Non-modifier shortcuts (except Esc) are suppressed while the user is typing,
 * and an event something closer to the key already consumed — a Radix
 * dialog/menu dismissing on Escape, the composer's autocomplete menu — is
 * skipped via `defaultPrevented`, so those layers always win over globals.
 */
export function ShortcutsProvider({ children }: { children: React.ReactNode }) {
  const handlers = useRef(new Map<string, () => void>());

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing) return;
      const typing = isTyping(document.activeElement);
      for (const def of SHORTCUTS) {
        const handler = handlers.current.get(def.id);
        if (!handler) continue;
        if (typing && !comboFiresWhileTyping(def.combo)) continue;
        if (matchCombo(def.combo, e)) {
          e.preventDefault();
          handler();
          return;
        }
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  const value = useRef<Registry>({
    register: (id, handler) => handlers.current.set(id, handler),
    unregister: (id) => handlers.current.delete(id),
  }).current;

  return (
    <ShortcutContext.Provider value={value}>
      {children}
    </ShortcutContext.Provider>
  );
}

/**
 * Register a handler for a shortcut id. The latest handler is always used (no
 * re-registration churn), and it's removed on unmount.
 */
export function useShortcut(id: string, handler: () => void) {
  const ctx = useContext(ShortcutContext);
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    if (!ctx) return;
    const stable = () => ref.current();
    ctx.register(id, stable);
    return () => ctx.unregister(id);
  }, [id, ctx]);
}

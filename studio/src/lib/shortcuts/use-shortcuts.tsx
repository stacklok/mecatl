"use client";

import { createContext, useContext, useEffect, useRef } from "react";
import { useShortcutBindings } from "./keymap";
import { comboFiresWhileTyping, matchCombo } from "./registry";

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
  // EFFECTIVE bindings (registry defaults + the user's keymap overrides,
  // Settings → Keyboard), mirrored into a ref so the one listener below
  // always matches the current keys without re-subscribing.
  const { bindings } = useShortcutBindings();
  const bindingsRef = useRef(bindings);
  bindingsRef.current = bindings;

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing) return;
      const typing = isTyping(document.activeElement);
      for (const def of bindingsRef.current) {
        const handler = handlers.current.get(def.id);
        if (!handler) continue;
        if (typing && !comboFiresWhileTyping(def.effectiveCombo)) continue;
        if (matchCombo(def.effectiveCombo, e)) {
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
 * re-registration churn), and it's removed on unmount. `enabled: false`
 * withdraws the registration entirely — the dispatcher then neither claims
 * the key nor prevents its default — for a shortcut that only means
 * something in a state (the approval verdicts while an ask is waiting).
 */
export function useShortcut(
  id: string,
  handler: () => void,
  options?: { enabled?: boolean },
) {
  const ctx = useContext(ShortcutContext);
  const enabled = options?.enabled ?? true;
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    if (!ctx || !enabled) return;
    const stable = () => ref.current();
    ctx.register(id, stable);
    return () => ctx.unregister(id);
  }, [id, ctx, enabled]);
}

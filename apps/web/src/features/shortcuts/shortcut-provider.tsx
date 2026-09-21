// SPDX-License-Identifier: Apache-2.0

import { createContext, type ReactNode, useContext, useEffect, useRef } from "react";
import {
  matchesShortcut,
  type ShortcutId,
  shortcutRegistry,
  shortcutWorksWhileTyping,
} from "./shortcut-registry";

interface ShortcutHandlers {
  register: (id: ShortcutId, handler: () => void) => void;
  unregister: (id: ShortcutId) => void;
}

const ShortcutContext = createContext<ShortcutHandlers | null>(null);

export function ShortcutProvider({ children }: { children: ReactNode }) {
  const handlers = useRef(new Map<ShortcutId, () => void>());

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.isComposing || event.repeat) return;
      const typing = isTyping(document.activeElement);
      for (const shortcut of shortcutRegistry) {
        const handler = handlers.current.get(shortcut.id);
        if (!handler || (typing && !shortcutWorksWhileTyping(shortcut.combo))) continue;
        if (!matchesShortcut(shortcut.combo, event)) continue;
        event.preventDefault();
        handler();
        return;
      }
    }

    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  const context = useRef<ShortcutHandlers>({
    register: (id, handler) => handlers.current.set(id, handler),
    unregister: (id) => handlers.current.delete(id),
  }).current;

  return <ShortcutContext.Provider value={context}>{children}</ShortcutContext.Provider>;
}

export function useShortcut(id: ShortcutId, handler: () => void) {
  const context = useContext(ShortcutContext);
  const latestHandler = useRef(handler);
  latestHandler.current = handler;

  useEffect(() => {
    if (!context) return;
    const stableHandler = () => latestHandler.current();
    context.register(id, stableHandler);
    return () => context.unregister(id);
  }, [context, id]);
}

function isTyping(element: Element | null): boolean {
  const node = element as HTMLElement | null;
  return Boolean(
    node &&
      (node.tagName === "INPUT" ||
        node.tagName === "TEXTAREA" ||
        node.tagName === "SELECT" ||
        node.isContentEditable),
  );
}

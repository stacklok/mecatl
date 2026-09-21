// SPDX-License-Identifier: Apache-2.0

import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
} from "react";
import {
  matchesShortcut,
  type ShortcutId,
  shortcutAllowedByScopes,
  shortcutRegistry,
  shortcutWorksWhileTyping,
} from "./shortcut-registry";

interface ShortcutHandlers {
  register: (id: ShortcutId, handler: () => void) => void;
  /** Opens a modal scope that permits only `allowed`; returns its release. */
  suppress: (allowed: ReadonlySet<ShortcutId>) => () => void;
  unregister: (id: ShortcutId) => void;
}

const ShortcutContext = createContext<ShortcutHandlers | null>(null);

const NO_SHORTCUTS: readonly ShortcutId[] = [];

export function ShortcutProvider({ children }: { children: ReactNode }) {
  const handlers = useRef(new Map<ShortcutId, () => void>());
  const scopes = useRef(new Set<ReadonlySet<ShortcutId>>());

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.isComposing || event.repeat) return;
      const typing = isTyping(document.activeElement);
      for (const shortcut of shortcutRegistry) {
        const handler = handlers.current.get(shortcut.id);
        if (!handler || (typing && !shortcutWorksWhileTyping(shortcut.combo))) continue;
        if (!shortcutAllowedByScopes(shortcut.id, scopes.current)) continue;
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
    suppress: (allowed) => {
      // A fresh Set per call, so two scopes with equal allow-lists stay distinct.
      const scope = new Set(allowed);
      scopes.current.add(scope);
      return () => {
        scopes.current.delete(scope);
      };
    },
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

/**
 * While `active`, blocks every registered shortcut except `allowed`. A modal
 * surface (dialog, palette) uses this so app-wide bindings such as Escape or
 * the arrow keys cannot act on the page behind it. Pass a stable (module-level)
 * `allowed` array. The scope is installed in a layout effect so it is in place
 * before the next keystroke can be dispatched.
 */
export function useShortcutSuppression(
  active: boolean,
  allowed: readonly ShortcutId[] = NO_SHORTCUTS,
) {
  const context = useContext(ShortcutContext);

  useLayoutEffect(() => {
    if (!context || !active) return;
    return context.suppress(new Set(allowed));
  }, [active, allowed, context]);
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

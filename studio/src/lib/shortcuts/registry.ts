/**
 * The single source of truth for keyboard shortcuts. The dispatcher matches
 * live key events against these combos, and the docs page renders its keycaps
 * from the same list — so the documentation can't drift from the behaviour.
 *
 * A shortcut only fires if a component has registered a handler for its `id`
 * (see `useShortcut`); entries without a handler are documentation-only (their
 * behaviour lives inside a component, e.g. Enter to send in the composer).
 *
 * Combos are `+`-joined tokens: `mod` (⌘ on macOS / Ctrl elsewhere), `shift`,
 * `alt`, then a key (`k`, `up`, `enter`, `?`, `/`, `,`, `@`, `esc`). `keycaps()`
 * derives the display and `matchCombo()` matches an event — one grammar, no
 * duplicated key lists.
 */

export interface ShortcutDef {
  readonly id: string;
  readonly combo: string;
  readonly description: string;
  readonly group: string;
}

export const SHORTCUTS: readonly ShortcutDef[] = [
  // General
  {
    id: "search.open",
    combo: "mod+k",
    description: "Open search",
    group: "General",
  },
  {
    id: "settings.open",
    combo: "mod+,",
    description: "Open settings",
    group: "General",
  },
  {
    id: "shortcuts.open",
    combo: "?",
    description: "Show keyboard shortcuts",
    group: "General",
  },
  {
    id: "shortcuts.open.mod",
    combo: "mod+/",
    description: "Show keyboard shortcuts (also while typing)",
    group: "General",
  },
  {
    id: "chat.toggleList",
    combo: "mod+b",
    description: "Toggle the chat list",
    group: "General",
  },
  // Esc is layered: an open dialog/menu handles its own Escape first (Radix
  // and the composer's autocomplete both consume the event, so the dispatcher
  // never sees it); this binding is the fallback beneath them.
  {
    id: "close.esc",
    combo: "esc",
    description: "Close the side panel — or stop the running turn",
    group: "General",
  },

  // Chats
  // NB: avoid browser-reserved combos. ⌘N / ⌘⇧N open a new (incognito) window
  // and can't be intercepted, so "New chat" uses ⌘⇧O (preventable) — the same
  // shortcut other web chat apps use.
  {
    id: "chat.new",
    combo: "mod+shift+o",
    description: "New chat",
    group: "Chats",
  },
  {
    id: "chat.prev",
    combo: "up",
    description: "Previous chat",
    group: "Chats",
  },
  { id: "chat.next", combo: "down", description: "Next chat", group: "Chats" },
  {
    id: "chat.next.vim",
    combo: "j",
    description: "Next chat (vim-style)",
    group: "Chats",
  },
  {
    id: "chat.prev.vim",
    combo: "k",
    description: "Previous chat (vim-style)",
    group: "Chats",
  },

  // Composer (behaviour lives in the composer; documentation-only here)
  {
    id: "composer.send",
    combo: "enter",
    description:
      "Send — while the agent is replying: queue or steer, per Settings → Personalize",
    group: "Composer",
  },
  {
    id: "composer.newline",
    combo: "shift+enter",
    description:
      "Insert a new line — while the agent is replying: the opposite of your Enter preference",
    group: "Composer",
  },
  {
    id: "composer.slash",
    combo: "/",
    description: "Slash commands and skills",
    group: "Composer",
  },
  {
    id: "composer.mention",
    combo: "@",
    description: "Mention an agent",
    group: "Composer",
  },
] as const;

/** Groups in render order. */
export const SHORTCUT_GROUPS = ["General", "Chats", "Composer"] as const;

const CAP_LABEL: Record<string, string> = {
  mod: "⌘",
  shift: "⇧",
  alt: "⌥",
  up: "↑",
  down: "↓",
  left: "←",
  right: "→",
  enter: "Enter",
  esc: "Esc",
};

/** Display keycaps for a combo, e.g. "mod+k" → ["⌘", "K"]. */
export function keycaps(combo: string): string[] {
  return combo
    .split("+")
    .map((p) => CAP_LABEL[p] ?? (p.length === 1 ? p.toUpperCase() : p));
}

const KEY_ALIAS: Record<string, string> = {
  up: "arrowup",
  down: "arrowdown",
  left: "arrowleft",
  right: "arrowright",
  esc: "escape",
};

/** True when a live key event matches a combo. `mod` = ⌘ or Ctrl. */
export function matchCombo(combo: string, e: KeyboardEvent): boolean {
  const parts = combo.split("+");
  const key = parts[parts.length - 1];
  const wantMod = parts.includes("mod");
  const wantShift = parts.includes("shift");
  const wantAlt = parts.includes("alt");
  const hasMod = e.metaKey || e.ctrlKey;
  if (wantMod !== hasMod) return false;
  if (wantAlt !== e.altKey) return false;
  // Only enforce shift when the combo asks for it — symbol keys like "?" carry
  // their own implicit shift in `e.key`.
  if (wantShift && !e.shiftKey) return false;
  return e.key.toLowerCase() === (KEY_ALIAS[key] ?? key);
}

/**
 * True when the combo may fire while the user is typing in an editable field:
 * combos carrying `mod` (the standard desktop-app rule — ⌘/Ctrl chords are
 * commands, not text), plus bare `esc` (it never inserts text, and Esc must
 * interrupt a streaming run even while the caret sits in the composer).
 */
export function comboFiresWhileTyping(combo: string): boolean {
  return combo.split("+").includes("mod") || combo === "esc";
}

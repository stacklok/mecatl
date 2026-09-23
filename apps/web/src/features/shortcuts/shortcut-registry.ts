// SPDX-License-Identifier: Apache-2.0

export interface ShortcutDefinition {
  combo: string;
  description: string;
  group: "Chats" | "Composer" | "General";
  id: string;
}

export const shortcutRegistry = [
  {
    combo: "mod+k",
    description: "Open workspace search",
    group: "General",
    id: "search.open",
  },
  {
    combo: "mod+,",
    description: "Open settings",
    group: "General",
    id: "settings.open",
  },
  {
    combo: "?",
    description: "Show keyboard shortcuts",
    group: "General",
    id: "shortcuts.open",
  },
  {
    combo: "mod+/",
    description: "Show keyboard shortcuts while typing",
    group: "General",
    id: "shortcuts.open.mod",
  },
  {
    combo: "mod+b",
    description: "Toggle the chat list",
    group: "General",
    id: "chat.toggleList",
  },
  {
    combo: "esc",
    description: "Close the chat list or stop the running turn",
    group: "General",
    id: "close.esc",
  },
  {
    combo: "mod+shift+o",
    description: "New chat",
    group: "Chats",
    id: "chat.new",
  },
  {
    combo: "up",
    description: "Previous chat",
    group: "Chats",
    id: "chat.prev",
  },
  {
    combo: "down",
    description: "Next chat",
    group: "Chats",
    id: "chat.next",
  },
  {
    combo: "k",
    description: "Previous chat (vim-style)",
    group: "Chats",
    id: "chat.prev.vim",
  },
  {
    combo: "j",
    description: "Next chat (vim-style)",
    group: "Chats",
    id: "chat.next.vim",
  },
  {
    combo: "l",
    description: "Jump to the most recent chat",
    group: "Chats",
    id: "chat.latest",
  },
  {
    combo: "c",
    description: "Copy the open chat's session ID",
    group: "Chats",
    id: "chat.copyId",
  },
  {
    combo: "mod+shift+s",
    description: "Fork the open chat as-is — continue in a copy",
    group: "Chats",
    id: "chat.fork",
  },
  {
    combo: "mod+shift+x",
    description: "Clear conversation — a fresh chat with the same settings",
    group: "Chats",
    id: "chat.clear",
  },
  {
    combo: "mod+shift+g",
    description: "Expand or collapse details — tool rows, reasoning, raw errors",
    group: "Chats",
    id: "chat.expandDetails",
  },
  {
    combo: "enter",
    description: "Send message",
    group: "Composer",
    id: "composer.send",
  },
  {
    combo: "shift+enter",
    description: "Insert a new line",
    group: "Composer",
    id: "composer.newline",
  },
] as const satisfies readonly ShortcutDefinition[];

export type ShortcutId = (typeof shortcutRegistry)[number]["id"];

export const shortcutGroups = ["General", "Chats", "Composer"] as const;

const keycapLabels: Record<string, string> = {
  alt: "⌥",
  down: "↓",
  enter: "Enter",
  esc: "Esc",
  left: "←",
  mod: "⌘",
  right: "→",
  shift: "⇧",
  up: "↑",
};

const keyAliases: Record<string, string> = {
  down: "arrowdown",
  esc: "escape",
  left: "arrowleft",
  right: "arrowright",
  up: "arrowup",
};

export function keycaps(combo: string, mac = true): string[] {
  return combo
    .split("+")
    .map((part) =>
      part === "mod" && !mac
        ? "Ctrl"
        : (keycapLabels[part] ?? (part.length === 1 ? part.toLocaleUpperCase() : part)),
    );
}

/**
 * A single printable punctuation or symbol character, such as `?`, `/`, `,`,
 * or `[`. Which of these a key produces already depends on Shift (and on the
 * keyboard layout), so `KeyboardEvent.key` is the whole truth for them.
 */
const punctuationKey = /^[\p{P}\p{S}]$/u;

/**
 * Whether a keyboard event is exactly the combination a binding documents.
 *
 * - `mod` is the primary modifier: exactly one of Ctrl or Meta must be held
 *   when the binding asks for it, and neither when it does not, so Ctrl and
 *   Meta pressed together never match.
 * - Alt must match exactly.
 * - Shift must match exactly for letters, digits, and named keys. For
 *   punctuation the produced `key` already encodes Shift, so the binding
 *   matches by that character alone: `?` matches the key `?` and `/` does not.
 */
export function matchesShortcut(
  combo: string,
  event: Pick<KeyboardEvent, "altKey" | "ctrlKey" | "key" | "metaKey" | "shiftKey">,
): boolean {
  const parts = combo.split("+");
  const key = parts.at(-1) ?? "";
  const wantsModifier = parts.includes("mod");
  const wantsShift = parts.includes("shift");
  const wantsAlt = parts.includes("alt");
  if (event.metaKey && event.ctrlKey) return false;
  if (wantsModifier !== (event.metaKey || event.ctrlKey)) return false;
  if (wantsAlt !== event.altKey) return false;
  if (!punctuationKey.test(key) && wantsShift !== event.shiftKey) return false;
  return event.key.toLocaleLowerCase() === (keyAliases[key] ?? key);
}

/**
 * Whether a shortcut may fire while modal scopes (dialogs, palettes) are
 * open. Each open scope lists the shortcuts it still permits; a shortcut
 * fires only when every open scope permits it, so with no scope open every
 * shortcut is allowed.
 */
export function shortcutAllowedByScopes(
  id: ShortcutId,
  scopes: Iterable<ReadonlySet<ShortcutId>>,
): boolean {
  for (const allowed of scopes) {
    if (!allowed.has(id)) return false;
  }
  return true;
}

export function shortcutWorksWhileTyping(combo: string): boolean {
  return combo.split("+").includes("mod") || combo === "esc";
}

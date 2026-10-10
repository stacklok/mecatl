// SPDX-License-Identifier: Apache-2.0

import type { EnterSendBehavior } from "@/lib/profile-preferences";

export interface ShortcutDefinition {
  combo: string;
  description: string;
  /**
   * Documentation only: no `useShortcut` handler dispatches this id. The
   * behaviour lives in a component's own key handling (the composer's
   * `onKeyDown`, `chat-escape.ts`), so the row describes it rather than
   * binding it.
   */
  fixed?: true;
  group: "Chats" | "Composer" | "Conversation" | "General";
  id: string;
  /**
   * The literal key is load-bearing and must never be rebound: Escape's
   * layering, its while-typing exemption, and Radix's overlay handling all
   * depend on it being Esc. Studio has no rebinding UI, so the flag is a
   * guard for the day one is added.
   */
  locked?: true;
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
  // Escape is handled by `chat-escape.ts` on chat surfaces only, so the reference
  // lists it under Conversation. It keeps its place in this array so the
  // dispatcher's scan order is unchanged.
  {
    combo: "esc",
    description: "Act on the top chat layer (see Escape order below)",
    fixed: true,
    group: "Conversation",
    id: "close.esc",
    locked: true,
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
  // Conversation: keys that act on the open conversation rather than the chat list.
  {
    combo: "mod+shift+g",
    description: "Expand or collapse details — tool rows, reasoning, raw errors",
    group: "Conversation",
    id: "chat.expandDetails",
  },
  {
    combo: "enter",
    description: "Send message",
    fixed: true,
    group: "Composer",
    id: "composer.send",
  },
  {
    combo: "shift+enter",
    description: "Insert a new line",
    fixed: true,
    group: "Composer",
    id: "composer.newline",
  },
] as const satisfies readonly ShortcutDefinition[];

export type ShortcutId = (typeof shortcutRegistry)[number]["id"];

export const shortcutGroups = ["General", "Chats", "Conversation", "Composer"] as const;

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

/**
 * The description the reference page shows for a shortcut. The two composer
 * rows follow the user's Enter preference (`useEnterSendBehavior`), so the page
 * says what Enter and Shift+Enter do while the agent is replying instead of
 * pointing at the setting. Mirrors `resolveComposerAction` in `chat-composer.tsx`:
 * Enter does the preference and Shift+Enter the opposite. Every other row keeps
 * its registry description.
 */
export function describeShortcut(
  shortcut: Pick<ShortcutDefinition, "description" | "id">,
  enterBehavior: EnterSendBehavior,
): string {
  const steer = "steer the agent";
  const queue = "queue the message";
  switch (shortcut.id) {
    case "composer.send":
      return `${shortcut.description} — while the agent is replying: ${enterBehavior === "steer" ? steer : queue}`;
    case "composer.newline":
      return `${shortcut.description} — while the agent is replying: ${enterBehavior === "steer" ? queue : steer}`;
    default:
      return shortcut.description;
  }
}

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

export function matchesShortcut(
  combo: string,
  event: Pick<KeyboardEvent, "altKey" | "ctrlKey" | "key" | "metaKey" | "shiftKey">,
): boolean {
  const parts = combo.split("+");
  const key = parts.at(-1) ?? "";
  const wantsModifier = parts.includes("mod");
  const wantsShift = parts.includes("shift");
  const wantsAlt = parts.includes("alt");
  if (wantsModifier !== (event.metaKey || event.ctrlKey)) return false;
  if (wantsAlt !== event.altKey) return false;
  if (wantsShift && !event.shiftKey) return false;
  return event.key.toLocaleLowerCase() === (keyAliases[key] ?? key);
}

export function shortcutWorksWhileTyping(combo: string): boolean {
  return combo.split("+").includes("mod") || combo === "esc";
}

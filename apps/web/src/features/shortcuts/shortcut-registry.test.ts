// SPDX-License-Identifier: Apache-2.0

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { resolveComposerAction } from "@/features/chat/chat-composer";
import {
  describeShortcut,
  keycaps,
  matchesShortcut,
  type ShortcutDefinition,
  type ShortcutId,
  shortcutAllowedByScopes,
  shortcutGroups,
  shortcutRegistry,
  shortcutWorksWhileTyping,
} from "./shortcut-registry";

function event(
  key: string,
  modifiers: Partial<{ alt: boolean; ctrl: boolean; meta: boolean; shift: boolean }> = {},
) {
  return {
    altKey: modifiers.alt ?? false,
    ctrlKey: modifiers.ctrl ?? false,
    key,
    metaKey: modifiers.meta ?? false,
    shiftKey: modifiers.shift ?? false,
  };
}

describe("shortcut registry", () => {
  it("has unique identifiers in known groups", () => {
    expect(new Set(shortcutRegistry.map((shortcut) => shortcut.id)).size).toBe(
      shortcutRegistry.length,
    );
    for (const shortcut of shortcutRegistry) expect(shortcutGroups).toContain(shortcut.group);
  });

  it("pins app-wide and chat bindings to preventable browser combos", () => {
    const combos = new Map(shortcutRegistry.map((shortcut) => [shortcut.id, shortcut.combo]));
    expect(combos.get("search.open")).toBe("mod+k");
    expect(combos.get("settings.open")).toBe("mod+,");
    expect(combos.get("shortcuts.open")).toBe("?");
    expect(combos.get("chat.new")).toBe("mod+shift+o");
    expect([...combos.values()]).not.toContain("mod+n");
  });
});

describe("keycaps", () => {
  it("renders platform modifiers and named keys", () => {
    expect(keycaps("mod+k", true)).toEqual(["⌘", "K"]);
    expect(keycaps("mod+k", false)).toEqual(["Ctrl", "K"]);
    expect(keycaps("mod+shift+o", true)).toEqual(["⌘", "⇧", "O"]);
    expect(keycaps("down", true)).toEqual(["↓"]);
  });
});

describe("matchesShortcut", () => {
  it("matches primary modifiers, plain keys, aliases, and punctuation", () => {
    expect(matchesShortcut("mod+k", event("k", { meta: true }))).toBe(true);
    expect(matchesShortcut("mod+k", event("k", { ctrl: true }))).toBe(true);
    expect(matchesShortcut("down", event("ArrowDown"))).toBe(true);
    expect(matchesShortcut("?", event("?", { shift: true }))).toBe(true);
    expect(matchesShortcut("mod+,", event(",", { meta: true }))).toBe(true);
  });

  it("rejects missing, extra, and incomplete modifiers", () => {
    expect(matchesShortcut("mod+k", event("k"))).toBe(false);
    expect(matchesShortcut("j", event("j", { meta: true }))).toBe(false);
    expect(matchesShortcut("mod+shift+o", event("o", { meta: true }))).toBe(false);
  });

  it("rejects an extra Shift on a letter binding", () => {
    expect(matchesShortcut("mod+k", event("K", { meta: true, shift: true }))).toBe(false);
    expect(matchesShortcut("mod+k", event("k", { ctrl: true, shift: true }))).toBe(false);
    expect(matchesShortcut("j", event("J", { shift: true }))).toBe(false);
  });

  it("rejects an extra Shift on a named key binding", () => {
    expect(matchesShortcut("enter", event("Enter", { shift: true }))).toBe(false);
    expect(matchesShortcut("down", event("ArrowDown", { shift: true }))).toBe(false);
    expect(matchesShortcut("esc", event("Escape", { shift: true }))).toBe(false);
  });

  it("rejects an extra Shift on a digit binding", () => {
    expect(matchesShortcut("mod+1", event("1", { meta: true }))).toBe(true);
    expect(matchesShortcut("mod+1", event("1", { meta: true, shift: true }))).toBe(false);
  });

  it("rejects Ctrl and Meta pressed together", () => {
    expect(matchesShortcut("mod+k", event("k", { ctrl: true, meta: true }))).toBe(false);
    expect(
      matchesShortcut("mod+shift+o", event("o", { ctrl: true, meta: true, shift: true })),
    ).toBe(false);
    expect(matchesShortcut("k", event("k", { ctrl: true, meta: true }))).toBe(false);
  });

  it("requires Alt exactly as the binding asks", () => {
    expect(matchesShortcut("mod+k", event("k", { alt: true, meta: true }))).toBe(false);
    expect(matchesShortcut("alt+k", event("k", { alt: true }))).toBe(true);
    expect(matchesShortcut("alt+k", event("k"))).toBe(false);
  });

  it("matches shifted punctuation by its produced key", () => {
    expect(matchesShortcut("?", event("?", { shift: true }))).toBe(true);
    expect(matchesShortcut("?", event("?"))).toBe(true);
    expect(matchesShortcut("?", event("/"))).toBe(false);
    expect(matchesShortcut("mod+/", event("/", { meta: true }))).toBe(true);
    expect(matchesShortcut("mod+/", event("?", { meta: true, shift: true }))).toBe(false);
    expect(matchesShortcut("mod+,", event("<", { meta: true, shift: true }))).toBe(false);
    expect(matchesShortcut("[", event("[", { shift: false }))).toBe(true);
    expect(matchesShortcut("[", event("{", { shift: true }))).toBe(false);
  });

  it("matches every registered binding by the key combination it documents", () => {
    const documented: Record<
      ShortcutId,
      { key: string; modifiers: { primary?: boolean; shift?: boolean } }
    > = {
      "chat.clear": { key: "x", modifiers: { primary: true, shift: true } },
      "chat.copyId": { key: "c", modifiers: {} },
      "chat.expandDetails": { key: "g", modifiers: { primary: true, shift: true } },
      "chat.fork": { key: "s", modifiers: { primary: true, shift: true } },
      "chat.latest": { key: "l", modifiers: {} },
      "chat.new": { key: "o", modifiers: { primary: true, shift: true } },
      "chat.next": { key: "ArrowDown", modifiers: {} },
      "chat.next.vim": { key: "j", modifiers: {} },
      "chat.prev": { key: "ArrowUp", modifiers: {} },
      "chat.prev.vim": { key: "k", modifiers: {} },
      "chat.toggleList": { key: "b", modifiers: { primary: true } },
      "close.esc": { key: "Escape", modifiers: {} },
      "composer.newline": { key: "Enter", modifiers: { shift: true } },
      "composer.send": { key: "Enter", modifiers: {} },
      "search.open": { key: "k", modifiers: { primary: true } },
      "settings.open": { key: ",", modifiers: { primary: true } },
      "shortcuts.open": { key: "?", modifiers: { shift: true } },
      "shortcuts.open.mod": { key: "/", modifiers: { primary: true } },
    };
    expect(Object.keys(documented).sort()).toEqual(shortcutRegistry.map((s) => s.id).sort());
    for (const shortcut of shortcutRegistry) {
      const { key, modifiers } = documented[shortcut.id];
      const variants = modifiers.primary ? [{ meta: true }, { ctrl: true }] : [{}];
      for (const variant of variants) {
        const pressed = event(key, { shift: modifiers.shift, ...variant });
        const matching = shortcutRegistry.filter((other) => matchesShortcut(other.combo, pressed));
        expect(
          matching.map((other) => other.id),
          `${shortcut.combo} with ${JSON.stringify(pressed)}`,
        ).toEqual([shortcut.id]);
      }
    }
  });
});

describe("shortcutWorksWhileTyping", () => {
  it("allows primary-modifier commands and escape", () => {
    expect(shortcutWorksWhileTyping("mod+k")).toBe(true);
    expect(shortcutWorksWhileTyping("mod+shift+o")).toBe(true);
    expect(shortcutWorksWhileTyping("esc")).toBe(true);
    expect(shortcutWorksWhileTyping("j")).toBe(false);
  });
});

describe("shortcutAllowedByScopes", () => {
  it("allows every shortcut when no modal scope is open", () => {
    expect(shortcutAllowedByScopes("close.esc", [])).toBe(true);
    expect(shortcutAllowedByScopes("chat.next", [])).toBe(true);
  });

  it("blocks shortcuts an open scope does not permit", () => {
    const palette = new Set<ShortcutId>(["search.open", "settings.open"]);
    expect(shortcutAllowedByScopes("close.esc", [palette])).toBe(false);
    expect(shortcutAllowedByScopes("chat.next", [palette])).toBe(false);
    expect(shortcutAllowedByScopes("settings.open", [palette])).toBe(true);
  });

  it("requires every open scope to permit the shortcut", () => {
    const outer = new Set<ShortcutId>(["search.open", "settings.open"]);
    const inner = new Set<ShortcutId>();
    expect(shortcutAllowedByScopes("search.open", [outer, inner])).toBe(false);
  });
});

/**
 * Pins the registry as Studio ships it, so a restyle of the reference page cannot
 * silently change what a shortcut is, which key fires it, or where it may fire.
 */
describe("registry pin", () => {
  it("pins every shortcut's id, combo, group, description, and while-typing scope", () => {
    expect(
      shortcutRegistry.map(({ combo, description, group, id }) => ({
        combo,
        description,
        group,
        id,
        whileTyping: shortcutWorksWhileTyping(combo),
      })),
    ).toEqual([
      {
        combo: "mod+k",
        description: "Open workspace search",
        group: "General",
        id: "search.open",
        whileTyping: true,
      },
      {
        combo: "mod+,",
        description: "Open settings",
        group: "General",
        id: "settings.open",
        whileTyping: true,
      },
      {
        combo: "?",
        description: "Show keyboard shortcuts",
        group: "General",
        id: "shortcuts.open",
        whileTyping: false,
      },
      {
        combo: "mod+/",
        description: "Show keyboard shortcuts while typing",
        group: "General",
        id: "shortcuts.open.mod",
        whileTyping: true,
      },
      {
        combo: "mod+b",
        description: "Toggle the chat list",
        group: "General",
        id: "chat.toggleList",
        whileTyping: true,
      },
      {
        combo: "esc",
        description: "Act on the top chat layer (see Escape order below)",
        group: "Conversation",
        id: "close.esc",
        whileTyping: true,
      },
      {
        combo: "mod+shift+o",
        description: "New chat",
        group: "Chats",
        id: "chat.new",
        whileTyping: true,
      },
      {
        combo: "up",
        description: "Previous chat",
        group: "Chats",
        id: "chat.prev",
        whileTyping: false,
      },
      {
        combo: "down",
        description: "Next chat",
        group: "Chats",
        id: "chat.next",
        whileTyping: false,
      },
      {
        combo: "k",
        description: "Previous chat (vim-style)",
        group: "Chats",
        id: "chat.prev.vim",
        whileTyping: false,
      },
      {
        combo: "j",
        description: "Next chat (vim-style)",
        group: "Chats",
        id: "chat.next.vim",
        whileTyping: false,
      },
      {
        combo: "l",
        description: "Jump to the most recent chat",
        group: "Chats",
        id: "chat.latest",
        whileTyping: false,
      },
      {
        combo: "c",
        description: "Copy the open chat's session ID",
        group: "Chats",
        id: "chat.copyId",
        whileTyping: false,
      },
      {
        combo: "mod+shift+s",
        description: "Fork the open chat as-is — continue in a copy",
        group: "Chats",
        id: "chat.fork",
        whileTyping: true,
      },
      {
        combo: "mod+shift+x",
        description: "Clear conversation — a fresh chat with the same settings",
        group: "Chats",
        id: "chat.clear",
        whileTyping: true,
      },
      {
        combo: "mod+shift+g",
        description: "Expand or collapse details — tool rows, reasoning, raw errors",
        group: "Conversation",
        id: "chat.expandDetails",
        whileTyping: true,
      },
      {
        combo: "enter",
        description: "Send message",
        group: "Composer",
        id: "composer.send",
        whileTyping: false,
      },
      {
        combo: "shift+enter",
        description: "Insert a new line",
        group: "Composer",
        id: "composer.newline",
        whileTyping: false,
      },
    ]);
  });

  it("lists the reference groups in page order", () => {
    expect(shortcutGroups).toEqual(["General", "Chats", "Conversation", "Composer"]);
  });

  it("flags only component-owned rows as fixed and only Escape as locked", () => {
    const definitions: readonly ShortcutDefinition[] = shortcutRegistry;
    expect(definitions.filter((shortcut) => shortcut.fixed).map((shortcut) => shortcut.id)).toEqual(
      ["close.esc", "composer.send", "composer.newline"],
    );
    expect(
      definitions.filter((shortcut) => shortcut.locked).map((shortcut) => shortcut.id),
    ).toEqual(["close.esc"]);
  });

  it("pins the keycaps each shortcut renders on Mac and elsewhere", () => {
    expect(
      Object.fromEntries(
        shortcutRegistry.map((shortcut) => [
          shortcut.id,
          [keycaps(shortcut.combo, true).join(" "), keycaps(shortcut.combo, false).join(" ")],
        ]),
      ),
    ).toEqual({
      "chat.clear": ["⌘ ⇧ X", "Ctrl ⇧ X"],
      "chat.copyId": ["C", "C"],
      "chat.expandDetails": ["⌘ ⇧ G", "Ctrl ⇧ G"],
      "chat.fork": ["⌘ ⇧ S", "Ctrl ⇧ S"],
      "chat.latest": ["L", "L"],
      "chat.new": ["⌘ ⇧ O", "Ctrl ⇧ O"],
      "chat.next": ["↓", "↓"],
      "chat.next.vim": ["J", "J"],
      "chat.prev": ["↑", "↑"],
      "chat.prev.vim": ["K", "K"],
      "chat.toggleList": ["⌘ B", "Ctrl B"],
      "close.esc": ["Esc", "Esc"],
      "composer.newline": ["⇧ Enter", "⇧ Enter"],
      "composer.send": ["Enter", "Enter"],
      "search.open": ["⌘ K", "Ctrl K"],
      "settings.open": ["⌘ ,", "Ctrl ,"],
      "shortcuts.open": ["?", "?"],
      "shortcuts.open.mod": ["⌘ /", "Ctrl /"],
    });
  });

  it("pins modal-scope filtering for every shortcut", () => {
    for (const { id } of shortcutRegistry) {
      expect(shortcutAllowedByScopes(id, []), id).toBe(true);
      expect(shortcutAllowedByScopes(id, [new Set<ShortcutId>()]), id).toBe(false);
      expect(shortcutAllowedByScopes(id, [new Set<ShortcutId>([id])]), id).toBe(true);
      expect(
        shortcutAllowedByScopes(id, [new Set<ShortcutId>([id]), new Set<ShortcutId>()]),
        id,
      ).toBe(false);
    }
  });

  it("pins which shortcuts the app dispatches through useShortcut", () => {
    const sourceRoot = fileURLToPath(new URL("../..", import.meta.url));
    const registered = new Set<string>();
    for (const file of readdirSync(sourceRoot, { recursive: true }) as string[]) {
      if (!/\.tsx?$/.test(file) || /\.test\.tsx?$/.test(file)) continue;
      const source = readFileSync(join(sourceRoot, file), "utf8");
      for (const match of source.matchAll(/useShortcut\("([^"]+)"/g))
        registered.add(match[1] ?? "");
    }
    expect([...registered].sort()).toEqual([
      "chat.clear",
      "chat.copyId",
      "chat.expandDetails",
      "chat.fork",
      "chat.latest",
      "chat.new",
      "chat.next",
      "chat.next.vim",
      "chat.prev",
      "chat.prev.vim",
      "chat.toggleList",
      "search.open",
      "settings.open",
      "shortcuts.open",
      "shortcuts.open.mod",
    ]);
    const definitions: readonly ShortcutDefinition[] = shortcutRegistry;
    expect([...registered].sort()).toEqual(
      definitions
        .filter((shortcut) => !shortcut.fixed)
        .map((shortcut) => shortcut.id)
        .sort(),
    );
  });
});

describe("describeShortcut", () => {
  const byId = new Map<string, ShortcutDefinition>(shortcutRegistry.map((s) => [s.id, s]));
  const send = byId.get("composer.send") as ShortcutDefinition;
  const newline = byId.get("composer.newline") as ShortcutDefinition;

  it("leaves every other row's description untouched for both preferences", () => {
    for (const shortcut of shortcutRegistry) {
      if (shortcut.id === "composer.send" || shortcut.id === "composer.newline") continue;
      expect(describeShortcut(shortcut, "queue")).toBe(shortcut.description);
      expect(describeShortcut(shortcut, "steer")).toBe(shortcut.description);
    }
  });

  it("phrases Enter and Shift+Enter for the queue preference", () => {
    expect(describeShortcut(send, "queue")).toBe(
      "Send message — while the agent is replying: queue the message",
    );
    expect(describeShortcut(newline, "queue")).toBe(
      "Insert a new line — while the agent is replying: steer the agent",
    );
  });

  it("phrases Enter and Shift+Enter for the steer preference", () => {
    expect(describeShortcut(send, "steer")).toBe(
      "Send message — while the agent is replying: steer the agent",
    );
    expect(describeShortcut(newline, "steer")).toBe(
      "Insert a new line — while the agent is replying: queue the message",
    );
  });

  it("agrees with what the composer does while the agent is replying", () => {
    for (const behavior of ["queue", "steer"] as const) {
      const enter = resolveComposerAction({ behavior, shift: false, working: true });
      const shiftEnter = resolveComposerAction({ behavior, shift: true, working: true });
      const phrase = { queue: "queue the message", steer: "steer the agent" } as const;
      expect(describeShortcut(send, behavior).endsWith(phrase[enter as "queue" | "steer"])).toBe(
        true,
      );
      expect(
        describeShortcut(newline, behavior).endsWith(phrase[shiftEnter as "queue" | "steer"]),
      ).toBe(true);
    }
  });
});

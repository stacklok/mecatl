// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  keycaps,
  matchesShortcut,
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

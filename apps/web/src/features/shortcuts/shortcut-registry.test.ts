// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  keycaps,
  matchesShortcut,
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
});

describe("shortcutWorksWhileTyping", () => {
  it("allows primary-modifier commands and escape", () => {
    expect(shortcutWorksWhileTyping("mod+k")).toBe(true);
    expect(shortcutWorksWhileTyping("mod+shift+o")).toBe(true);
    expect(shortcutWorksWhileTyping("esc")).toBe(true);
    expect(shortcutWorksWhileTyping("j")).toBe(false);
  });
});

import { describe, expect, it } from "vitest";
import {
  comboFiresWhileTyping,
  keycaps,
  matchCombo,
  SHORTCUT_GROUPS,
  SHORTCUTS,
} from "./registry";

/** Build a minimal KeyboardEvent-like object for matchCombo. */
function ev(
  key: string,
  mods: Partial<{
    meta: boolean;
    ctrl: boolean;
    shift: boolean;
    alt: boolean;
  }> = {},
): KeyboardEvent {
  return {
    key,
    metaKey: mods.meta ?? false,
    ctrlKey: mods.ctrl ?? false,
    shiftKey: mods.shift ?? false,
    altKey: mods.alt ?? false,
  } as KeyboardEvent;
}

describe("shortcut registry", () => {
  it("has unique ids", () => {
    const ids = SHORTCUTS.map((s) => s.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("every shortcut belongs to a known group", () => {
    for (const s of SHORTCUTS) {
      expect(SHORTCUT_GROUPS).toContain(
        s.group as (typeof SHORTCUT_GROUPS)[number],
      );
    }
  });

  it("pins the app-wide bindings to their combos", () => {
    const byId = new Map(SHORTCUTS.map((s) => [s.id, s.combo]));
    expect(byId.get("search.open")).toBe("mod+k");
    expect(byId.get("settings.open")).toBe("mod+,");
    expect(byId.get("shortcuts.open")).toBe("?");
    expect(byId.get("shortcuts.open.mod")).toBe("mod+/");
    expect(byId.get("chat.toggleList")).toBe("mod+b");
    expect(byId.get("close.esc")).toBe("esc");
    // Deliberately NOT mod+n: browsers reserve ⌘N/Ctrl+N (new window) and the
    // page can't intercept it, so "New chat" stays on the preventable ⌘⇧O.
    expect(byId.get("chat.new")).toBe("mod+shift+o");
  });
});

describe("keycaps", () => {
  it("renders modifiers and keys", () => {
    expect(keycaps("mod+k")).toEqual(["⌘", "K"]);
    expect(keycaps("mod+shift+n")).toEqual(["⌘", "⇧", "N"]);
    expect(keycaps("down")).toEqual(["↓"]);
    expect(keycaps("?")).toEqual(["?"]);
    expect(keycaps("shift+enter")).toEqual(["⇧", "Enter"]);
  });
});

describe("matchCombo", () => {
  it("matches modifier combos (⌘ or Ctrl)", () => {
    expect(matchCombo("mod+k", ev("k", { meta: true }))).toBe(true);
    expect(matchCombo("mod+k", ev("k", { ctrl: true }))).toBe(true);
    expect(matchCombo("mod+k", ev("k"))).toBe(false); // no modifier
  });

  it("matches plain keys and arrow aliases", () => {
    expect(matchCombo("j", ev("j"))).toBe(true);
    expect(matchCombo("j", ev("J", { shift: true }))).toBe(true); // capital J
    expect(matchCombo("down", ev("ArrowDown"))).toBe(true);
    expect(matchCombo("up", ev("ArrowUp"))).toBe(true);
  });

  it("matches symbol keys without needing an explicit shift", () => {
    expect(matchCombo("?", ev("?", { shift: true }))).toBe(true);
    expect(matchCombo("/", ev("/"))).toBe(true);
  });

  it("rejects when a modifier is present but not wanted", () => {
    expect(matchCombo("j", ev("j", { meta: true }))).toBe(false);
    expect(matchCombo("down", ev("ArrowDown", { alt: true }))).toBe(false);
  });

  it("requires shift when the combo declares it", () => {
    expect(
      matchCombo("mod+shift+n", ev("n", { meta: true, shift: true })),
    ).toBe(true);
    expect(matchCombo("mod+shift+n", ev("n", { meta: true }))).toBe(false);
  });

  it("matches mod + punctuation combos", () => {
    expect(matchCombo("mod+,", ev(",", { meta: true }))).toBe(true);
    expect(matchCombo("mod+,", ev(",", { ctrl: true }))).toBe(true);
    expect(matchCombo("mod+,", ev(","))).toBe(false);
    expect(matchCombo("mod+/", ev("/", { meta: true }))).toBe(true);
    expect(matchCombo("mod+/", ev("/"))).toBe(false);
  });

  it("matches esc via its alias", () => {
    expect(matchCombo("esc", ev("Escape"))).toBe(true);
    expect(matchCombo("esc", ev("Escape", { meta: true }))).toBe(false);
  });
});

describe("comboFiresWhileTyping", () => {
  it("allows mod combos and bare esc, suppresses plain keys", () => {
    expect(comboFiresWhileTyping("mod+k")).toBe(true);
    expect(comboFiresWhileTyping("mod+shift+o")).toBe(true);
    expect(comboFiresWhileTyping("esc")).toBe(true);
    expect(comboFiresWhileTyping("j")).toBe(false);
    expect(comboFiresWhileTyping("?")).toBe(false);
    expect(comboFiresWhileTyping("shift+enter")).toBe(false);
  });
});

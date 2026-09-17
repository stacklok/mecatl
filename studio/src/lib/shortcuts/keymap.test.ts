import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  effectiveBindings,
  isRebindable,
  KEYMAP_STORAGE_KEY,
  normalizeCombo,
  RESERVED_COMBOS,
  readOverrides,
  sanitizeOverrides,
  useShortcutBindings,
  writeOverrides,
} from "./keymap";
import { SHORTCUTS } from "./registry";

/**
 * The user keymap layer: name normalisation (the TUI's combo grammar for
 * the browser), the browser-reserved set, the fail-safe storage read, and
 * the shared store every consumer (dispatcher, reference, ⌘K hint) reads
 * from.
 */

describe("normalizeCombo", () => {
  it("aliases modifier and key names onto the registry grammar", () => {
    expect(normalizeCombo("Cmd+Shift+K")).toBe("mod+shift+k");
    expect(normalizeCombo("Ctrl+PgUp")).toBe("mod+pageup");
    expect(normalizeCombo("Escape")).toBe("esc");
    expect(normalizeCombo("option+arrowdown")).toBe("alt+down");
    expect(normalizeCombo("Return")).toBe("enter");
    expect(normalizeCombo("meta k")).toBe("mod+k");
    expect(normalizeCombo("control+,")).toBe("mod+,");
  });

  it("emits a canonical modifier order: mod, shift, alt, key", () => {
    expect(normalizeCombo("shift+alt+mod+k")).toBe("mod+shift+alt+k");
    expect(normalizeCombo("k+shift")).toBe("shift+k");
    expect(normalizeCombo("MOD+SHIFT+O")).toBe("mod+shift+o");
  });

  it("drops an explicit shift on a symbol (the character carries it) but keeps it on letters", () => {
    expect(normalizeCombo("shift+?")).toBe("?");
    expect(normalizeCombo("shift+/")).toBe("/");
    expect(normalizeCombo("shift+k")).toBe("shift+k");
    expect(normalizeCombo("shift+pageup")).toBe("shift+pageup");
  });

  it("rejects modifier-only, two keys, unknown names and nothing at all", () => {
    expect(normalizeCombo("")).toBeNull();
    expect(normalizeCombo("mod")).toBeNull();
    expect(normalizeCombo("shift+mod")).toBeNull();
    expect(normalizeCombo("k+j")).toBeNull();
    expect(normalizeCombo("mod+bogus")).toBeNull();
    expect(normalizeCombo("+")).toBeNull();
  });
});

describe("RESERVED_COMBOS", () => {
  it("covers window/tab chords, clipboard/undo, the browser's own keys and the native focus keys", () => {
    for (const combo of [
      "mod+n",
      "mod+shift+n",
      "mod+t",
      "mod+w",
      "mod+q",
      "mod+tab",
      "mod+c",
      "mod+v",
      "mod+x",
      "mod+z",
      "mod+shift+z",
      "mod+a",
      "mod+f",
      // Browser-owned chords a page may never see (or must not hijack):
      // location, reload, print, bookmarks, history/hide, downloads, open,
      // view-source, zoom, the ⌘1…⌘9 tab switches, clear-browsing-data.
      "mod+l",
      "mod+r",
      "mod+shift+r",
      "mod+p",
      "mod+shift+p",
      "mod+d",
      "mod+shift+d",
      "mod+h",
      "mod+j",
      "mod+o",
      "mod+u",
      "mod+=",
      "mod+-",
      "mod+0",
      "mod+1",
      "mod+9",
      "mod+shift+delete",
      // Function keys the browser answers itself, and the Alt navigation.
      "f1",
      "f5",
      "f11",
      "f12",
      "alt+left",
      "alt+right",
      "alt+f4",
      "tab",
      "shift+tab",
      "enter",
      "space",
    ]) {
      expect(RESERVED_COMBOS.has(combo)).toBe(true);
    }
  });

  it("never reserves a rebindable registry default", () => {
    for (const def of SHORTCUTS) {
      if (!isRebindable(def)) continue;
      expect(RESERVED_COMBOS.has(def.combo)).toBe(false);
    }
  });
});

describe("registry defaults", () => {
  it("are canonical, so stored overrides and defaults compare as plain strings", () => {
    for (const def of SHORTCUTS) {
      expect(normalizeCombo(def.combo)).toBe(def.combo);
    }
  });

  it("the dispatched defaults are collision-free", () => {
    const live = SHORTCUTS.filter((s) => !s.fixed).map((s) => s.combo);
    expect(new Set(live).size).toBe(live.length);
  });
});

describe("sanitizeOverrides / readOverrides", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("drops unknown ids, non-rebindable ids, invalid, reserved and default-equal combos", () => {
    expect(
      sanitizeOverrides({
        bogus: "mod+shift+k",
        "close.esc": "mod+e", // locked
        "composer.send": "mod+e", // fixed
        "chat.details": "mod", // invalid
        "chat.toggleList": "mod+n", // reserved
        "chat.new": "mod+shift+o", // equals the default
        "search.open": 42, // not a string
        "agents.toggle": "Cmd+Shift+K", // valid → canonical
      }),
    ).toEqual({ "agents.toggle": "mod+shift+k" });
  });

  it("keeps the earliest-stored of two colliding overrides", () => {
    expect(
      sanitizeOverrides({
        "chat.new": "mod+shift+k",
        "chat.details": "mod+shift+k",
      }),
    ).toEqual({ "chat.new": "mod+shift+k" });
  });

  it("drops an override that collides with another shortcut's default", () => {
    expect(sanitizeOverrides({ "chat.new": "mod+k" })).toEqual({});
    // …including a locked row's key (Esc is dispatched, so it is live).
    expect(sanitizeOverrides({ "chat.new": "esc" })).toEqual({});
  });

  it("round-trips a swap of two defaults (the second pass restores nothing)", () => {
    // chat.new takes chat.toggleList's default while chat.toggleList moves
    // onto chat.new's: valid as a SET, though neither is valid alone.
    const swap = { "chat.new": "mod+b", "chat.toggleList": "mod+shift+o" };
    expect(sanitizeOverrides(swap)).toEqual(swap);
  });

  it("ignores malformed or non-object storage", () => {
    expect(sanitizeOverrides(null)).toEqual({});
    expect(sanitizeOverrides(["mod+k"])).toEqual({});
    window.localStorage.setItem(KEYMAP_STORAGE_KEY, "{not json");
    expect(readOverrides()).toEqual({});
  });

  it("reads and sanitises what the browser stores", () => {
    window.localStorage.setItem(
      KEYMAP_STORAGE_KEY,
      JSON.stringify({ "chat.new": "Cmd+Shift+K", bogus: "x" }),
    );
    expect(readOverrides()).toEqual({ "chat.new": "mod+shift+k" });
  });

  it("effectiveBindings marks custom only for surviving overrides", () => {
    const bindings = effectiveBindings({
      "chat.new": "mod+shift+k",
      "chat.details": "mod+shift+k", // loses the collision
      "close.esc": "mod+e", // locked — ignored
    });
    const byId = new Map(bindings.map((b) => [b.id, b]));
    expect(byId.get("chat.new")).toMatchObject({
      effectiveCombo: "mod+shift+k",
      custom: true,
    });
    expect(byId.get("chat.details")).toMatchObject({
      effectiveCombo: "mod+i",
      custom: false,
    });
    expect(byId.get("close.esc")).toMatchObject({
      effectiveCombo: "esc",
      custom: false,
    });
    expect(bindings.map((b) => b.id)).toEqual(SHORTCUTS.map((s) => s.id));
  });
});

describe("useShortcutBindings", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  const stored = () => {
    const raw = window.localStorage.getItem(KEYMAP_STORAGE_KEY);
    return raw ? JSON.parse(raw) : null;
  };

  it("starts from the registry defaults with nothing stored", () => {
    const { result } = renderHook(() => useShortcutBindings());
    expect(
      result.current.bindings.find((b) => b.id === "chat.new"),
    ).toMatchObject({ effectiveCombo: "mod+shift+o", custom: false });
    expect(stored()).toBeNull();
  });

  it("a written override persists and keeps two mounted instances in sync", () => {
    const reference = renderHook(() => useShortcutBindings());
    const dispatcher = renderHook(() => useShortcutBindings());

    act(() => writeOverrides({ "chat.new": "Cmd+Shift+K" }));
    expect(stored()).toEqual({ "chat.new": "mod+shift+k" });
    for (const hook of [reference, dispatcher]) {
      expect(
        hook.result.current.bindings.find((b) => b.id === "chat.new"),
      ).toMatchObject({ effectiveCombo: "mod+shift+k", custom: true });
    }

    act(() => writeOverrides({}));
    expect(stored()).toBeNull();
    expect(
      reference.result.current.bindings.find((b) => b.id === "chat.new")
        ?.custom,
    ).toBe(false);
  });

  it("returns a referentially stable snapshot while storage is unchanged", () => {
    const { result, rerender } = renderHook(() => useShortcutBindings());
    const first = result.current.bindings;
    rerender();
    expect(result.current.bindings).toBe(first);
    act(() => writeOverrides({ "chat.new": "mod+shift+k" }));
    expect(result.current.bindings).not.toBe(first);
    const second = result.current.bindings;
    rerender();
    expect(result.current.bindings).toBe(second);
  });

  it("hydrates an existing keymap and drops what no longer validates", () => {
    window.localStorage.setItem(
      KEYMAP_STORAGE_KEY,
      JSON.stringify({ "chat.new": "mod+shift+k", "chat.details": "mod+k" }),
    );
    const { result } = renderHook(() => useShortcutBindings());
    const byId = new Map(result.current.bindings.map((b) => [b.id, b]));
    expect(byId.get("chat.new")?.effectiveCombo).toBe("mod+shift+k");
    // mod+k collides with search.open's default → dropped, default kept.
    expect(byId.get("chat.details")).toMatchObject({
      effectiveCombo: "mod+i",
      custom: false,
    });
    expect(byId.get("search.open")?.effectiveCombo).toBe("mod+k");
  });
});

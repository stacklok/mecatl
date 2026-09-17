import { describe, expect, it } from "vitest";
import { RESERVED_COMBOS } from "@/lib/shortcuts/keymap";
import { keycaps } from "@/lib/shortcuts/registry";
import {
  ALWAYS_NEWLINE_COMBO,
  CLEAR_DRAFT_COMBO,
  isAlwaysNewlineChord,
  isClearDraftChord,
} from "./composer-keys";

/** A minimal KeyboardEvent-like object for the chord recognisers. */
function ev(
  key: string,
  mods: Partial<{
    meta: boolean;
    ctrl: boolean;
    shift: boolean;
    alt: boolean;
  }> = {},
) {
  return {
    key,
    metaKey: mods.meta ?? false,
    ctrlKey: mods.ctrl ?? false,
    shiftKey: mods.shift ?? false,
    altKey: mods.alt ?? false,
  };
}

/**
 * The clear-draft chord (the TUI's ctrl+u): ⌘⇧U / Ctrl+Shift+U and only
 * that. The recogniser rides `matchCombo`, so a plain Backspace, ⌘⌫, a bare
 * U or an alt-laden variant never clear the field.
 */
describe("isClearDraftChord", () => {
  it("is ⌘⇧U / Ctrl+Shift+U, a chord no browser reserves", () => {
    expect(CLEAR_DRAFT_COMBO).toBe("mod+shift+u");
    // NOT ⌘⇧⌫: on macOS the delete key reports as Backspace, so that chord is
    // Chrome's/Firefox's Clear browsing data — which the keymap reserves.
    expect(RESERVED_COMBOS.has("mod+shift+delete")).toBe(true);
    expect(RESERVED_COMBOS.has(CLEAR_DRAFT_COMBO)).toBe(false);
    expect(keycaps(CLEAR_DRAFT_COMBO)).toEqual(["⌘", "⇧", "U"]);
  });

  it("matches on either modifier, with shift, however the key is cased", () => {
    expect(isClearDraftChord(ev("U", { meta: true, shift: true }))).toBe(true);
    expect(isClearDraftChord(ev("u", { meta: true, shift: true }))).toBe(true);
    expect(isClearDraftChord(ev("U", { ctrl: true, shift: true }))).toBe(true);
  });

  it("rejects plain Backspace, ⌘⌫, ⌘⇧⌫, a bare U, ⌘U and an alt variant", () => {
    expect(isClearDraftChord(ev("Backspace"))).toBe(false);
    expect(isClearDraftChord(ev("Backspace", { meta: true }))).toBe(false);
    expect(
      isClearDraftChord(ev("Backspace", { meta: true, shift: true })),
    ).toBe(false);
    expect(isClearDraftChord(ev("u"))).toBe(false);
    expect(isClearDraftChord(ev("U", { shift: true }))).toBe(false);
    expect(isClearDraftChord(ev("u", { meta: true }))).toBe(false);
    expect(
      isClearDraftChord(ev("U", { meta: true, shift: true, alt: true })),
    ).toBe(false);
  });
});

/**
 * The unconditional newline (the TUI's ctrl+j): any Enter with ⌘ or Ctrl
 * held is left to the editor's Mod-Enter hardBreak — never intercepted as a
 * send, queue or steer. Plain Enter and Shift+Enter keep their own rules.
 */
describe("isAlwaysNewlineChord", () => {
  it("is documented as mod+enter", () => {
    expect(ALWAYS_NEWLINE_COMBO).toBe("mod+enter");
    expect(keycaps(ALWAYS_NEWLINE_COMBO)).toEqual(["⌘", "Enter"]);
  });

  it("recognises ⌘Enter and Ctrl+Enter, with or without shift or alt", () => {
    expect(isAlwaysNewlineChord(ev("Enter", { meta: true }))).toBe(true);
    expect(isAlwaysNewlineChord(ev("Enter", { ctrl: true }))).toBe(true);
    expect(isAlwaysNewlineChord(ev("Enter", { meta: true, shift: true }))).toBe(
      true,
    );
    expect(isAlwaysNewlineChord(ev("Enter", { ctrl: true, alt: true }))).toBe(
      true,
    );
  });

  it("leaves plain Enter, Shift+Enter and non-Enter chords to the other rules", () => {
    expect(isAlwaysNewlineChord(ev("Enter"))).toBe(false);
    expect(isAlwaysNewlineChord(ev("Enter", { shift: true }))).toBe(false);
    expect(isAlwaysNewlineChord(ev("Enter", { alt: true }))).toBe(false);
    expect(isAlwaysNewlineChord(ev("u", { meta: true }))).toBe(false);
    expect(isAlwaysNewlineChord(ev("Escape", { meta: true }))).toBe(false);
  });
});

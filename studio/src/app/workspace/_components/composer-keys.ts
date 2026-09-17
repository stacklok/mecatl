import { type ComboKeyEvent, matchCombo } from "@/lib/shortcuts/registry";

/**
 * The composer's two chord recognisers, extracted pure so the editor's
 * capture-phase keydown handler stays a dispatch table and the chords are
 * testable without driving TipTap. Both are OWNED by the composer's own
 * listener (never the global dispatcher): they fire only while the caret is
 * in the field, and their registry rows are documentation-only (`fixed`).
 */

/**
 * The one-shot draft clear (the TUI's ctrl+u `ClearPrompt`): ⌘⇧U on macOS,
 * Ctrl+Shift+U elsewhere. NOT ⌘⇧⌫ — on macOS the delete key reports as
 * `Backspace`, so that chord is Chrome's and Firefox's "Clear browsing data"
 * (⌘⇧Delete, which the keymap already lists as browser-reserved). ⌘⇧U is
 * unbound in Chrome, Firefox and Safari; Edge's Read aloud yields to a page
 * handler that prevents it. The × button beside Send and the double-Esc
 * guard are the same clear, so the chord is a convenience, not the only path.
 */
export const CLEAR_DRAFT_COMBO = "mod+shift+u";

/**
 * The unconditional newline (the TUI's ctrl+j): ⌘Enter / Ctrl+Enter always
 * inserts a line break — idle or streaming, empty field or not — where
 * Shift+Enter is repurposed mid-run as the opposite of the Enter preference.
 */
export const ALWAYS_NEWLINE_COMBO = "mod+enter";

/** True for exactly the clear-draft chord (`matchCombo` grammar: mod + shift
 *  + U, no alt). A plain Backspace, ⌘⌫ or a bare U never match. */
export function isClearDraftChord(event: ComboKeyEvent): boolean {
  return matchCombo(CLEAR_DRAFT_COMBO, event);
}

/**
 * True when Enter is pressed with ⌘ or Ctrl held — with or without shift or
 * alt. The composer's keydown must NOT intercept such a press: it falls
 * through to the editor, whose own `Mod-Enter` hardBreak binding inserts the
 * line break (a shift/alt variant matches no editor binding and is a no-op,
 * never a send). Kept wider than `matchCombo("mod+enter")` on purpose — a
 * modifier-laden Enter must never route into send/queue/steer.
 */
export function isAlwaysNewlineChord(event: ComboKeyEvent): boolean {
  return event.key === "Enter" && (event.metaKey || event.ctrlKey);
}

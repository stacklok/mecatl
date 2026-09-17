import type { SessionPermissionMode } from "@/lib/protocol";
import { matchCombo, SHORTCUTS } from "@/lib/shortcuts/registry";

/**
 * The session permission mode as Studio shows it — one vocabulary for the
 * composer's Mode pill, the chat header badge, the status strip and the
 * Shift+Tab cycle, so a label or a colour can never disagree between them.
 *
 * The daemon's three modes (`SessionPermissionMode`, protocol/sessions.ts)
 * are `default` (ask before every change), `plan` (plan first, no edits) and
 * `acceptEdits` (file edits pre-approved). The daemon is authoritative: the
 * hook adopts its echo, and a refused change rolls back.
 */

/** The Mode menu rows, in menu order: the everyday posture first. */
export const PERMISSION_MODE_OPTIONS = [
  {
    id: "default",
    label: "Manual",
    description: "Always ask before making changes",
  },
  {
    id: "acceptEdits",
    label: "Accept edits",
    description: "Automatically accept all file edits",
  },
  {
    id: "plan",
    label: "Plan",
    description: "Create a plan before making changes",
  },
] as const satisfies readonly {
  id: SessionPermissionMode;
  label: string;
  description: string;
}[];

/** Display label for a session permission mode ("Manual" for the default). */
export function permissionModeLabel(mode: SessionPermissionMode): string {
  return (
    PERMISSION_MODE_OPTIONS.find((option) => option.id === mode)?.label ??
    "Manual"
  );
}

/**
 * The Shift+Tab cycle order — the TUI's `ModeSwitch`: default → plan →
 * accept-edits → default. Deliberately NOT the menu order: escalating from
 * "ask me" through "plan first" to "just edit" reads as a ladder of trust,
 * and it matches what a mecatui user's fingers already know.
 */
export const PERMISSION_MODE_CYCLE: readonly SessionPermissionMode[] = [
  "default",
  "plan",
  "acceptEdits",
];

/** The mode after `mode` in the cycle; an unknown value restarts at default. */
export function nextPermissionMode(
  mode: SessionPermissionMode,
): SessionPermissionMode {
  const at = PERMISSION_MODE_CYCLE.indexOf(mode);
  if (at < 0) return PERMISSION_MODE_CYCLE[0];
  return PERMISSION_MODE_CYCLE[(at + 1) % PERMISSION_MODE_CYCLE.length];
}

/**
 * The colour cue for a non-default mode, on the TUI's own mapping
 * (`modeAccentStyle`: plan → info, accept-edits → success). Default is the
 * everyday posture and gets no tint at all, so a coloured pill or badge
 * always means "something other than ask-me-first is in force".
 */
export function modeAccentClass(mode: SessionPermissionMode): string {
  switch (mode) {
    case "plan":
      return "text-info";
    case "acceptEdits":
      return "text-success";
    default:
      return "";
  }
}

/** The `Badge` variant carrying the same cue; null for the default mode. */
export function modeBadgeVariant(
  mode: SessionPermissionMode,
): "info" | "success" | null {
  switch (mode) {
    case "plan":
      return "info";
    case "acceptEdits":
      return "success";
    default:
      return null;
  }
}

/**
 * The filled dot that sits before a mode label on the composer's Mode pill
 * and the mobile menu's Mode row — the same cue as the box tint, so the pill
 * and the rail always agree even where the label collapses to "Mode". Empty
 * for the default mode: no dot means ask-me-first.
 */
export function modeDotClass(mode: SessionPermissionMode): string {
  switch (mode) {
    case "plan":
      return "bg-info";
    case "acceptEdits":
      return "bg-success";
    default:
      return "";
  }
}

/**
 * The composer box's border + ring for a non-default mode (the TUI recolours
 * the whole input; a hairline tint is the browser-sized version). Empty for
 * the default, so the caller falls back to its ordinary border.
 */
export function modeComposerRingClass(mode: SessionPermissionMode): string {
  switch (mode) {
    case "plan":
      return "border-info/50 ring-1 ring-info/20";
    case "acceptEdits":
      return "border-success/50 ring-1 ring-success/20";
    default:
      return "";
  }
}

/** The registry row the composer's cycle key is documented under. */
export const MODE_CYCLE_SHORTCUT_ID = "composer.mode.cycle";

/**
 * The combo that cycles the mode, read from the registry so the help page
 * and the behaviour can't drift. `shift+tab` unless the registry says
 * otherwise (it is a `fixed`, component-owned row, so the keymap's overrides
 * never apply to it).
 */
export const MODE_CYCLE_COMBO: string =
  SHORTCUTS.find((s) => s.id === MODE_CYCLE_SHORTCUT_ID)?.combo ?? "shift+tab";

/**
 * Whether a keydown seen by the composer's OWN editor listener cycles the
 * permission mode. Extracted pure (like `resolveComposerAction`) so the
 * decision table is testable without driving ProseMirror.
 *
 * The chord is `MODE_CYCLE_COMBO` (⇧Tab) and nothing else: a bare Tab keeps
 * its native forward-focus meaning, ⌘/Ctrl+⇧Tab is the browser's tab switch
 * (never ours), and Alt is a different chord. Two more gates:
 *
 * - `menuOpen`: with the `/` or `@` autocomplete open, Tab and ⇧Tab belong
 *   to the menu (they pick the highlighted row — `handleMenuNavKey`), so the
 *   cycle yields.
 * - `canChangeMode`: no Mode selector on this surface, a disabled composer,
 *   or a run streaming without the deferred-switch contract — then ⇧Tab is
 *   left to the browser (reverse focus), exactly as the disabled pill is
 *   left alone.
 *
 * The listener is the editor's, not the global dispatcher's, so outside the
 * composer ⇧Tab is never claimed — settings forms, dialogs and screen readers
 * keep their reverse-focus navigation (the keymap reserves the chord for the
 * same reason).
 */
export function resolveModeCycleKey(input: {
  key: string;
  shiftKey: boolean;
  metaKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  menuOpen: boolean;
  canChangeMode: boolean;
}): boolean {
  if (input.menuOpen || !input.canChangeMode) return false;
  return matchCombo(MODE_CYCLE_COMBO, input);
}

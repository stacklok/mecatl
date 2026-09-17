import { type ComboKeyEvent, matchCombo } from "@/lib/shortcuts/registry";
import { isDebugMcpMutationAsk } from "./approval-queue";
import type { ApprovalChoice, ApprovalRequest } from "./types";

/**
 * The keyboard half of the permission verdict (the TUI's approval modal
 * keys, cmd/mecatui/ui/keys.go: allow = a/y/enter, always allow = w, deny =
 * d/n/esc; ←/→/tab cycle the focused button). Pure functions over the
 * shortcut registry's EFFECTIVE bindings, so the verdict bar, the global
 * dispatcher handlers and the key hints all read one source — a remapped
 * key (Settings → Keyboard) is dispatched, hinted and honoured inside the
 * card alike.
 */

/** Registry ids that answer an ask, and the verdict each one gives. */
export const APPROVAL_VERDICT_SHORTCUTS: ReadonlyArray<{
  readonly id: string;
  readonly choice: ApprovalChoice;
}> = [
  { id: "approval.allow", choice: "once" },
  { id: "approval.allow.alt", choice: "once" },
  { id: "approval.always", choice: "always" },
  { id: "approval.deny", choice: "deny" },
  { id: "approval.deny.alt", choice: "deny" },
];

/**
 * True when an ask may be answered "Always allow": a main-agent ask that is
 * not a debugger MCP call (ADR 0254). A child's ask offers no persistent
 * grant (it would outlive the throwaway child — the TUI withholds
 * AllowAlways for child asks too), and the daemon never learns Always allow
 * for a debugger MCP call, so the button and its key are withheld together.
 */
export function offersAlwaysAllow(
  approval: Pick<ApprovalRequest, "child" | "toolName" | "reason" | "details">,
  debugSession = false,
): boolean {
  return (
    approval.child !== true && !isDebugMcpMutationAsk(approval, debugSession)
  );
}

/**
 * The one ordered verdict set for an ask (the TUI's
 * `visibleApprovalVerdicts`): render order, key hints and focus traversal
 * all consume it, so withholding Always allow never shifts a key onto the
 * wrong button.
 */
export function visibleApprovalVerdicts(
  offerAlways: boolean,
): readonly ApprovalChoice[] {
  return offerAlways ? ["once", "always", "deny"] : ["once", "deny"];
}

/**
 * Where focus moves on a traversal key inside the verdict bar: ← / → step
 * with wrap (the TUI's left/right/tab cycle), Home / End jump to the ends.
 * `current` is the focused button's index, or -1 when none of the buttons
 * holds focus (→ and Home then land on the first, ← and End on the last).
 * Null for any other key.
 */
export function stepVerdictIndex(
  current: number,
  key: string,
  length: number,
): number | null {
  if (length <= 0) return null;
  switch (key) {
    case "ArrowRight":
      return current < 0 ? 0 : (current + 1) % length;
    case "ArrowLeft":
      return current < 0 ? length - 1 : (current - 1 + length) % length;
    case "Home":
      return 0;
    case "End":
      return length - 1;
    default:
      return null;
  }
}

/** The subset of a keymap binding the verdict helpers read. */
export type VerdictBinding = {
  readonly id: string;
  readonly effectiveCombo: string;
};

/**
 * The verdict a key press gives, resolved against the effective bindings of
 * the `approval.*` shortcuts — or null when the press is none of them. A
 * caller that has already claimed the traversal keys and filtered out
 * modifier chords calls this last.
 */
export function verdictForKeyEvent(
  bindings: readonly VerdictBinding[],
  e: ComboKeyEvent,
): ApprovalChoice | null {
  for (const { id, choice } of APPROVAL_VERDICT_SHORTCUTS) {
    const binding = bindings.find((b) => b.id === id);
    if (binding && matchCombo(binding.effectiveCombo, e)) return choice;
  }
  return null;
}

/**
 * Every effective combo that gives `choice`, primary first (`y` before `a`
 * for Allow once) — the hint shows the first, `aria-keyshortcuts` lists all.
 */
export function verdictCombos(
  bindings: readonly VerdictBinding[],
  choice: ApprovalChoice,
): string[] {
  const combos: string[] = [];
  for (const shortcut of APPROVAL_VERDICT_SHORTCUTS) {
    if (shortcut.choice !== choice) continue;
    const binding = bindings.find((b) => b.id === shortcut.id);
    if (binding) combos.push(binding.effectiveCombo);
  }
  return combos;
}

const ARIA_KEY: Record<string, string> = {
  mod: "Control",
  shift: "Shift",
  alt: "Alt",
  esc: "Escape",
  up: "ArrowUp",
  down: "ArrowDown",
  left: "ArrowLeft",
  right: "ArrowRight",
  enter: "Enter",
  space: "Space",
  tab: "Tab",
  pageup: "PageUp",
  pagedown: "PageDown",
  home: "Home",
  end: "End",
  delete: "Delete",
  backspace: "Backspace",
  insert: "Insert",
};

/**
 * Registry combos as an `aria-keyshortcuts` value: space-separated chords
 * whose tokens are UI Events key names joined by `+` (`mod+k` → `Control+k`;
 * ⌘ is reported as Control since the attribute has no "either" spelling).
 */
export function ariaKeyShortcuts(combos: readonly string[]): string {
  return combos
    .map((combo) =>
      combo
        .split("+")
        .map((token) => ARIA_KEY[token] ?? token)
        .join("+"),
    )
    .join(" ");
}

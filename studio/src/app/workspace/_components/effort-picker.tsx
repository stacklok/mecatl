"use client";

/**
 * The reasoning-effort half of the composer's model + effort picker: the
 * daemon's neutral tiers (auto / low / medium / high / xhigh / max) as a
 * checkmarked list, in the desktop dropdown's Effort submenu and the mobile
 * sheet's Effort section alike.
 *
 * In a DRAFT the pick is pending local state that rides the first send's
 * create body (`reasoning_effort`); in a LIVE chat the daemon fixes the tier
 * at create, so a pick FORKS the chat onto the tier (the same handoff as a
 * model switch) and the list says so. The checkmark follows the value the
 * caller passes: the draft's pending pick, or the live session's
 * `resolved_model.reasoning_effort` — the EFFECTIVE tier, never a guess.
 *
 * A model that reports no reasoning support gets the TUI's warning row: the
 * daemon still accepts the tier, the provider may ignore it.
 */

import { Check } from "lucide-react";
import {
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@/components/ui/dropdown-menu";
import {
  EFFORT_TIERS,
  effortLabel,
  effortTier,
  effortWire,
} from "@/lib/reasoning-effort";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { cn } from "@/lib/utils";

/**
 * Registers the `composer.model` shortcut (the TUI's F7 analogue) for the
 * picker that mounts it. Rendered ONLY by a picker wired to a session, so a
 * display-only composer (the thread panel) never claims the chord away from
 * the main chat's picker — conditional mount is conditional registration.
 */
export function ModelPickerShortcut({ onOpen }: { onOpen: () => void }) {
  useShortcut("composer.model", onOpen);
  return null;
}

/** The live-chat note above the tiers (a pick forks, like a model switch). */
export const EFFORT_SWITCH_NOTE =
  "Picking an effort continues this chat in a copy on it.";

/** Shown when the picked/current model reports `reasoning: false`. */
export const NO_REASONING_WARNING =
  "This model reports no reasoning support — a tier may be ignored.";

export interface EffortPickerProps {
  /** The tier in force, as a wire value or tier name: the draft's pending
   *  pick, or the live session's resolved_model.reasoning_effort ("" = auto). */
  value: string;
  /** Receives the WIRE value: "" for auto, else the tier. */
  onPick: (wire: string) => void;
  /** Live chat: the pick forks the chat; the note above the tiers says so. */
  live?: boolean;
  /** The picked/current model's `reasoning` capability flag. `false` renders
   *  the no-reasoning warning; undefined (auto-routed, or an unlisted model)
   *  renders nothing — Studio never warns on a guess. */
  modelReasoning?: boolean;
}

/** "{model} · {effort}" — the composer trigger's label in both modes. */
export function effortTriggerLabel(modelLabel: string, value: string): string {
  return `${modelLabel} · ${effortLabel(value)}`;
}

const ITEM_CLASS =
  "flex items-center gap-3 rounded-lg px-3 py-3 text-sm cursor-pointer hover:bg-zinc-100 dark:hover:bg-zinc-800 justify-between";

/** The desktop Effort submenu's body: note, warning, and one row per tier. */
export function EffortMenuItems({
  value,
  onPick,
  live,
  modelReasoning,
}: EffortPickerProps) {
  const current = effortTier(value);
  return (
    <>
      {live && (
        <p className="px-3 pb-1.5 text-xs text-muted-foreground">
          {EFFORT_SWITCH_NOTE}
        </p>
      )}
      {modelReasoning === false && (
        <p role="note" className="px-3 pb-1.5 text-xs text-warning">
          {NO_REASONING_WARNING}
        </p>
      )}
      {EFFORT_TIERS.map((tier) => {
        const isSelected = tier === current;
        return (
          <DropdownMenuItem
            key={tier}
            role="menuitemradio"
            aria-checked={isSelected}
            className={cn(
              ITEM_CLASS,
              isSelected && "bg-zinc-100 dark:bg-zinc-800",
            )}
            onClick={() => onPick(effortWire(tier))}
          >
            <span className="font-medium">{effortLabel(tier)}</span>
            <Check
              className={cn(
                "size-4 shrink-0",
                isSelected ? "text-foreground" : "text-transparent",
              )}
            />
          </DropdownMenuItem>
        );
      })}
    </>
  );
}

/** The desktop dropdown's Effort submenu (trigger + the tier list). */
export function EffortSubmenu(props: EffortPickerProps) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <span className="flex-1">Effort</span>
        <span className="text-muted-foreground">
          {effortLabel(props.value)}
        </span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="w-72 p-2">
        <EffortMenuItems {...props} />
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

/**
 * The mobile picker sheet's Effort section: the section heading, the
 * live/warning notes, and one tappable row per tier (the sheet's option-row
 * idiom — label, trailing checkmark). Closing the sheet after a live pick is
 * the caller's `onPick`, the way its model rows do.
 */
export function EffortSheetSection({
  value,
  onPick,
  live,
  modelReasoning,
}: EffortPickerProps) {
  const current = effortTier(value);
  return (
    <fieldset className="min-w-0">
      <legend className="px-4 pt-3 pb-1 text-xs font-medium text-muted-foreground">
        Effort
      </legend>
      {live && (
        <p className="px-4 pb-1 text-xs text-muted-foreground">
          {EFFORT_SWITCH_NOTE}
        </p>
      )}
      {modelReasoning === false && (
        <p role="note" className="px-4 pb-1 text-xs text-warning">
          {NO_REASONING_WARNING}
        </p>
      )}
      {EFFORT_TIERS.map((tier) => {
        const isSelected = tier === current;
        return (
          <button
            key={tier}
            type="button"
            aria-pressed={isSelected}
            onClick={() => onPick(effortWire(tier))}
            className="flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50"
          >
            <span className="flex min-w-0 flex-1 flex-col text-left">
              <span className="truncate font-medium">{effortLabel(tier)}</span>
            </span>
            <Check
              className={cn(
                "size-4 shrink-0",
                isSelected ? "text-foreground" : "text-transparent",
              )}
            />
          </button>
        );
      })}
    </fieldset>
  );
}

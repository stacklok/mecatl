// SPDX-License-Identifier: Apache-2.0

import { Check } from "lucide-react";
import {
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "../../components/ui/dropdown-menu";
import { type EffortChoices, effortLabel } from "../../lib/reasoning-effort";
import { cn } from "../../lib/utils";

/**
 * The Model pill's Effort submenu, from the prototype (`stack-08`): Auto and
 * the levels `choices` offers for the model (see `effortChoicesFor`), with its
 * note underneath.
 */
export function EffortSubmenu({
  choices,
  disabled,
  effort,
  onOpen,
  onSelect,
}: {
  choices: EffortChoices;
  disabled?: boolean;
  effort: string;
  /** Called each time the submenu opens, such as to list models whose levels are unknown again. */
  onOpen?: () => void;
  onSelect: (value: string) => void;
}) {
  return (
    <DropdownMenuSub onOpenChange={(open) => open && onOpen?.()}>
      <DropdownMenuSubTrigger disabled={disabled}>
        <span className="flex-1">Effort</span>
        <span className="text-muted-foreground">{effortLabel(effort)}</span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="w-56">
        {choices.options.map((option) => (
          <DropdownMenuItem key={option.value} onSelect={() => onSelect(option.value)}>
            <Check
              aria-hidden="true"
              className={cn("size-3.5", effort !== option.value && "invisible")}
            />
            {option.label}
          </DropdownMenuItem>
        ))}
        {choices.note && (
          <p className="border-t px-2.5 py-2 text-xs text-muted-foreground" role="note">
            {choices.note}
          </p>
        )}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

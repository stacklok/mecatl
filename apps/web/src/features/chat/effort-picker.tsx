// SPDX-License-Identifier: Apache-2.0

import { Check } from "lucide-react";
import {
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "../../components/ui/dropdown-menu";
import { EFFORT_OPTIONS, effortLabel, NO_REASONING_SUPPORT_NOTE } from "../../lib/reasoning-effort";
import { cn } from "../../lib/utils";

/**
 * The Model pill's Effort submenu, from the prototype (`stack-08`): Auto
 * through Max, the tiers `reasoningEffortSchema` accepts on both
 * `createSession` and `forkSession`.
 *
 * `modelSupportsReasoning` gates only the caveat line. The daemon still
 * accepts a tier for a model that reports no reasoning support, so the choice
 * stays available.
 */
export function EffortSubmenu({
  disabled,
  effort,
  modelSupportsReasoning,
  onSelect,
}: {
  disabled?: boolean;
  effort: string;
  /** Undefined until a model is resolved; the caveat shows only when this is `false`. */
  modelSupportsReasoning?: boolean;
  onSelect: (value: string) => void;
}) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger disabled={disabled}>
        <span className="flex-1">Effort</span>
        <span className="text-muted-foreground">{effortLabel(effort)}</span>
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="w-56">
        {EFFORT_OPTIONS.map((option) => (
          <DropdownMenuItem key={option.value} onSelect={() => onSelect(option.value)}>
            <Check
              aria-hidden="true"
              className={cn("size-3.5", effort !== option.value && "invisible")}
            />
            {option.label}
          </DropdownMenuItem>
        ))}
        {modelSupportsReasoning === false && (
          <p className="border-t px-2.5 py-2 text-xs text-muted-foreground">
            {NO_REASONING_SUPPORT_NOTE}
          </p>
        )}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  );
}

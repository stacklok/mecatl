// SPDX-License-Identifier: Apache-2.0

import { Check, ChevronDown } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { cn } from "../../lib/utils";

/**
 * The composer's picker pattern, matching Mecatl Studio's `OptionField`: a
 * pill trigger showing the current choice, opening a dropdown of options
 * each carrying a title and a one-line description — not a plain native
 * `<select>`, which can't show per-option description text.
 */
export interface ComposerOption<V extends string> {
  description: string;
  title: string;
  value: V;
}

export function ComposerOptionMenu<V extends string>({
  disabled,
  items,
  label,
  onSelect,
  value,
  valueLabel,
}: {
  disabled?: boolean;
  items: ComposerOption<V>[];
  label: string;
  onSelect: (value: V) => void;
  value: V;
  valueLabel: string;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button
          className="flex h-8 items-center gap-1.5 rounded-full border px-3 text-xs disabled:pointer-events-none disabled:opacity-50"
          type="button"
        >
          <span className="font-medium">{label}</span>
          <span className="text-muted-foreground">{valueLabel}</span>
          <ChevronDown aria-hidden="true" className="size-3 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        {items.map((item) => (
          <DropdownMenuItem
            className="flex-col items-start gap-0.5 py-2"
            key={item.value}
            onSelect={() => onSelect(item.value)}
          >
            <span className="flex w-full items-center gap-1.5 font-medium">
              <Check
                aria-hidden="true"
                className={cn("size-3.5 shrink-0", item.value !== value && "invisible")}
              />
              {item.title}
            </span>
            <span className="pl-5 text-xs text-muted-foreground">{item.description}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

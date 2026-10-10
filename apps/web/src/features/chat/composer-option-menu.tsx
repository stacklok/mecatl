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
 * The composer pill row's shared ghost trigger, from the prototype
 * (`stack-08`): every pill (Mode, Tools, Model) uses this one class list.
 */
export const GHOST_TRIGGER_CLASS =
  "flex h-7 shrink-0 items-center gap-1 whitespace-nowrap rounded-full border-0 bg-transparent px-2.5 text-sm font-normal text-foreground shadow-none hover:bg-zinc-200 disabled:pointer-events-none disabled:opacity-50 dark:hover:bg-zinc-700";

/**
 * The composer's picker pattern: a pill trigger showing the current choice,
 * opening a dropdown of options, each with a title and a one-line
 * description, which a native `<select>` can't show.
 */
export interface ComposerOption<V extends string> {
  description: string;
  /** A posture dot before the title, as in the Mode menu. Omit for tables with nothing to signal. */
  dotClassName?: string;
  title: string;
  value: V;
}

export function ComposerOptionMenu<V extends string>({
  disabled,
  items,
  label,
  menuClassName = "w-64",
  onSelect,
  value,
  valueLabel,
}: {
  disabled?: boolean;
  items: ComposerOption<V>[];
  label: string;
  /** The open menu's width, `w-72` for the Mode menu. */
  menuClassName?: string;
  onSelect: (value: V) => void;
  value: V;
  valueLabel: string;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button className={GHOST_TRIGGER_CLASS} type="button">
          <span>{label}</span>
          <span className="text-muted-foreground @max-md:hidden">{valueLabel}</span>
          <ChevronDown aria-hidden="true" className="size-3.5 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className={menuClassName}>
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
              {item.dotClassName && (
                <span
                  aria-hidden="true"
                  className={cn("size-1.5 shrink-0 rounded-full", item.dotClassName)}
                />
              )}
              {item.title}
            </span>
            <span className="pl-5 text-xs text-muted-foreground">{item.description}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

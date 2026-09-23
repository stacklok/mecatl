// SPDX-License-Identifier: Apache-2.0

import { Check, ChevronDown } from "lucide-react";
import { useState } from "react";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "../../components/ui/sheet";
import { useIsMobile } from "../../lib/use-mobile";
import { cn } from "../../lib/utils";

export interface OptionItem {
  description?: string;
  icon?: React.ComponentType<{ className?: string }>;
  label: string;
  value: string;
}

/**
 * A select-style settings control: a trigger button showing the current
 * choice, opening a dropdown on desktop and a bottom sheet on mobile —
 * matching Studio's `OptionField`, since a native `<select>` can't carry
 * per-option description text or icons.
 */
export function OptionField({
  label,
  onChange,
  options,
  value,
}: {
  label: string;
  onChange: (value: string) => void;
  options: readonly OptionItem[];
  value: string;
}) {
  const isMobile = useIsMobile();
  const [sheetOpen, setSheetOpen] = useState(false);
  const current = options.find((option) => option.value === value);
  const CurrentIcon = current?.icon;

  const trigger = (
    <Button
      aria-label={label}
      className="h-9 w-44 justify-between gap-2 rounded-lg px-3 font-normal"
      onClick={isMobile ? () => setSheetOpen(true) : undefined}
      variant="outline"
    >
      <span className="flex min-w-0 items-center gap-2">
        {CurrentIcon && <CurrentIcon className="size-4 text-muted-foreground" />}
        <span className="truncate">{current?.label ?? value}</span>
      </span>
      <ChevronDown aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
    </Button>
  );

  if (isMobile) {
    return (
      <>
        {trigger}
        <Sheet onOpenChange={setSheetOpen} open={sheetOpen}>
          <SheetContent className="p-0" side="bottom">
            <SheetTitle className="sr-only">{label}</SheetTitle>
            <div className="py-2">
              {options.map((option) => {
                const Icon = option.icon;
                return (
                  <button
                    className="flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50"
                    key={option.value}
                    onClick={() => {
                      onChange(option.value);
                      setSheetOpen(false);
                    }}
                    type="button"
                  >
                    {Icon && <Icon aria-hidden="true" className="size-4 text-muted-foreground" />}
                    <span className="flex min-w-0 flex-1 flex-col text-left">
                      <span className="truncate font-medium">{option.label}</span>
                      {option.description && (
                        <span className="truncate text-xs text-muted-foreground">
                          {option.description}
                        </span>
                      )}
                    </span>
                    <Check
                      aria-hidden="true"
                      className={cn(
                        "size-4 shrink-0",
                        option.value === value ? "text-foreground" : "text-transparent",
                      )}
                    />
                  </button>
                );
              })}
            </div>
          </SheetContent>
        </Sheet>
      </>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>{trigger}</DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-36">
        {options.map((option) => {
          const Icon = option.icon;
          return (
            <DropdownMenuItem
              className="gap-2"
              key={option.value}
              onSelect={() => onChange(option.value)}
            >
              <Check
                aria-hidden="true"
                className={cn(
                  "size-4",
                  option.value === value ? "text-foreground" : "text-transparent",
                )}
              />
              {Icon && <Icon aria-hidden="true" className="size-4 text-muted-foreground" />}
              {option.description ? (
                <span className="flex min-w-0 flex-col">
                  <span>{option.label}</span>
                  <span className="text-xs text-muted-foreground">{option.description}</span>
                </span>
              ) : (
                option.label
              )}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

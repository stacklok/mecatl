"use client";

import { Check, ChevronDown } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { useIsMobile } from "@/hooks/use-mobile";
import { cn } from "@/lib/utils";

export interface OptionItem {
  value: string;
  label: string;
  icon?: React.ComponentType<{ className?: string }>;
}

/**
 * A select-style settings control: a trigger button showing the current
 * choice that opens a dropdown on desktop and a bottom sheet on mobile
 * (the app's picker convention — pill groups don't scale on small screens).
 */
export function OptionField({
  label,
  value,
  options,
  onChange,
}: {
  /** Accessible name for the control (the visible label lives beside it). */
  label: string;
  value: string;
  options: readonly OptionItem[];
  onChange: (value: string) => void;
}) {
  const isMobile = useIsMobile();
  const [sheetOpen, setSheetOpen] = useState(false);
  const current = options.find((option) => option.value === value);
  const CurrentIcon = current?.icon;

  const trigger = (
    <Button
      variant="outline"
      className="h-9 w-44 justify-between gap-2 rounded-lg px-3 font-normal"
      aria-label={label}
      onClick={isMobile ? () => setSheetOpen(true) : undefined}
    >
      <span className="flex min-w-0 items-center gap-2">
        {CurrentIcon && (
          <CurrentIcon className="size-4 text-muted-foreground" />
        )}
        <span className="truncate">{current?.label ?? value}</span>
      </span>
      <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
    </Button>
  );

  if (isMobile) {
    return (
      <>
        {trigger}
        <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
          <SheetContent side="bottom" className="p-0">
            <SheetTitle className="sr-only">{label}</SheetTitle>
            <div className="py-2">
              {options.map((option) => {
                const Icon = option.icon;
                return (
                  <button
                    key={option.value}
                    type="button"
                    onClick={() => {
                      onChange(option.value);
                      setSheetOpen(false);
                    }}
                    className="flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50"
                  >
                    {Icon && <Icon className="size-4 text-muted-foreground" />}
                    <span className="min-w-0 flex-1 truncate text-left font-medium">
                      {option.label}
                    </span>
                    <Check
                      className={cn(
                        "size-4 shrink-0",
                        option.value === value
                          ? "text-foreground"
                          : "text-transparent",
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
              key={option.value}
              className="gap-2"
              onClick={() => onChange(option.value)}
            >
              <Check
                className={cn(
                  "size-4",
                  option.value === value
                    ? "text-foreground"
                    : "text-transparent",
                )}
              />
              {Icon && <Icon className="size-4 text-muted-foreground" />}
              {option.label}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

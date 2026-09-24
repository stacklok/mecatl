// SPDX-License-Identifier: Apache-2.0

import { ChevronDown } from "lucide-react";
import { useEffect, useState } from "react";
import { useIsMobile } from "../../lib/use-mobile";
import { cn } from "../../lib/utils";
import { Button } from "./button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "./dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetTitle, SheetTrigger } from "./sheet";

export interface OptionItem {
  description?: string;
  icon?: React.ComponentType<{ className?: string }>;
  label: string;
  value: string;
}

/** One choice control with a menu on desktop and a modal sheet on mobile. */
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
  const surface = isMobile ? "mobile" : "desktop";
  const [openSurface, setOpenSurface] = useState<"mobile" | "desktop" | null>(null);
  const open = openSurface === surface;
  const current = options.find((option) => option.value === value);
  const CurrentIcon = current?.icon;

  // A resize must not transfer an open menu into a sheet, or vice versa.
  useEffect(() => {
    if (openSurface !== null && openSurface !== surface) setOpenSurface(null);
  }, [openSurface, surface]);

  const trigger = (
    <Button
      aria-label={`${label}: ${current?.label ?? value}`}
      className="min-h-11 w-44 justify-between gap-2 rounded-lg px-3 font-normal"
      type="button"
      variant="outline"
    >
      <span className="flex min-w-0 items-center gap-2">
        {CurrentIcon && (
          <span aria-hidden="true">
            <CurrentIcon className="size-4 text-input-icon" />
          </span>
        )}
        <span className="truncate">{current?.label ?? value}</span>
      </span>
      <ChevronDown aria-hidden="true" className="size-4 shrink-0 text-input-icon" />
    </Button>
  );

  if (isMobile) {
    return (
      <Sheet onOpenChange={(next) => setOpenSurface(next ? "mobile" : null)} open={open}>
        <SheetTrigger asChild>{trigger}</SheetTrigger>
        <SheetContent
          className="flex max-h-[85dvh] flex-col border-control-border p-0"
          side="bottom"
        >
          <SheetTitle className="sr-only">{label}</SheetTitle>
          <SheetDescription className="sr-only">Choose {label.toLowerCase()}.</SheetDescription>
          <fieldset className="min-h-0 overflow-y-auto pb-2">
            <legend className="sr-only">{label}</legend>
            {options.map((option) => {
              const Icon = option.icon;
              return (
                <label
                  className="flex cursor-pointer items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 focus-within:ring-2 focus-within:ring-inset focus-within:ring-ring"
                  key={option.value}
                >
                  <input
                    checked={option.value === value}
                    className="size-4 shrink-0 accent-btn-primary focus-visible:ring-2 focus-visible:ring-ring"
                    name={label}
                    onChange={() => {
                      onChange(option.value);
                      setOpenSurface(null);
                    }}
                    type="radio"
                    value={option.value}
                  />
                  {Icon && (
                    <span aria-hidden="true">
                      <Icon className="size-4 shrink-0 text-input-icon" />
                    </span>
                  )}
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="font-medium">{option.label}</span>
                    {option.description && (
                      <span className="text-xs text-muted-foreground">{option.description}</span>
                    )}
                  </span>
                </label>
              );
            })}
          </fieldset>
        </SheetContent>
      </Sheet>
    );
  }

  return (
    <DropdownMenu onOpenChange={(next) => setOpenSurface(next ? "desktop" : null)} open={open}>
      <DropdownMenuTrigger asChild>{trigger}</DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="w-[min(18rem,calc(100vw-2rem))] border-control-border"
      >
        <DropdownMenuRadioGroup
          onValueChange={(next) => {
            onChange(next);
            setOpenSurface(null);
          }}
          value={value}
        >
          {options.map((option) => {
            const Icon = option.icon;
            return (
              <DropdownMenuRadioItem className="gap-2 py-2" key={option.value} value={option.value}>
                {Icon && (
                  <span aria-hidden="true">
                    <Icon className="size-4 shrink-0 text-input-icon" />
                  </span>
                )}
                <span className={cn("flex min-w-0 flex-col", !option.description && "py-1")}>
                  <span>{option.label}</span>
                  {option.description && (
                    <span className="text-xs text-muted-foreground">{option.description}</span>
                  )}
                </span>
              </DropdownMenuRadioItem>
            );
          })}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

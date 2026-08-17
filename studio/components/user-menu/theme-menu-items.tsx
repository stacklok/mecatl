"use client";

import { Check, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";

import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

const THEMES = [
  { value: "light", label: "Light mode", icon: Sun },
  { value: "dark", label: "Dark mode", icon: Moon },
] as const;

// The console's menu-row recipe, kept byte-identical so the two products' menus
// feel like one: square rows, generous padding, and an INSET left bar (not a
// border) marking the active choice so the row's box model never shifts.
export function ThemeMenuItems() {
  const { theme: activeTheme, setTheme } = useTheme();

  return (
    <div className="py-1">
      {THEMES.map(({ value, label, icon: Icon }) => {
        const isActive = activeTheme === value;
        return (
          <DropdownMenuItem
            key={value}
            onSelect={() => setTheme(value)}
            className={cn(
              "flex cursor-pointer items-center gap-4 rounded-none px-5 py-3.5 font-medium text-foreground",
              isActive && "shadow-[inset_4px_0_0_var(--color-nav-background)]",
            )}
          >
            <Icon className="size-5 shrink-0 text-foreground/60" />
            <span className="text-sm">{label}</span>
            {isActive && <Check className="ml-auto size-4 text-foreground/70" />}
          </DropdownMenuItem>
        );
      })}
    </div>
  );
}

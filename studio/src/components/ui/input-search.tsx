"use client";

import { Search, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

interface InputSearchProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
}

export function InputSearch({
  value,
  onChange,
  placeholder = "Search",
  className,
}: InputSearchProps) {
  // `relative` must always apply so the absolute Search icon anchors to this
  // wrapper — a passed `className` sets width but must not drop positioning.
  return (
    <div className={cn("relative", className ?? "w-48")}>
      <Search className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-input-icon" />
      <Input
        type="text"
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-9 px-9 bg-white dark:bg-card"
      />
      {value && (
        <Button
          variant="ghost"
          size="icon"
          onClick={() => onChange("")}
          className="absolute top-1/2 right-1 size-7 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          aria-label="Clear search"
        >
          <X className="size-4" />
        </Button>
      )}
    </div>
  );
}

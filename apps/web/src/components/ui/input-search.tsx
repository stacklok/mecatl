// SPDX-License-Identifier: Apache-2.0

import { Search, X } from "lucide-react";
import { cn } from "../../lib/utils";
import { Button } from "./button";
import { Input } from "./input";

interface InputSearchProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  /** Accessible name when the placeholder alone is not the label. */
  "aria-label"?: string;
  /**
   * Handle to the underlying `<input>` so a page can focus it from a
   * keyboard shortcut. `Input` spreads props onto a plain `<input>`, so a
   * React 19 `ref` prop passes straight through without forwardRef.
   */
  inputRef?: React.Ref<HTMLInputElement>;
  /** Key handling on the input itself (e.g. a two-stage Escape). */
  onKeyDown?: React.KeyboardEventHandler<HTMLInputElement>;
}

/** A search field with a leading icon and a clear button, ported from
 *  Studio's `studio/components/ui/input-search.tsx`. */
export function InputSearch({
  value,
  onChange,
  placeholder = "Search",
  className,
  "aria-label": ariaLabel,
  inputRef,
  onKeyDown,
}: InputSearchProps) {
  // `relative` must always apply so the absolute Search icon anchors to this
  // wrapper — a passed `className` sets width but must not drop positioning.
  return (
    <div className={cn("relative", className ?? "w-48")}>
      <Search className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-input-icon" />
      <Input
        aria-label={ariaLabel}
        className="h-9 bg-white px-9 dark:bg-card"
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        ref={inputRef}
        type="text"
        value={value}
      />
      {value && (
        <Button
          aria-label="Clear search"
          className="absolute top-1/2 right-1 size-7 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          onClick={() => onChange("")}
          size="icon"
          variant="ghost"
        >
          <X className="size-4" />
        </Button>
      )}
    </div>
  );
}

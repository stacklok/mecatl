// SPDX-License-Identifier: Apache-2.0

import type * as React from "react";
import { cn } from "@/lib/utils";

// The prototype's input with Studio's contrast-hardened edges: the border is
// `control-border` and focus draws a solid 2px `ring` (both meet 3:1 non-text
// contrast), and an invalid field focuses with a solid destructive ring rather
// than the prototype's translucent one.
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      data-slot="input"
      className={cn(
        "h-9 w-full min-w-0 rounded-md border border-control-border bg-transparent px-3 py-1 text-base outline-none transition-[color,box-shadow] file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive dark:bg-input/30 md:text-sm",
        className,
      )}
      type={type}
      {...props}
    />
  );
}

export { Input };

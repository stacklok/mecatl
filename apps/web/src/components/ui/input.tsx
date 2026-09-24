// SPDX-License-Identifier: Apache-2.0

import type * as React from "react";
import { cn } from "../../lib/utils";

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      className={cn(
        "h-9 w-full min-w-0 rounded-md border border-control-border bg-transparent px-3 py-1 text-base outline-none transition-[color,box-shadow] placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30 md:text-sm",
        className,
      )}
      type={type}
      {...props}
    />
  );
}

export { Input };

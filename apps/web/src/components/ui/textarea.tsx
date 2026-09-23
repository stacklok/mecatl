// SPDX-License-Identifier: Apache-2.0

import type * as React from "react";
import { cn } from "../../lib/utils";

function Textarea({ className, ref, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(
        "field-sizing-content flex min-h-16 w-full rounded-md border border-control-border bg-transparent px-3 py-2 text-base outline-none transition-[color,box-shadow] placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30 md:text-sm",
        className,
      )}
      ref={ref}
      {...props}
    />
  );
}

export { Textarea };

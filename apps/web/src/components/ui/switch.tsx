// SPDX-License-Identifier: Apache-2.0

import * as SwitchPrimitive from "@radix-ui/react-switch";
import type * as React from "react";

import { cn } from "../../lib/utils";

function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        "peer data-[state=unchecked]:bg-input dark:data-[state=unchecked]:bg-input/80 data-[state=checked]:bg-success inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-control-border shadow-xs transition-all outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          "bg-background dark:data-[state=unchecked]:bg-foreground dark:data-[state=checked]:bg-foreground pointer-events-none block size-3.5 rounded-full ring-0 transition-transform data-[state=checked]:translate-x-[calc(100%+3px)] data-[state=unchecked]:translate-x-[3px]",
        )}
      />
    </SwitchPrimitive.Root>
  );
}

export { Switch };

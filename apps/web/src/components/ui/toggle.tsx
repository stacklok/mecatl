// SPDX-License-Identifier: Apache-2.0

import * as TogglePrimitive from "@radix-ui/react-toggle";
import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";
import { cn } from "../../lib/utils";

const pressedFill = "data-[state=on]:bg-accent data-[state=on]:text-accent-foreground";

const toggleVariants = cva(
  "inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium whitespace-nowrap transition-colors hover:bg-muted hover:text-muted-foreground disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    defaultVariants: {
      size: "default",
      variant: "default",
    },
    variants: {
      size: {
        default: "h-9 min-w-9 px-2",
        lg: "h-10 min-w-10 px-2.5",
        sm: "h-8 min-w-8 px-1.5",
      },
      variant: {
        default: `bg-transparent ${pressedFill}`,
        // The ghost Button's look, with no fill while pressed: for a toggle
        // whose label already names the view it switches to.
        ghost:
          "rounded-full hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        outline: `border border-border bg-transparent hover:bg-accent hover:text-accent-foreground ${pressedFill}`,
      },
    },
  },
);

const Toggle = React.forwardRef<
  React.ElementRef<typeof TogglePrimitive.Root>,
  React.ComponentPropsWithoutRef<typeof TogglePrimitive.Root> & VariantProps<typeof toggleVariants>
>(({ className, size, variant, ...props }, ref) => (
  // As on Button, data-toggle comes after the spread so it survives an asChild
  // trigger's data-slot: the phone glyph rule in styles.css keys on it.
  <TogglePrimitive.Root
    data-slot="toggle"
    className={cn(toggleVariants({ className, size, variant }))}
    ref={ref}
    {...props}
    data-toggle=""
  />
));
Toggle.displayName = TogglePrimitive.Root.displayName;

export { Toggle, toggleVariants };

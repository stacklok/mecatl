import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type * as React from "react";

import { cn } from "@/lib/utils";

// Base metrics from the design system's `.badge`: pill, 0.15rem × 0.55rem
// padding, 0.72rem / 500 type, 0.3rem gap.
const badgeVariants = cva(
  "inline-flex items-center gap-[0.3rem] rounded-full border px-[0.55rem] py-[0.15rem] text-[0.72rem] font-medium whitespace-nowrap w-fit shrink-0 [&>svg]:size-3 [&>svg]:pointer-events-none transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground",
        secondary: "border-transparent bg-secondary text-secondary-foreground",
        // Dedicated tokens: on dark the design system lightens the border and
        // dims the ink rather than reusing border/foreground.
        outline: "border-badge-outline-border text-badge-outline-text",
        muted: "border-transparent bg-muted text-muted-foreground",
        // Status tints: alpha-tinted over the semantic token, so they adapt
        // to light and dark through the token's own `.dark` value.
        success: "border-transparent bg-success/15 text-success",
        warning: "border-transparent bg-warning/15 text-warning",
        info: "border-transparent bg-info/15 text-info",
        destructive: "border-transparent bg-destructive/15 text-destructive",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

function Badge({
  className,
  variant,
  asChild = false,
  ...props
}: React.ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "span";

  return (
    <Comp
      data-slot="badge"
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  );
}

export { Badge, badgeVariants };

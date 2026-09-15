// SPDX-License-Identifier: Apache-2.0

"use client";

import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import * as React from "react";

import { useIsTruncated } from "@/hooks/use-is-truncated";
import { cn } from "@/lib/utils";

function TooltipProvider({
  delayDuration = 0,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return (
    <TooltipPrimitive.Provider
      data-slot="tooltip-provider"
      delayDuration={delayDuration}
      {...props}
    />
  );
}

type TooltipProps = React.ComponentProps<typeof TooltipPrimitive.Root> & {
  /**
   * When true, the tooltip only opens if the trigger element's content is
   * visually truncated (e.g. ellipsised by `truncate` / line-clamp). Useful
   * for table cells or card titles where the tooltip should disclose hidden
   * text but not duplicate visible text.
   */
  onlyWhenTruncated?: boolean;
};

const TruncationContext = React.createContext<{
  onlyWhenTruncated: boolean;
  setTriggerElement: React.Dispatch<React.SetStateAction<HTMLElement | null>>;
} | null>(null);

function Tooltip({
  onlyWhenTruncated = false,
  onOpenChange,
  open,
  ...props
}: TooltipProps) {
  const [triggerElement, setTriggerElement] =
    React.useState<HTMLElement | null>(null);
  const isTruncated = useIsTruncated(triggerElement);
  const [internalOpen, setInternalOpen] = React.useState(false);

  const contextValue = React.useMemo(
    () => ({ onlyWhenTruncated, setTriggerElement }),
    [onlyWhenTruncated],
  );

  const handleOpenChange = React.useCallback(
    (nextOpen: boolean) => {
      if (onlyWhenTruncated) {
        const resolvedOpen = nextOpen && isTruncated;
        setInternalOpen(resolvedOpen);
        onOpenChange?.(resolvedOpen);
        return;
      }
      onOpenChange?.(nextOpen);
    },
    [onlyWhenTruncated, isTruncated, onOpenChange],
  );

  React.useEffect(() => {
    if (onlyWhenTruncated && !isTruncated) {
      setInternalOpen(false);
    }
  }, [onlyWhenTruncated, isTruncated]);

  const resolvedOpen = onlyWhenTruncated ? internalOpen : open;
  const resolvedOnOpenChange = onlyWhenTruncated
    ? handleOpenChange
    : onOpenChange;

  return (
    <TooltipProvider>
      <TruncationContext.Provider value={contextValue}>
        <TooltipPrimitive.Root
          data-slot="tooltip"
          {...props}
          open={resolvedOpen}
          onOpenChange={resolvedOnOpenChange}
        />
      </TruncationContext.Provider>
    </TooltipProvider>
  );
}

function TooltipTrigger({
  ref: forwardedRef,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Trigger>) {
  const ctx = React.useContext(TruncationContext);

  const setRefs = React.useCallback(
    (node: HTMLButtonElement | null) => {
      if (ctx) {
        ctx.setTriggerElement(node);
      }
      if (typeof forwardedRef === "function") {
        forwardedRef(node);
      } else if (forwardedRef) {
        (forwardedRef as React.RefObject<HTMLButtonElement | null>).current =
          node;
      }
    },
    [ctx, forwardedRef],
  );

  return (
    <TooltipPrimitive.Trigger
      ref={ctx?.onlyWhenTruncated ? setRefs : forwardedRef}
      data-slot="tooltip-trigger"
      {...props}
    />
  );
}

function TooltipContent({
  className,
  sideOffset = 0,
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          "bg-foreground text-background animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 z-50 w-fit origin-(--radix-tooltip-content-transform-origin) rounded-md px-3 py-1.5 text-xs text-balance",
          className,
        )}
        {...props}
      >
        {children}
        <TooltipPrimitive.Arrow className="bg-foreground fill-foreground z-50 size-2.5 translate-y-[calc(-50%-2px)] rotate-45 rounded-[2px]" />
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  );
}

export { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger };

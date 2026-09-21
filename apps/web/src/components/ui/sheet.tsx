// SPDX-License-Identifier: Apache-2.0

import * as SheetPrimitive from "@radix-ui/react-dialog";
import { cva, type VariantProps } from "class-variance-authority";
import { X } from "lucide-react";
import * as React from "react";

import { cn } from "../../lib/utils";

function Sheet({ ...props }: React.ComponentProps<typeof SheetPrimitive.Root>) {
  return <SheetPrimitive.Root data-slot="sheet" {...props} />;
}

function SheetTrigger({ ...props }: React.ComponentProps<typeof SheetPrimitive.Trigger>) {
  return <SheetPrimitive.Trigger data-slot="sheet-trigger" {...props} />;
}

function SheetClose({ ...props }: React.ComponentProps<typeof SheetPrimitive.Close>) {
  return <SheetPrimitive.Close data-slot="sheet-close" {...props} />;
}

function SheetPortal({ ...props }: React.ComponentProps<typeof SheetPrimitive.Portal>) {
  return <SheetPrimitive.Portal data-slot="sheet-portal" {...props} />;
}

function SheetOverlay({
  className,
  ...props
}: React.ComponentProps<typeof SheetPrimitive.Overlay>) {
  return (
    <SheetPrimitive.Overlay
      data-slot="sheet-overlay"
      className={cn(
        "fixed inset-0 z-50 bg-black/80 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        className,
      )}
      {...props}
    />
  );
}

const sheetVariants = cva(
  "fixed z-50 gap-4 bg-background p-6 shadow-lg transition ease-in-out data-[state=closed]:duration-300 data-[state=open]:duration-500 data-[state=open]:animate-in data-[state=closed]:animate-out",
  {
    variants: {
      side: {
        top: "inset-x-0 top-0 border-b data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top",
        bottom:
          // gap-0 undoes the base gap-4: the grab handle's own padding is the
          // only space wanted between it and the sheet body.
          "inset-x-0 bottom-0 gap-0 rounded-none border-t data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom",
        left: "inset-y-0 left-0 h-full border-r data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left",
        right:
          "inset-y-0 right-0 h-full border-l data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right",
      },
    },
    defaultVariants: {
      side: "right",
    },
  },
);

interface SheetContentProps
  extends React.ComponentProps<typeof SheetPrimitive.Content>,
    VariantProps<typeof sheetVariants> {}

/** Distance a pointer must travel vertically before a drag engages. */
const DRAG_SLOP_PX = 10;
/** Fraction of the sheet height beyond which release closes the sheet. */
const CLOSE_FRACTION = 0.25;
/** Downward velocity (px/ms) that counts as a dismissing flick. */
const FLICK_VELOCITY = 0.5;
/** Duration of the settle/close transition on release. */
const RELEASE_MS = 200;

/**
 * True when some scrollable element between `target` and `container`
 * (inclusive of `target`, exclusive of `container`) is scrolled away from its
 * top — in that case a downward swipe should scroll the content, not drag the
 * sheet.
 */
function hasScrolledAncestor(target: HTMLElement | null, container: HTMLElement): boolean {
  let el = target;
  while (el && el !== container) {
    if (el.scrollTop > 0) return true;
    el = el.parentElement;
  }
  return false;
}

interface DragState {
  pointerId: number | null;
  startX: number;
  startY: number;
  dragging: boolean;
  translate: number;
  lastY: number;
  lastT: number;
  velocity: number;
  justDragged: boolean;
  closing: boolean;
}

const initialDragState: DragState = {
  pointerId: null,
  startX: 0,
  startY: 0,
  dragging: false,
  translate: 0,
  lastY: 0,
  lastT: 0,
  velocity: 0,
  justDragged: false,
  closing: false,
};

function SheetContent({
  side = "right",
  className,
  children,
  style,
  onPointerDown,
  onPointerMove,
  onPointerUp,
  onPointerCancel,
  onClickCapture,
  ...props
}: SheetContentProps) {
  const isBottom = side === "bottom";
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const closeRef = React.useRef<HTMLButtonElement | null>(null);
  const dragRef = React.useRef<DragState>({ ...initialDragState });

  // While a drag is engaged, block native scrolling/overscroll (a passive
  // pointermove cannot cancel it — only a non-passive touchmove can).
  React.useEffect(() => {
    if (!isBottom) return;
    const el = contentRef.current;
    if (!el) return;
    const onTouchMove = (e: TouchEvent) => {
      if (dragRef.current.dragging) e.preventDefault();
    };
    el.addEventListener("touchmove", onTouchMove, { passive: false });
    return () => el.removeEventListener("touchmove", onTouchMove);
  }, [isBottom]);

  const handlePointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    onPointerDown?.(e);
    if (!isBottom || e.defaultPrevented) return;
    const d = dragRef.current;
    d.justDragged = false;
    if (d.closing) return;
    if (!e.isPrimary || (e.pointerType === "mouse" && e.button !== 0)) return;
    const el = contentRef.current;
    if (!el) return;
    const target = e.target as HTMLElement;
    const onHandle = target.closest("[data-sheet-handle]") !== null;
    // Drags may start on the handle always, or on the body only when the
    // content under the pointer is not scrolled.
    if (!onHandle && hasScrolledAncestor(target, el)) return;
    d.pointerId = e.pointerId;
    d.startX = e.clientX;
    d.startY = e.clientY;
    d.dragging = false;
    d.translate = 0;
    d.lastY = e.clientY;
    d.lastT = e.timeStamp;
    d.velocity = 0;
  };

  const handlePointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    onPointerMove?.(e);
    const d = dragRef.current;
    if (!isBottom || d.closing || d.pointerId !== e.pointerId) return;
    const el = contentRef.current;
    if (!el) return;
    const dy = e.clientY - d.startY;
    const dx = e.clientX - d.startX;
    if (!d.dragging) {
      if (Math.abs(dy) < DRAG_SLOP_PX) return;
      if (Math.abs(dx) > Math.abs(dy)) {
        // Horizontal gesture — never a sheet drag.
        d.pointerId = null;
        return;
      }
      d.dragging = true;
      el.setPointerCapture(e.pointerId);
      el.style.transition = "none";
    }
    const dt = e.timeStamp - d.lastT;
    if (dt > 0) d.velocity = (e.clientY - d.lastY) / dt;
    d.lastY = e.clientY;
    d.lastT = e.timeStamp;
    // Follow the finger downward; never drag above the resting position.
    d.translate = Math.max(0, dy);
    el.style.transform = d.translate > 0 ? `translateY(${d.translate}px)` : "";
  };

  const finishDrag = (e: React.PointerEvent<HTMLDivElement>, cancelled: boolean) => {
    const d = dragRef.current;
    if (!isBottom || d.pointerId !== e.pointerId) return;
    const wasDragging = d.dragging;
    d.pointerId = null;
    d.dragging = false;
    const el = contentRef.current;
    if (!el || !wasDragging) return;
    if (el.hasPointerCapture(e.pointerId)) {
      el.releasePointerCapture(e.pointerId);
    }
    d.justDragged = true;
    const height = el.getBoundingClientRect().height || 1;
    const shouldClose =
      !cancelled &&
      (d.translate > height * CLOSE_FRACTION ||
        (d.velocity > FLICK_VELOCITY && d.translate > DRAG_SLOP_PX));
    if (shouldClose) {
      d.closing = true;
      // Slide the rest of the way out ourselves, and suppress the Radix
      // exit animation (it would snap back to 0 first).
      el.style.animationName = "none";
      el.style.transition = `transform ${RELEASE_MS}ms ease-in`;
      el.style.transform = "translateY(100%)";
      window.setTimeout(() => closeRef.current?.click(), RELEASE_MS);
    } else {
      // Settle back to rest.
      el.style.transition = `transform ${RELEASE_MS}ms ease`;
      el.style.transform = "";
      window.setTimeout(() => {
        el.style.transition = "";
      }, RELEASE_MS + 20);
    }
  };

  const handlePointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    onPointerUp?.(e);
    finishDrag(e, false);
  };

  const handlePointerCancel = (e: React.PointerEvent<HTMLDivElement>) => {
    onPointerCancel?.(e);
    finishDrag(e, true);
  };

  // Swallow the click synthesized at the end of a drag so buttons under the
  // finger don't activate.
  const handleClickCapture = (e: React.MouseEvent<HTMLDivElement>) => {
    const d = dragRef.current;
    if (d.justDragged) {
      d.justDragged = false;
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    onClickCapture?.(e);
  };

  return (
    <SheetPortal>
      <SheetOverlay />
      <SheetPrimitive.Content
        data-slot="sheet-content"
        ref={contentRef}
        className={cn(sheetVariants({ side }), className)}
        style={isBottom ? { paddingBottom: "env(safe-area-inset-bottom)", ...style } : style}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerCancel}
        onClickCapture={handleClickCapture}
        {...props}
      >
        {isBottom ? (
          <>
            {/* Grab handle: duplicates the overlay/Escape dismiss affordance,
                so it is hidden from assistive tech. */}
            <div
              data-sheet-handle
              aria-hidden="true"
              className="flex shrink-0 touch-none justify-center pt-3 pb-2"
            >
              <div className="h-1.5 w-10 rounded-full bg-muted-foreground/30" />
            </div>
            {/* Hidden Radix close used to route drag-dismiss through the
                normal onOpenChange path. */}
            <SheetPrimitive.Close
              ref={closeRef}
              className="hidden"
              tabIndex={-1}
              aria-hidden="true"
            />
          </>
        ) : (
          <SheetPrimitive.Close className="absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:pointer-events-none data-[state=open]:bg-secondary">
            <X className="h-4 w-4" />
            <span className="sr-only">Close</span>
          </SheetPrimitive.Close>
        )}
        {children}
      </SheetPrimitive.Content>
    </SheetPortal>
  );
}

function SheetHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sheet-header"
      className={cn("flex flex-col space-y-2 text-center sm:text-left", className)}
      {...props}
    />
  );
}

function SheetFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="sheet-footer"
      className={cn("flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2", className)}
      {...props}
    />
  );
}

function SheetTitle({ className, ...props }: React.ComponentProps<typeof SheetPrimitive.Title>) {
  return (
    <SheetPrimitive.Title
      data-slot="sheet-title"
      className={cn("text-lg font-semibold text-foreground", className)}
      {...props}
    />
  );
}

function SheetDescription({
  className,
  ...props
}: React.ComponentProps<typeof SheetPrimitive.Description>) {
  return (
    <SheetPrimitive.Description
      data-slot="sheet-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}

export {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetOverlay,
  SheetPortal,
  SheetTitle,
  SheetTrigger,
};

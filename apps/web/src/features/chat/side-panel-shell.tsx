// SPDX-License-Identifier: Apache-2.0

import { Fullscreen, Minimize2, X } from "lucide-react";
import {
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type PointerEvent as ReactPointerEvent,
  type Ref,
  useEffect,
  useRef,
  useState,
} from "react";
import { Button } from "../../components/ui/button";
import { maxPanelWidth, minPanelWidth, usePanelWidth } from "../../lib/panel-width";
import { cn } from "../../lib/utils";

/** Presentation only: each feature owns its content, actions, and delivery state. */
export function SidePanelShell({
  actions,
  autoFocusClose = true,
  bodyClassName,
  children,
  closeLabel = "Close panel",
  escapeHint = false,
  icon,
  maximizable = false,
  onClose,
  onKeyDown,
  rootRef,
  surface,
  restoreFocusOnClose = true,
  title,
  titleRef,
  titleTabIndex,
}: {
  actions?: ReactNode;
  autoFocusClose?: boolean;
  bodyClassName?: string;
  children: ReactNode;
  closeLabel?: string;
  /** Shows the "Esc to Close" hint; the owning surface decides when Escape closes. */
  escapeHint?: boolean;
  icon?: ReactNode;
  maximizable?: boolean;
  onClose: () => void;
  onKeyDown?: (event: ReactKeyboardEvent<HTMLElement>) => void;
  rootRef?: Ref<HTMLElement>;
  /** Marks the chat surface that owns Escape routing inside this panel. */
  surface?: string;
  restoreFocusOnClose?: boolean;
  title: string;
  titleRef?: Ref<HTMLHeadingElement>;
  titleTabIndex?: number;
}) {
  const width = usePanelWidth("contentPreview");
  const [maximized, setMaximized] = useState(false);
  const closeButton = useRef<HTMLButtonElement>(null);
  const resizeCleanup = useRef<(() => void) | null>(null);

  // Enter the sheet once. Deliveries rerender the body without taking focus
  // away from a reader's selection or composer.
  useEffect(() => {
    const returnFocus = document.activeElement;
    if (autoFocusClose) closeButton.current?.focus();
    return () => {
      resizeCleanup.current?.();
      if (restoreFocusOnClose && returnFocus instanceof HTMLElement && returnFocus.isConnected)
        returnFocus.focus();
    };
  }, [autoFocusClose, restoreFocusOnClose]);

  function startResize(event: ReactPointerEvent<HTMLButtonElement>) {
    event.currentTarget.focus();
    event.preventDefault();
    resizeCleanup.current?.();
    const startX = event.clientX;
    const startWidth = width.value;
    const resize = (moveEvent: PointerEvent) =>
      width.setValue(startWidth - moveEvent.clientX + startX);
    const finish = () => {
      window.removeEventListener("pointermove", resize);
      window.removeEventListener("pointerup", finish);
      window.removeEventListener("pointercancel", finish);
      resizeCleanup.current = null;
    };
    resizeCleanup.current = finish;
    window.addEventListener("pointermove", resize);
    window.addEventListener("pointerup", finish);
    window.addEventListener("pointercancel", finish);
  }

  return (
    <>
      <button
        aria-label="Dismiss panel backdrop"
        className="fixed inset-0 z-30 bg-black/35 min-[760px]:hidden"
        onClick={onClose}
        type="button"
      />
      <aside
        aria-label={title}
        data-chat-surface={surface}
        onKeyDown={onKeyDown}
        ref={rootRef}
        className={
          maximized
            ? "fixed inset-0 z-50 flex min-w-0 max-w-full flex-col overflow-x-hidden bg-background"
            : "fixed inset-x-0 bottom-0 z-40 flex h-[94dvh] w-full min-w-0 max-w-full flex-col rounded-t-2xl border bg-background shadow-2xl min-[760px]:relative min-[760px]:inset-auto min-[760px]:order-3 min-[760px]:h-full min-[760px]:w-[var(--content-panel-width)] min-[760px]:shrink-0 min-[760px]:rounded-none min-[760px]:border-y-0 min-[760px]:border-r-0"
        }
        style={
          maximized ? undefined : ({ "--content-panel-width": `${width.value}px` } as CSSProperties)
        }
      >
        {!maximized && (
          <button
            aria-label="Resize panel"
            className="absolute inset-y-0 -left-1 z-10 hidden w-2 cursor-col-resize touch-none border-0 bg-transparent p-0 hover:bg-brand/20 min-[760px]:block"
            onKeyDown={(event) => {
              if (event.key === "ArrowLeft") width.setValue(width.value + 12);
              else if (event.key === "ArrowRight") width.setValue(width.value - 12);
              else return;
              event.preventDefault();
            }}
            onPointerDown={startResize}
            title={`Resize panel (${minPanelWidth}–${maxPanelWidth}px)`}
            type="button"
          />
        )}
        {/* The header sits on the chat header's seam: 64px beside the chat, 56px as a phone's sheet. */}
        <header
          className={cn(
            "@container/panel-header flex h-14 shrink-0 items-center gap-3 border-b px-4",
            !maximized && "min-[760px]:h-16",
          )}
        >
          {icon}
          <h2
            className="min-w-0 flex-1 truncate text-sm font-semibold"
            ref={titleRef}
            tabIndex={titleTabIndex}
          >
            {title}
          </h2>
          {actions}
          {escapeHint && (
            // A narrow panel keeps its title; the hint returns once there is room.
            <span className="shrink-0 text-xs text-muted-foreground @max-[22rem]/panel-header:hidden">
              Esc to Close
            </span>
          )}
          <div className="flex shrink-0 items-center gap-1">
            {maximizable && (
              <Button
                aria-label={maximized ? "Restore panel" : "Maximize panel"}
                className="size-8 text-muted-foreground"
                onClick={() => setMaximized((current) => !current)}
                size="icon"
                variant="ghost"
              >
                {maximized ? (
                  <Minimize2 aria-hidden="true" className="size-4" />
                ) : (
                  <Fullscreen aria-hidden="true" className="size-4" />
                )}
              </Button>
            )}
            <Button
              aria-label={closeLabel}
              className="ml-1 size-8 text-muted-foreground"
              onClick={onClose}
              ref={closeButton}
              size="icon"
              variant="ghost"
            >
              <X aria-hidden="true" className="size-4" />
            </Button>
          </div>
        </header>
        <div
          className={`min-h-0 min-w-0 flex-1 ${bodyClassName ?? "overflow-auto"}`}
          data-testid="side-panel-body"
        >
          {children}
        </div>
      </aside>
    </>
  );
}

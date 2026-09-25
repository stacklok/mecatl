// SPDX-License-Identifier: Apache-2.0

import { Maximize2, PanelRightClose } from "lucide-react";
import {
  type CSSProperties,
  type Ref,
  type ReactNode,
  type PointerEvent as ReactPointerEvent,
  useEffect,
  useRef,
  useState,
} from "react";
import { Button } from "../../components/ui/button";
import { maxPanelWidth, minPanelWidth, usePanelWidth } from "../../lib/panel-width";

/** Presentation only: each feature owns its content, actions, and delivery state. */
export function SidePanelShell({
  actions,
  bodyClassName,
  children,
  closeLabel = "Close panel",
  icon,
  maximizable = false,
  onClose,
  title,
  titleRef,
  titleTabIndex,
}: {
  actions?: ReactNode;
  bodyClassName?: string;
  children: ReactNode;
  closeLabel?: string;
  icon?: ReactNode;
  maximizable?: boolean;
  onClose: () => void;
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
    closeButton.current?.focus();
    return () => {
      resizeCleanup.current?.();
      if (returnFocus instanceof HTMLElement && returnFocus.isConnected) returnFocus.focus();
    };
  }, []);

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
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          {icon}
          <h2
            className="min-w-0 flex-1 truncate text-sm font-semibold"
            ref={titleRef}
            tabIndex={titleTabIndex}
          >
            {title}
          </h2>
          {actions}
          {maximizable && (
            <Button
              aria-label={maximized ? "Restore panel" : "Maximize panel"}
              onClick={() => setMaximized((current) => !current)}
              size="icon"
              variant="ghost"
            >
              <Maximize2 aria-hidden="true" className={maximized ? "rotate-180" : undefined} />
            </Button>
          )}
          <Button
            aria-label={closeLabel}
            onClick={onClose}
            ref={closeButton}
            size="icon"
            variant="ghost"
          >
            <PanelRightClose aria-hidden="true" />
          </Button>
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

"use client";

import { Fullscreen, Minimize2, X } from "lucide-react";
import { useEffect, useRef } from "react";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { usePanelWidth } from "@/hooks/use-panel-width";
import { cn } from "@/lib/utils";

/** Maximize/restore + close controls, spaced apart so they read as two. */
function PanelWindowControls({
  maximized,
  onToggleMaximize,
  onClose,
  closeLabel,
}: {
  maximized: boolean;
  onToggleMaximize: () => void;
  onClose: () => void;
  closeLabel: string;
}) {
  return (
    <div className="flex items-center gap-1 shrink-0">
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-8 text-muted-foreground"
            onClick={onToggleMaximize}
            aria-label={maximized ? "Restore split view" : "Maximize panel"}
          >
            {maximized ? (
              <Minimize2 className="size-4" />
            ) : (
              <Fullscreen className="size-4" />
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent side="bottom">
          {maximized ? "Restore split view" : "Maximize"}
        </TooltipContent>
      </Tooltip>
      <Button
        variant="ghost"
        size="icon"
        className="size-8 text-muted-foreground ml-1"
        onClick={onClose}
        aria-label={closeLabel}
      >
        <X className="size-4" />
      </Button>
    </div>
  );
}

/**
 * The shared chrome for every right-hand chat panel (file preview, markdown
 * canvas, thread): a resizable/maximizable frame with a titled header and the
 * window controls. Bodies plug in as `children`; the frame owns the width,
 * the drag handle, the border (drawn only in the split view so it doesn't
 * double with the shell border when maximized), and the max/close buttons.
 */
export function SidePanel({
  icon: Icon,
  title,
  closeLabel,
  maximized,
  onToggleMaximize,
  onClose,
  headerExtra,
  toolbar,
  minWidth = 320,
  windowControls = true,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  closeLabel: string;
  maximized: boolean;
  onToggleMaximize: () => void;
  onClose: () => void;
  /** Extra header content rendered between the title and the window controls. */
  headerExtra?: React.ReactNode;
  /** Optional row rendered under the header (e.g. a formatting toolbar). */
  toolbar?: React.ReactNode;
  minWidth?: number;
  /** False inside the mobile sheet, whose grab handle owns dismissal. */
  windowControls?: boolean;
  children: React.ReactNode;
}) {
  // The persisted width shared with the session list, so one resize setting
  // carries across every chat panel and across reloads.
  const [width, setWidth] = usePanelWidth();
  const isDragging = useRef(false);
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (!isDragging.current || !panelRef.current) return;
      const parentRect =
        panelRef.current.parentElement?.getBoundingClientRect();
      if (!parentRect) return;
      const newWidth = parentRect.right - e.clientX;
      // The store clamps to its own global bounds; the parent-relative cap
      // keeps the conversation readable on narrow windows.
      setWidth(Math.max(minWidth, Math.min(newWidth, parentRect.width * 0.75)));
    };
    const handleMouseUp = () => {
      isDragging.current = false;
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    };
    window.addEventListener("mousemove", handleMouseMove);
    window.addEventListener("mouseup", handleMouseUp);
    return () => {
      window.removeEventListener("mousemove", handleMouseMove);
      window.removeEventListener("mouseup", handleMouseUp);
    };
  }, [minWidth, setWidth]);

  return (
    <div
      ref={panelRef}
      className={cn(
        "relative flex flex-col bg-background",
        // When maximized the panel is the leftmost element, so its own left
        // border would double up with the shell/nav border.
        // min-h-0 lets the panel CONSTRAIN inside a flex column (the mobile
        // bottom sheet) instead of growing past it — without it the inner
        // overflow-y-auto never engages and the thread can't scroll.
        maximized ? "min-h-0 flex-1" : "shrink-0 border-l border-border",
      )}
      style={maximized ? undefined : { width }}
    >
      {!maximized && (
        // biome-ignore lint/a11y/noStaticElementInteractions: resize drag handle
        <div
          className="absolute left-0 top-0 bottom-0 w-1.5 cursor-col-resize z-10 hover:bg-brand/20 active:bg-brand/30 transition-colors"
          onMouseDown={(e) => {
            // Prevent the drag from starting a text selection in the panel.
            e.preventDefault();
            isDragging.current = true;
            document.body.style.cursor = "col-resize";
            document.body.style.userSelect = "none";
          }}
        />
      )}

      {/* h-[60px] matches the chat and session-list headers on the same top
          seam (h-14 under the 500px mobile breakpoint), so the header borders
          align pixel-perfect across chat/list/panel. */}
      <div className="flex h-[60px] shrink-0 items-center gap-3 border-b border-border px-4 max-[499px]:h-14">
        <Icon className="size-4 text-muted-foreground shrink-0" />
        <h3 className="text-sm font-semibold truncate flex-1">{title}</h3>
        {headerExtra}
        {windowControls && (
          <PanelWindowControls
            maximized={maximized}
            onToggleMaximize={onToggleMaximize}
            onClose={onClose}
            closeLabel={closeLabel}
          />
        )}
      </div>

      {toolbar}

      {children}
    </div>
  );
}

"use client";

import { useRef } from "react";
import { cn } from "@/lib/utils";

interface ResizeHandleProps {
  direction?: "left" | "right";
  width: number;
  onWidthChange: (width: number) => void;
  min?: number;
  max?: number;
}

export function ResizeHandle({
  direction = "right",
  width,
  onWidthChange,
  min = 200,
  max = 720,
}: ResizeHandleProps) {
  const isDraggingRef = useRef(false);

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: drag-only resize handle
    <div
      className={cn(
        "absolute top-0 bottom-0 w-1 cursor-col-resize z-10 hover:bg-primary/20 active:bg-primary/30",
        direction === "right" ? "right-0" : "left-0",
      )}
      onMouseDown={(e) => {
        e.preventDefault();
        isDraggingRef.current = true;
        const startX = e.clientX;
        const startW = width;
        const sign = direction === "right" ? 1 : -1;
        const onMove = (ev: MouseEvent) => {
          if (!isDraggingRef.current) return;
          onWidthChange(
            Math.max(min, Math.min(max, startW + sign * (ev.clientX - startX))),
          );
        };
        const onUp = () => {
          isDraggingRef.current = false;
          document.removeEventListener("mousemove", onMove);
          document.removeEventListener("mouseup", onUp);
          document.body.style.cursor = "";
          document.body.style.userSelect = "";
        };
        document.body.style.cursor = "col-resize";
        document.body.style.userSelect = "none";
        document.addEventListener("mousemove", onMove);
        document.addEventListener("mouseup", onUp);
      }}
    />
  );
}

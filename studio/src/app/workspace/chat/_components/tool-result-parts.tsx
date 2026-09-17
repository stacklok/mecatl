"use client";

import type { ToolResultPart } from "@/features/agent";
import {
  resourceLinkLabel,
  safeImageDataUrl,
  safeResourceLinkHref,
} from "@/lib/tool-summary";
import { cn } from "@/lib/utils";

/**
 * The artifact parts of a tool result (the TUI's `↗ name` and `[image]`
 * lines): resource links as anchors, images as thumbnails. The parts are
 * tool-authored and untrusted — a link is an anchor ONLY when it is http(s)
 * and an image is decoded ONLY for the raster types a browser handles
 * safely; anything else degrades to a plain text label.
 */
export function ToolResultParts({
  parts,
  size = "sm",
  className,
}: {
  parts: ToolResultPart[];
  /** `lg` in the drill-down panel (bigger thumbnails). */
  size?: "sm" | "lg";
  className?: string;
}) {
  if (parts.length === 0) return null;
  return (
    <div
      data-testid="tool-result-parts"
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-1 text-xs",
        className,
      )}
    >
      {parts.map((part, index) => {
        const key = `${index}:${part.kind}`;
        if (part.kind === "resource_link") {
          const label = resourceLinkLabel(part);
          const href = safeResourceLinkHref(part.url);
          return href ? (
            <a
              key={key}
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              title={href}
              className="inline-flex max-w-full items-center gap-1 truncate text-brand underline-offset-2 hover:underline"
            >
              <span aria-hidden="true">↗</span>
              <span className="truncate">{label}</span>
            </a>
          ) : (
            <span
              key={key}
              title="Only http(s) links open; this one is shown as text."
              className="inline-flex max-w-full items-center gap-1 truncate text-muted-foreground"
            >
              <span aria-hidden="true">↗</span>
              <span className="truncate">{label}</span>
            </span>
          );
        }
        const src = safeImageDataUrl(part);
        return src ? (
          // A base64 thumbnail from the tool's own result: next/image cannot
          // optimise an inline data: URL, and the bytes never leave the page.
          // biome-ignore lint/performance/noImgElement: inline data: URL thumbnail
          <img
            key={key}
            src={src}
            alt="Returned by the tool"
            className={cn(
              "rounded border border-border object-contain",
              size === "lg" ? "max-h-64" : "max-h-16",
            )}
          />
        ) : (
          <span
            key={key}
            title={
              part.mimeType
                ? `${part.mimeType} is not shown inline`
                : "Image type unknown; not shown inline"
            }
            className="text-muted-foreground"
          >
            [image]
          </span>
        );
      })}
    </div>
  );
}
